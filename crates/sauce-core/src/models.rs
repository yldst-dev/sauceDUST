use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ImageMetadata {
    pub id: i64,
    pub source_site: String,
    pub source_post_id: String,
    pub source_url: Option<String>,
    pub canonical_url: Option<String>,
    pub file_url: Option<String>,
    pub preview_url: Option<String>,
    pub md5: Option<String>,
    pub phash: Option<String>,
    pub dhash: Option<String>,
    pub width: Option<i32>,
    pub height: Option<i32>,
    pub rating: Option<String>,
    pub score: Option<i32>,
    pub tags: Vec<String>,
    pub artist_tags: Vec<String>,
    pub indexed_at: DateTime<Utc>,
    pub updated_at: DateTime<Utc>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NewImageMetadata {
    pub source_site: String,
    pub source_post_id: String,
    pub source_url: Option<String>,
    pub canonical_url: Option<String>,
    pub file_url: Option<String>,
    pub preview_url: Option<String>,
    pub md5: Option<String>,
    pub phash: Option<String>,
    pub dhash: Option<String>,
    pub width: Option<i32>,
    pub height: Option<i32>,
    pub rating: Option<String>,
    pub score: Option<i32>,
    pub tags: Vec<String>,
    pub artist_tags: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SearchResult {
    pub score: f32,
    pub source_site: String,
    pub source_post_id: String,
    pub source_url: Option<String>,
    pub canonical_url: Option<String>,
    pub preview_url: Option<String>,
    pub rating: Option<String>,
    pub tags: Vec<String>,
    pub artist_tags: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SearchResponse {
    pub results: Vec<SearchResult>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EmbedResponse {
    pub vector_size: usize,
    pub vector: Vec<f32>,
    pub device: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Hashes {
    pub phash: String,
    pub dhash: String,
    pub width: u32,
    pub height: u32,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ReadyResponse {
    pub postgres: bool,
    pub qdrant: bool,
    pub embedding_worker: bool,
}
