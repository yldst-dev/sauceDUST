use std::collections::HashSet;

use qdrant_client::qdrant::{
    point_id::PointIdOptions, value::Kind, CountPointsBuilder, CreateCollectionBuilder, Distance,
    GetPointsBuilder, PointStruct, QueryPointsBuilder, ScoredPoint, UpsertPointsBuilder, Value,
    VectorParamsBuilder,
};
use qdrant_client::{Payload, Qdrant};
use sauce_core::errors::{SauceError, SauceResult};
use sauce_core::models::ImageMetadata;

#[derive(Clone)]
pub struct QdrantRepo {
    client: Qdrant,
    collection: String,
    vector_size: u64,
}

#[derive(Debug, Clone)]
pub struct VectorMatch {
    pub image_id: i64,
    pub score: f32,
}

impl QdrantRepo {
    pub fn new(url: &str, collection: impl Into<String>, vector_size: u64) -> SauceResult<Self> {
        let client = Qdrant::from_url(&grpc_url(url))
            .build()
            .map_err(|err| SauceError::Vector(err.to_string()))?;

        Ok(Self {
            client,
            collection: collection.into(),
            vector_size,
        })
    }

    pub async fn ensure_collection(&self) -> SauceResult<()> {
        let exists = self
            .client
            .collection_exists(&self.collection)
            .await
            .map_err(|err| SauceError::Vector(err.to_string()))?;

        if !exists {
            self.client
                .create_collection(
                    CreateCollectionBuilder::new(&self.collection).vectors_config(
                        VectorParamsBuilder::new(self.vector_size, Distance::Cosine),
                    ),
                )
                .await
                .map_err(|err| SauceError::Vector(err.to_string()))?;
        }

        Ok(())
    }

    pub async fn ping(&self) -> bool {
        self.client.health_check().await.is_ok()
    }

    pub async fn count(&self) -> SauceResult<u64> {
        let result = self
            .client
            .count(CountPointsBuilder::new(&self.collection).exact(true))
            .await
            .map_err(|err| SauceError::Vector(err.to_string()))?;

        Ok(result.result.map(|count| count.count).unwrap_or_default())
    }

    pub async fn existing_point_ids(&self, ids: &[i64]) -> SauceResult<HashSet<i64>> {
        if ids.is_empty() {
            return Ok(HashSet::new());
        }

        let point_ids = ids
            .iter()
            .filter_map(|id| u64::try_from(*id).ok())
            .map(Into::into)
            .collect::<Vec<_>>();
        if point_ids.is_empty() {
            return Ok(HashSet::new());
        }

        let response = self
            .client
            .get_points(
                GetPointsBuilder::new(&self.collection, point_ids)
                    .with_payload(false)
                    .with_vectors(false),
            )
            .await
            .map_err(|err| SauceError::Vector(err.to_string()))?;

        Ok(response
            .result
            .into_iter()
            .filter_map(|point| match point.id?.point_id_options? {
                PointIdOptions::Num(id) => i64::try_from(id).ok(),
                PointIdOptions::Uuid(_) => None,
            })
            .collect())
    }

    pub async fn upsert_image_vector(
        &self,
        metadata: &ImageMetadata,
        vector: Vec<f32>,
    ) -> SauceResult<()> {
        let payload = image_payload(metadata);
        let point = PointStruct::new(metadata.id as u64, vector, payload);

        self.client
            .upsert_points(UpsertPointsBuilder::new(&self.collection, vec![point]))
            .await
            .map_err(|err| SauceError::Vector(err.to_string()))?;

        Ok(())
    }

    pub async fn search(&self, vector: Vec<f32>, limit: u64) -> SauceResult<Vec<VectorMatch>> {
        let response = self
            .client
            .query(
                QueryPointsBuilder::new(&self.collection)
                    .query(vector)
                    .limit(limit)
                    .with_payload(true),
            )
            .await
            .map_err(|err| SauceError::Vector(err.to_string()))?;

        response
            .result
            .into_iter()
            .map(scored_point_to_match)
            .collect()
    }

    pub async fn clear_collection(&self) -> SauceResult<()> {
        let exists = self
            .client
            .collection_exists(&self.collection)
            .await
            .map_err(|err| SauceError::Vector(err.to_string()))?;

        if exists {
            self.client
                .delete_collection(&self.collection)
                .await
                .map_err(|err| SauceError::Vector(err.to_string()))?;
        }

        self.ensure_collection().await
    }
}

fn grpc_url(url: &str) -> String {
    url.trim_end_matches('/')
        .replace(":6333", ":6334")
        .to_owned()
}

fn image_payload(metadata: &ImageMetadata) -> Payload {
    let mut payload = Payload::new();
    payload.insert("image_id".to_owned(), metadata.id);
    payload.insert("source_site".to_owned(), metadata.source_site.clone());
    payload.insert("source_post_id".to_owned(), metadata.source_post_id.clone());
    insert_optional(&mut payload, "source_url", &metadata.source_url);
    insert_optional(&mut payload, "canonical_url", &metadata.canonical_url);
    insert_optional(&mut payload, "preview_url", &metadata.preview_url);
    insert_optional(&mut payload, "md5", &metadata.md5);
    insert_optional(&mut payload, "phash", &metadata.phash);
    insert_optional(&mut payload, "dhash", &metadata.dhash);
    insert_optional(&mut payload, "rating", &metadata.rating);
    if let Some(score) = metadata.score {
        payload.insert("score".to_owned(), i64::from(score));
    }
    if let Some(width) = metadata.width {
        payload.insert("width".to_owned(), i64::from(width));
    }
    if let Some(height) = metadata.height {
        payload.insert("height".to_owned(), i64::from(height));
    }
    let tags = metadata
        .tags
        .iter()
        .map(|tag| Value {
            kind: Some(Kind::StringValue(tag.clone())),
        })
        .collect();
    payload.insert(
        "tags".to_owned(),
        Value {
            kind: Some(Kind::ListValue(qdrant_client::qdrant::ListValue {
                values: tags,
            })),
        },
    );
    let artist_tags = metadata
        .artist_tags
        .iter()
        .map(|tag| Value {
            kind: Some(Kind::StringValue(tag.clone())),
        })
        .collect();
    payload.insert(
        "artist_tags".to_owned(),
        Value {
            kind: Some(Kind::ListValue(qdrant_client::qdrant::ListValue {
                values: artist_tags,
            })),
        },
    );
    payload
}

fn insert_optional(payload: &mut Payload, key: &str, value: &Option<String>) {
    if let Some(value) = value {
        payload.insert(key.to_owned(), value.clone());
    }
}

fn scored_point_to_match(point: ScoredPoint) -> SauceResult<VectorMatch> {
    let image_id = point
        .payload
        .get("image_id")
        .and_then(value_to_i64)
        .ok_or_else(|| SauceError::Vector("missing image_id payload".to_owned()))?;

    Ok(VectorMatch {
        image_id,
        score: point.score,
    })
}

fn value_to_i64(value: &Value) -> Option<i64> {
    match &value.kind {
        Some(Kind::IntegerValue(value)) => Some(*value),
        Some(Kind::DoubleValue(value)) => Some(*value as i64),
        _ => None,
    }
}
