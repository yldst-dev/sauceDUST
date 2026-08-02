use sauce_core::errors::{SauceError, SauceResult};
use sqlx::Row;

use super::PostgresRepo;

impl PostgresRepo {
    pub async fn create_index_job(&self, source_site: &str, total: i32) -> SauceResult<i64> {
        let row = sqlx::query(
            "INSERT INTO index_jobs (source_site, status, total) VALUES ($1, $2, $3) RETURNING id",
        )
        .bind(source_site)
        .bind("running")
        .bind(total)
        .fetch_one(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        row.try_get("id")
            .map_err(|err| SauceError::Database(err.to_string()))
    }

    pub async fn finish_index_job(
        &self,
        job_id: i64,
        status: &str,
        succeeded: i32,
        failed: i32,
    ) -> SauceResult<()> {
        sqlx::query(
            "UPDATE index_jobs SET status = $1, succeeded = $2, failed = $3, finished_at = now() WHERE id = $4",
        )
        .bind(status)
        .bind(succeeded)
        .bind(failed)
        .bind(job_id)
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }

    pub async fn record_failure(
        &self,
        job_id: i64,
        source_site: &str,
        source_post_id: Option<&str>,
        url: Option<&str>,
        reason: &str,
    ) -> SauceResult<()> {
        sqlx::query(
            "INSERT INTO index_failures (job_id, source_site, source_post_id, url, reason) VALUES ($1, $2, $3, $4, $5)",
        )
        .bind(job_id)
        .bind(source_site)
        .bind(source_post_id)
        .bind(url)
        .bind(reason)
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }
}
