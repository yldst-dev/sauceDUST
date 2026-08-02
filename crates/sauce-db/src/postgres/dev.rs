use sauce_core::errors::{SauceError, SauceResult};

use super::PostgresRepo;

impl PostgresRepo {
    pub async fn clear_dev(&self) -> SauceResult<()> {
        sqlx::query(
            "TRUNCATE crawl_post_retries, crawl_ranges, crawl_states, query_embedding_cache, index_failures, index_jobs, images RESTART IDENTITY CASCADE",
        )
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }
}
