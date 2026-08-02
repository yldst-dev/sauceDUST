use std::collections::HashMap;

use axum::extract::{Multipart, Path, State};
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use sauce_core::errors::SauceError;
use sauce_core::image_preprocess::{prepare_query_image, sha256_hex};
use sauce_core::models::{ReadyResponse, SearchResponse, SearchResult};

use crate::state::AppState;

pub fn router(state: AppState) -> Router {
    Router::new()
        .route("/health", get(health))
        .route("/ready", get(ready))
        .route("/search", post(search))
        .route("/images/:id", get(get_image))
        .route(
            "/images/source/:source_site/:source_post_id",
            get(get_image_by_source_post_id),
        )
        .with_state(state)
}

async fn health() -> Json<serde_json::Value> {
    Json(serde_json::json!({ "status": "ok" }))
}

async fn ready(State(state): State<AppState>) -> Json<ReadyResponse> {
    Json(ReadyResponse {
        postgres: state.postgres.ping().await,
        qdrant: state.qdrant.ping().await,
        embedding_worker: state.embedding.health().await,
    })
}

async fn get_image(
    State(state): State<AppState>,
    Path(id): Path<i64>,
) -> Result<Response, ApiError> {
    match state.postgres.get_image(id).await? {
        Some(image) => Ok(Json(image).into_response()),
        None => Ok(StatusCode::NOT_FOUND.into_response()),
    }
}

async fn get_image_by_source_post_id(
    State(state): State<AppState>,
    Path((source_site, source_post_id)): Path<(String, String)>,
) -> Result<Response, ApiError> {
    match state
        .postgres
        .get_image_by_source_post_id(&source_site, &source_post_id)
        .await?
    {
        Some(image) => Ok(Json(image).into_response()),
        None => Ok(StatusCode::NOT_FOUND.into_response()),
    }
}

async fn search(
    State(state): State<AppState>,
    multipart: Multipart,
) -> Result<Json<SearchResponse>, ApiError> {
    let bytes = extract_file(multipart).await?;
    let sha256 = sha256_hex(&bytes);
    let vector = match state.postgres.get_cached_query_embedding(&sha256).await? {
        Some(vector) => vector,
        None => {
            let prepared = prepare_query_image(&bytes)?;
            let embedding = state
                .embedding
                .embed_image(prepared, "query-image.jpg")
                .await?;
            state
                .postgres
                .upsert_cached_query_embedding(&sha256, &embedding.vector)
                .await?;
            embedding.vector
        }
    };
    let matches = state.qdrant.search(vector, 5).await?;
    let ids: Vec<i64> = matches.iter().map(|item| item.image_id).collect();
    let images = state.postgres.get_images_by_ids(&ids).await?;
    let by_id: HashMap<i64, _> = images.into_iter().map(|image| (image.id, image)).collect();
    let results = matches
        .into_iter()
        .filter_map(|item| {
            by_id.get(&item.image_id).map(|image| SearchResult {
                score: item.score,
                source_site: image.source_site.clone(),
                source_post_id: image.source_post_id.clone(),
                source_url: image.source_url.clone(),
                canonical_url: image.canonical_url.clone(),
                preview_url: image.preview_url.clone(),
                rating: image.rating.clone(),
                tags: image.tags.clone(),
                artist_tags: image.artist_tags.clone(),
            })
        })
        .collect();

    Ok(Json(SearchResponse { results }))
}

async fn extract_file(mut multipart: Multipart) -> Result<Vec<u8>, ApiError> {
    while let Some(field) = multipart
        .next_field()
        .await
        .map_err(|err| ApiError::bad_request(err.to_string()))?
    {
        if field.name() == Some("file") {
            let bytes = field
                .bytes()
                .await
                .map_err(|err| ApiError::bad_request(err.to_string()))?;
            if bytes.is_empty() {
                return Err(ApiError::bad_request("empty file"));
            }
            return Ok(bytes.to_vec());
        }
    }

    Err(ApiError::bad_request("missing multipart file field"))
}

struct ApiError {
    status: StatusCode,
    message: String,
}

impl ApiError {
    fn bad_request(message: impl Into<String>) -> Self {
        Self {
            status: StatusCode::BAD_REQUEST,
            message: message.into(),
        }
    }
}

impl From<SauceError> for ApiError {
    fn from(value: SauceError) -> Self {
        Self {
            status: StatusCode::INTERNAL_SERVER_ERROR,
            message: value.to_string(),
        }
    }
}

impl IntoResponse for ApiError {
    fn into_response(self) -> Response {
        (
            self.status,
            Json(serde_json::json!({
                "error": self.message
            })),
        )
            .into_response()
    }
}
