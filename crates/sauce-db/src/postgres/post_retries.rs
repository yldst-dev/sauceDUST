use std::time::Duration;

use sauce_core::errors::{SauceError, SauceResult};
use sqlx::Row;

use super::PostgresRepo;

#[derive(Debug, Clone)]
pub struct CrawlPostRetryInput {
    pub source_post_id: String,
    pub url: Option<String>,
    pub reason: String,
}

#[derive(Debug, Clone)]
pub struct CrawlPostRetryLease {
    pub id: i64,
    pub source_post_id: String,
    pub attempts: i32,
}

impl PostgresRepo {
    pub async fn enqueue_crawl_post_retries(
        &self,
        source_site: &str,
        scope_key: &str,
        direction: &str,
        failures: &[CrawlPostRetryInput],
        delay: Duration,
    ) -> SauceResult<()> {
        if failures.is_empty() {
            return Ok(());
        }

        let delay_secs = delay.as_secs().max(1) as i64;
        let mut tx = self
            .pool
            .begin()
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;

        for failure in failures {
            sqlx::query(
                "INSERT INTO crawl_post_retries (
                    source_site, scope_key, source_post_id, direction, url, reason, status, next_attempt_at
                ) VALUES ($1, $2, $3, $4, $5, $6, 'pending', now() + ($7 * interval '1 second'))
                ON CONFLICT (source_site, scope_key, source_post_id)
                DO UPDATE SET
                    direction = EXCLUDED.direction,
                    url = EXCLUDED.url,
                    reason = EXCLUDED.reason,
                    status = 'pending',
                    next_attempt_at = now() + ($7 * interval '1 second'),
                    completed_at = NULL,
                    updated_at = now()
                WHERE crawl_post_retries.status <> 'succeeded'",
            )
            .bind(source_site)
            .bind(scope_key)
            .bind(&failure.source_post_id)
            .bind(direction)
            .bind(&failure.url)
            .bind(&failure.reason)
            .bind(delay_secs)
            .execute(&mut *tx)
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;
        }

        tx.commit()
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }

    pub async fn lease_crawl_post_retries(
        &self,
        source_site: &str,
        scope_key: &str,
        limit: i64,
    ) -> SauceResult<Vec<CrawlPostRetryLease>> {
        let rows = sqlx::query(
            "WITH selected AS (
                SELECT id FROM crawl_post_retries
                WHERE source_site = $1
                    AND scope_key = $2
                    AND (
                        (status = 'pending' AND next_attempt_at <= now())
                        OR (status = 'running' AND leased_at < now() - interval '30 minutes')
                    )
                ORDER BY attempts ASC, next_attempt_at ASC, id ASC
                LIMIT $3
                FOR UPDATE SKIP LOCKED
            )
            UPDATE crawl_post_retries retry
            SET status = 'running',
                attempts = retry.attempts + 1,
                leased_at = now(),
                updated_at = now()
            FROM selected
            WHERE retry.id = selected.id
            RETURNING retry.id, retry.source_post_id, retry.attempts",
        )
        .bind(source_site)
        .bind(scope_key)
        .bind(limit.max(1))
        .fetch_all(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        rows.into_iter()
            .map(|row| {
                Ok(CrawlPostRetryLease {
                    id: row
                        .try_get("id")
                        .map_err(|err| SauceError::Database(err.to_string()))?,
                    source_post_id: row
                        .try_get("source_post_id")
                        .map_err(|err| SauceError::Database(err.to_string()))?,
                    attempts: row
                        .try_get("attempts")
                        .map_err(|err| SauceError::Database(err.to_string()))?,
                })
            })
            .collect()
    }

    pub async fn finish_crawl_post_retry(
        &self,
        id: i64,
        attempts: i32,
        status: &str,
        reason: Option<&str>,
    ) -> SauceResult<()> {
        sqlx::query(
            "UPDATE crawl_post_retries
            SET status = $3,
                reason = COALESCE($4, reason),
                completed_at = now(),
                updated_at = now()
            WHERE id = $1 AND attempts = $2 AND status = 'running'",
        )
        .bind(id)
        .bind(attempts)
        .bind(status)
        .bind(reason)
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }

    pub async fn reschedule_crawl_post_retry(
        &self,
        id: i64,
        attempts: i32,
        reason: &str,
        delay: Duration,
    ) -> SauceResult<()> {
        let delay_secs = delay.as_secs().max(1) as i64;
        sqlx::query(
            "UPDATE crawl_post_retries
            SET status = 'pending',
                reason = $3,
                next_attempt_at = now() + ($4 * interval '1 second'),
                updated_at = now()
            WHERE id = $1 AND attempts = $2 AND status = 'running'",
        )
        .bind(id)
        .bind(attempts)
        .bind(reason)
        .bind(delay_secs)
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }
}
