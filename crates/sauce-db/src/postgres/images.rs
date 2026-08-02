use std::collections::{HashMap, HashSet};

use sauce_core::errors::{SauceError, SauceResult};
use sauce_core::models::{ImageMetadata, NewImageMetadata};
use sqlx::Row;

use super::{row_to_image, PostgresRepo};

impl PostgresRepo {
    pub async fn upsert_image(&self, image: &NewImageMetadata) -> SauceResult<ImageMetadata> {
        let row = sqlx::query(
            "INSERT INTO images (
                source_site, source_post_id, source_url, canonical_url, file_url, preview_url,
                md5, phash, dhash, width, height, rating, score, tags, artist_tags
            ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
            ON CONFLICT (source_site, source_post_id) DO UPDATE SET
                source_url = EXCLUDED.source_url,
                canonical_url = EXCLUDED.canonical_url,
                file_url = EXCLUDED.file_url,
                preview_url = EXCLUDED.preview_url,
                md5 = EXCLUDED.md5,
                phash = EXCLUDED.phash,
                dhash = EXCLUDED.dhash,
                width = EXCLUDED.width,
                height = EXCLUDED.height,
                rating = EXCLUDED.rating,
                score = EXCLUDED.score,
                tags = EXCLUDED.tags,
                artist_tags = EXCLUDED.artist_tags,
                updated_at = now()
            RETURNING *",
        )
        .bind(&image.source_site)
        .bind(&image.source_post_id)
        .bind(&image.source_url)
        .bind(&image.canonical_url)
        .bind(&image.file_url)
        .bind(&image.preview_url)
        .bind(&image.md5)
        .bind(&image.phash)
        .bind(&image.dhash)
        .bind(image.width)
        .bind(image.height)
        .bind(&image.rating)
        .bind(image.score)
        .bind(&image.tags)
        .bind(&image.artist_tags)
        .fetch_one(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        row_to_image(row)
    }

    pub async fn get_image(&self, id: i64) -> SauceResult<Option<ImageMetadata>> {
        let row = sqlx::query("SELECT * FROM images WHERE id = $1")
            .bind(id)
            .fetch_optional(&self.pool)
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;

        row.map(row_to_image).transpose()
    }

    pub async fn get_image_by_source_post_id(
        &self,
        source_site: &str,
        source_post_id: &str,
    ) -> SauceResult<Option<ImageMetadata>> {
        let row =
            sqlx::query("SELECT * FROM images WHERE source_site = $1 AND source_post_id = $2")
                .bind(source_site)
                .bind(source_post_id)
                .fetch_optional(&self.pool)
                .await
                .map_err(|err| SauceError::Database(err.to_string()))?;

        row.map(row_to_image).transpose()
    }

    pub async fn get_images_by_ids(&self, ids: &[i64]) -> SauceResult<Vec<ImageMetadata>> {
        let rows = sqlx::query("SELECT * FROM images WHERE id = ANY($1)")
            .bind(ids)
            .fetch_all(&self.pool)
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;

        rows.into_iter().map(row_to_image).collect()
    }

    pub async fn existing_source_post_ids(
        &self,
        source_site: &str,
        source_post_ids: &[String],
    ) -> SauceResult<HashSet<String>> {
        if source_post_ids.is_empty() {
            return Ok(HashSet::new());
        }

        let rows = sqlx::query(
            "SELECT source_post_id FROM images
            WHERE source_site = $1 AND source_post_id = ANY($2)",
        )
        .bind(source_site)
        .bind(source_post_ids)
        .fetch_all(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        rows.into_iter()
            .map(|row| {
                row.try_get("source_post_id")
                    .map_err(|err| SauceError::Database(err.to_string()))
            })
            .collect()
    }

    pub async fn existing_source_post_image_ids(
        &self,
        source_site: &str,
        source_post_ids: &[String],
    ) -> SauceResult<HashMap<String, i64>> {
        if source_post_ids.is_empty() {
            return Ok(HashMap::new());
        }

        let rows = sqlx::query(
            "SELECT id, source_post_id FROM images
            WHERE source_site = $1 AND source_post_id = ANY($2)",
        )
        .bind(source_site)
        .bind(source_post_ids)
        .fetch_all(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        rows.into_iter()
            .map(|row| {
                Ok((
                    row.try_get("source_post_id")
                        .map_err(|err| SauceError::Database(err.to_string()))?,
                    row.try_get("id")
                        .map_err(|err| SauceError::Database(err.to_string()))?,
                ))
            })
            .collect()
    }

    pub async fn mark_image_vector_indexed(&self, image_id: i64) -> SauceResult<()> {
        sqlx::query(
            "UPDATE images
            SET vector_indexed_at = now(), updated_at = now()
            WHERE id = $1",
        )
        .bind(image_id)
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }
}
