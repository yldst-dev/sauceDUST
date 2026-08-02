mod cache;
mod crawl_metrics;
mod crawl_state;
mod dev;
mod images;
mod jobs;
mod post_retries;

use std::env;
use std::time::Duration;

use chrono::{DateTime, Utc};
use sauce_core::errors::{SauceError, SauceResult};
use sauce_core::models::ImageMetadata;
use sqlx::{postgres::PgPoolOptions, PgPool, Row};

const INIT_SQL: &str = include_str!("../../../../migrations/0001_init.sql");
const DEFAULT_POOL_HEADROOM: usize = 8;
const MIN_POOL_CONNECTIONS: u32 = 16;
const MAX_AUTO_POOL_CONNECTIONS: u32 = 95;
const MAX_CONFIGURED_POOL_CONNECTIONS: u32 = 95;

#[derive(Clone)]
pub struct PostgresRepo {
    pub(super) pool: PgPool,
}

#[derive(Debug, Clone)]
pub struct CrawlState {
    pub high_watermark_id: Option<i64>,
    pub backfill_before_id: Option<i64>,
}

#[derive(Debug, Clone)]
pub struct CrawlRangeLease {
    pub id: i64,
    pub lower_id: i64,
    pub upper_id: i64,
    pub attempts: i32,
}

#[derive(Debug, Clone)]
pub struct CrawlProgressStats {
    pub high_watermark_id: Option<i64>,
    pub backfill_before_id: Option<i64>,
    pub running_ranges: i64,
    pub failed_ranges: i64,
    pub completed_ranges: i64,
    pub empty_ranges: i64,
}

pub use crawl_metrics::CrawlDirectionCounts;
pub use post_retries::{CrawlPostRetryInput, CrawlPostRetryLease};

impl PostgresRepo {
    pub async fn connect(database_url: &str) -> SauceResult<Self> {
        let max_connections = postgres_max_connections();
        let acquire_timeout = postgres_acquire_timeout();
        let pool = PgPoolOptions::new()
            .max_connections(max_connections)
            .acquire_timeout(acquire_timeout)
            .connect(database_url)
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(Self { pool })
    }

    pub fn pool(&self) -> &PgPool {
        &self.pool
    }

    pub async fn apply_migrations(&self) -> SauceResult<()> {
        let mut tx = self
            .pool
            .begin()
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;
        for statement in INIT_SQL.split(';') {
            let statement = statement.trim();
            if !statement.is_empty() {
                sqlx::query(statement)
                    .execute(&mut *tx)
                    .await
                    .map_err(|err| SauceError::Database(err.to_string()))?;
            }
        }
        tx.commit()
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }

    pub async fn ping(&self) -> bool {
        sqlx::query("SELECT 1").execute(&self.pool).await.is_ok()
    }

    pub async fn image_count(&self) -> SauceResult<i64> {
        let row = sqlx::query("SELECT COUNT(*) AS count FROM images")
            .fetch_one(&self.pool)
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;

        row.try_get("count")
            .map_err(|err| SauceError::Database(err.to_string()))
    }

    pub async fn database_size_bytes(&self) -> SauceResult<i64> {
        let row = sqlx::query("SELECT pg_database_size(current_database()) AS size")
            .fetch_one(&self.pool)
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;

        row.try_get("size")
            .map_err(|err| SauceError::Database(err.to_string()))
    }
}

fn postgres_max_connections() -> u32 {
    if let Ok(value) = env::var("POSTGRES_MAX_CONNECTIONS") {
        if let Ok(parsed) = value.parse::<u32>() {
            return parsed.clamp(1, MAX_CONFIGURED_POOL_CONNECTIONS);
        }
    }

    let crawl_concurrency =
        env_usize_with_fallback("CRAWL_PIPELINE_CONCURRENCY", "CRAWL_CONCURRENCY", 2).max(1);
    let database_concurrency =
        env_usize("CRAWL_DATABASE_CONCURRENCY", crawl_concurrency.clamp(1, 32)).max(1);
    let estimated = database_concurrency.saturating_add(DEFAULT_POOL_HEADROOM);
    (estimated as u32).clamp(MIN_POOL_CONNECTIONS, MAX_AUTO_POOL_CONNECTIONS)
}

fn postgres_acquire_timeout() -> Duration {
    Duration::from_secs(env_u64("POSTGRES_ACQUIRE_TIMEOUT_SECS", 120).max(1))
}

fn env_usize(key: &str, default: usize) -> usize {
    env::var(key)
        .ok()
        .and_then(|value| {
            let value = value.trim();
            if value.is_empty() {
                None
            } else {
                value.parse().ok()
            }
        })
        .unwrap_or(default)
}

fn env_usize_with_fallback(key: &str, fallback_key: &str, default: usize) -> usize {
    env::var(key)
        .ok()
        .and_then(|value| value.parse().ok())
        .unwrap_or_else(|| env_usize(fallback_key, default))
}

fn env_u64(key: &str, default: u64) -> u64 {
    env::var(key)
        .ok()
        .and_then(|value| value.parse().ok())
        .unwrap_or(default)
}

pub(super) fn row_to_image(row: sqlx::postgres::PgRow) -> SauceResult<ImageMetadata> {
    Ok(ImageMetadata {
        id: row
            .try_get::<i64, _>("id")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        source_site: row
            .try_get::<String, _>("source_site")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        source_post_id: row
            .try_get::<String, _>("source_post_id")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        source_url: row
            .try_get::<Option<String>, _>("source_url")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        canonical_url: row
            .try_get::<Option<String>, _>("canonical_url")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        file_url: row
            .try_get::<Option<String>, _>("file_url")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        preview_url: row
            .try_get::<Option<String>, _>("preview_url")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        md5: row
            .try_get::<Option<String>, _>("md5")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        phash: row
            .try_get::<Option<String>, _>("phash")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        dhash: row
            .try_get::<Option<String>, _>("dhash")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        width: row
            .try_get::<Option<i32>, _>("width")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        height: row
            .try_get::<Option<i32>, _>("height")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        rating: row
            .try_get::<Option<String>, _>("rating")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        score: row
            .try_get::<Option<i32>, _>("score")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        tags: row
            .try_get::<Vec<String>, _>("tags")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        artist_tags: row
            .try_get::<Vec<String>, _>("artist_tags")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        indexed_at: row
            .try_get::<DateTime<Utc>, _>("indexed_at")
            .map_err(|err| SauceError::Database(err.to_string()))?,
        updated_at: row
            .try_get::<DateTime<Utc>, _>("updated_at")
            .map_err(|err| SauceError::Database(err.to_string()))?,
    })
}
