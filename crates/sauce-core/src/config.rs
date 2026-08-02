use std::env;
use std::time::Duration;

use crate::errors::{SauceError, SauceResult};

#[derive(Debug, Clone)]
pub struct AppConfig {
    pub database_url: String,
    pub qdrant_url: String,
    pub embedding_worker_url: String,
    pub api_bind: String,
    pub danbooru_base_url: String,
    pub danbooru_connect_base_url: String,
    pub danbooru_host_header: Option<String>,
    pub danbooru_user_agent: String,
    pub danbooru_latest_tags: String,
    pub index_limit: u32,
    pub index_tags: String,
    pub vector_size: u64,
    pub request_timeout: Duration,
    pub crawl_delay: Duration,
    pub crawl_concurrency: usize,
    pub crawl_embedding_concurrency: usize,
    pub crawl_database_concurrency: usize,
    pub crawl_backfill_workers: usize,
    pub crawl_backfill_active_ranges: usize,
    pub crawl_backfill_range_size: i64,
    pub crawl_backfill_idle: Duration,
    pub verbose_index_logs: bool,
}

impl AppConfig {
    pub fn from_env() -> SauceResult<Self> {
        dotenvy::dotenv().ok();
        let crawl_concurrency =
            parse_var_with_fallback("CRAWL_PIPELINE_CONCURRENCY", "CRAWL_CONCURRENCY", 2usize)?
                .max(1);
        let crawl_embedding_concurrency = parse_var(
            "CRAWL_EMBEDDING_CONCURRENCY",
            default_embedding_concurrency(crawl_concurrency),
        )?
        .max(1);
        let crawl_database_concurrency = parse_var(
            "CRAWL_DATABASE_CONCURRENCY",
            default_database_concurrency(crawl_concurrency),
        )?
        .clamp(1, 64);
        let crawl_backfill_workers = parse_var("CRAWL_BACKFILL_WORKERS", 2usize)?.max(1);
        let crawl_backfill_active_ranges = parse_var(
            "CRAWL_BACKFILL_ACTIVE_RANGES",
            default_backfill_active_ranges(crawl_concurrency, crawl_backfill_workers),
        )?
        .max(1);

        Ok(Self {
            database_url: var(
                "DATABASE_URL",
                "postgres://sauce:saucepass@localhost:5432/sauce",
            ),
            qdrant_url: var("QDRANT_URL", "http://localhost:6333"),
            embedding_worker_url: var("EMBEDDING_WORKER_URL", "http://localhost:8100"),
            api_bind: var("SAUCE_API_BIND", "0.0.0.0:8000"),
            danbooru_base_url: var("DANBOORU_BASE_URL", "https://danbooru.donmai.us"),
            danbooru_connect_base_url: var("DANBOORU_CONNECT_BASE_URL", "https://donmai.us"),
            danbooru_host_header: optional_var("DANBOORU_HOST_HEADER")
                .or_else(|| Some("danbooru.donmai.us".to_owned())),
            danbooru_user_agent: var("DANBOORU_USER_AGENT", "saucedust/0.1.0"),
            danbooru_latest_tags: var("DANBOORU_LATEST_TAGS", "rating:g"),
            index_limit: parse_var("INDEX_LIMIT", 1000)?,
            index_tags: var("INDEX_TAGS", "rating:g"),
            vector_size: parse_var("VECTOR_SIZE", 512)?,
            request_timeout: Duration::from_secs(parse_var("REQUEST_TIMEOUT_SECS", 30)?),
            crawl_delay: Duration::from_millis(parse_var("CRAWL_DELAY_MS", 500)?),
            crawl_concurrency,
            crawl_embedding_concurrency,
            crawl_database_concurrency,
            crawl_backfill_workers,
            crawl_backfill_active_ranges,
            crawl_backfill_range_size: parse_var("CRAWL_BACKFILL_RANGE_SIZE", 10_000)?.max(1),
            crawl_backfill_idle: Duration::from_secs(
                parse_var("CRAWL_BACKFILL_IDLE_SECS", 30)?.max(1),
            ),
            verbose_index_logs: parse_bool("SAUCEDUST_VERBOSE_INDEX_LOGS", false),
        })
    }
}

fn var(key: &str, default: &str) -> String {
    env::var(key).unwrap_or_else(|_| default.to_owned())
}

fn optional_var(key: &str) -> Option<String> {
    env::var(key).ok().and_then(|value| {
        let value = value.trim();
        if value.is_empty() {
            None
        } else {
            Some(value.to_owned())
        }
    })
}

fn parse_var<T>(key: &str, default: T) -> SauceResult<T>
where
    T: std::str::FromStr + Copy,
    T::Err: std::fmt::Display,
{
    match env::var(key) {
        Ok(value) if value.trim().is_empty() => Ok(default),
        Ok(value) => value
            .trim()
            .parse()
            .map_err(|err| SauceError::Config(format!("{key}: {err}"))),
        Err(_) => Ok(default),
    }
}

fn parse_var_with_fallback<T>(key: &str, fallback_key: &str, default: T) -> SauceResult<T>
where
    T: std::str::FromStr + Copy,
    T::Err: std::fmt::Display,
{
    match env::var(key) {
        Ok(value) if value.trim().is_empty() => Ok(default),
        Ok(value) => value
            .trim()
            .parse()
            .map_err(|err| SauceError::Config(format!("{key}: {err}"))),
        Err(_) => parse_var(fallback_key, default),
    }
}

fn parse_bool(key: &str, default: bool) -> bool {
    env::var(key)
        .ok()
        .map(|value| {
            matches!(
                value.trim().to_ascii_lowercase().as_str(),
                "1" | "true" | "yes" | "on"
            )
        })
        .unwrap_or(default)
}

fn default_embedding_concurrency(crawl_concurrency: usize) -> usize {
    crawl_concurrency
}

fn default_database_concurrency(crawl_concurrency: usize) -> usize {
    crawl_concurrency.clamp(1, 64)
}

fn default_backfill_active_ranges(
    crawl_concurrency: usize,
    crawl_backfill_workers: usize,
) -> usize {
    crawl_backfill_workers.min(crawl_concurrency.clamp(1, 32))
}
