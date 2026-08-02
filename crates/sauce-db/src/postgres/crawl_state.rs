use sauce_core::errors::{SauceError, SauceResult};
use sqlx::Row;

use super::{CrawlProgressStats, CrawlRangeLease, CrawlState, PostgresRepo};

impl PostgresRepo {
    pub async fn get_crawl_state(
        &self,
        source_site: &str,
        scope_key: &str,
    ) -> SauceResult<Option<CrawlState>> {
        let row = sqlx::query(
            "SELECT high_watermark_id, backfill_before_id FROM crawl_states
            WHERE source_site = $1 AND scope_key = $2",
        )
        .bind(source_site)
        .bind(scope_key)
        .fetch_optional(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        row.map(|row| {
            Ok(CrawlState {
                high_watermark_id: row
                    .try_get("high_watermark_id")
                    .map_err(|err| SauceError::Database(err.to_string()))?,
                backfill_before_id: row
                    .try_get("backfill_before_id")
                    .map_err(|err| SauceError::Database(err.to_string()))?,
            })
        })
        .transpose()
    }

    pub async fn upsert_crawl_state(
        &self,
        source_site: &str,
        scope_key: &str,
        high_watermark_id: Option<i64>,
        backfill_before_id: Option<i64>,
    ) -> SauceResult<()> {
        sqlx::query(
            "INSERT INTO crawl_states (source_site, scope_key, high_watermark_id, backfill_before_id)
            VALUES ($1, $2, $3, $4)
            ON CONFLICT (source_site, scope_key) DO UPDATE SET
                high_watermark_id = EXCLUDED.high_watermark_id,
                backfill_before_id = EXCLUDED.backfill_before_id,
                updated_at = now()",
        )
        .bind(source_site)
        .bind(scope_key)
        .bind(high_watermark_id)
        .bind(backfill_before_id)
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }

    pub async fn update_crawl_high_watermark(
        &self,
        source_site: &str,
        scope_key: &str,
        high_watermark_id: i64,
    ) -> SauceResult<()> {
        sqlx::query(
            "UPDATE crawl_states
            SET high_watermark_id = GREATEST(COALESCE(high_watermark_id, $3), $3),
                updated_at = now()
            WHERE source_site = $1 AND scope_key = $2",
        )
        .bind(source_site)
        .bind(scope_key)
        .bind(high_watermark_id)
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }

    pub async fn update_crawl_backfill_before(
        &self,
        source_site: &str,
        scope_key: &str,
        backfill_before_id: i64,
    ) -> SauceResult<()> {
        sqlx::query(
            "UPDATE crawl_states
            SET backfill_before_id = $3, updated_at = now()
            WHERE source_site = $1 AND scope_key = $2",
        )
        .bind(source_site)
        .bind(scope_key)
        .bind(backfill_before_id)
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }

    pub async fn allocate_backfill_range(
        &self,
        source_site: &str,
        scope_key: &str,
        range_size: i64,
        worker_id: &str,
    ) -> SauceResult<Option<CrawlRangeLease>> {
        let range_size = range_size.max(1);
        let mut tx = self
            .pool
            .begin()
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;

        let retry_row = sqlx::query(
            "UPDATE crawl_ranges
            SET status = 'running',
                worker_id = $3,
                attempts = attempts + 1,
                leased_at = now(),
                completed_at = NULL
            WHERE id = (
                SELECT id FROM crawl_ranges
                WHERE source_site = $1
                    AND scope_key = $2
                    AND direction = 'backfill'
                    AND (
                        status = 'failed'
                        OR (status = 'running' AND leased_at < now() - interval '30 minutes')
                    )
                ORDER BY
                    CASE WHEN status = 'failed' THEN 0 ELSE 1 END,
                    attempts ASC,
                    lower_id DESC
                LIMIT 1
                FOR UPDATE SKIP LOCKED
            )
            RETURNING id, lower_id, upper_id, attempts",
        )
        .bind(source_site)
        .bind(scope_key)
        .bind(worker_id)
        .fetch_optional(&mut *tx)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        if let Some(row) = retry_row {
            tx.commit()
                .await
                .map_err(|err| SauceError::Database(err.to_string()))?;
            return Ok(Some(CrawlRangeLease {
                id: row
                    .try_get("id")
                    .map_err(|err| SauceError::Database(err.to_string()))?,
                lower_id: row
                    .try_get("lower_id")
                    .map_err(|err| SauceError::Database(err.to_string()))?,
                upper_id: row
                    .try_get("upper_id")
                    .map_err(|err| SauceError::Database(err.to_string()))?,
                attempts: row
                    .try_get("attempts")
                    .map_err(|err| SauceError::Database(err.to_string()))?,
            }));
        }

        let row = sqlx::query(
            "SELECT backfill_before_id FROM crawl_states
            WHERE source_site = $1 AND scope_key = $2
            FOR UPDATE",
        )
        .bind(source_site)
        .bind(scope_key)
        .fetch_optional(&mut *tx)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        let Some(row) = row else {
            tx.commit()
                .await
                .map_err(|err| SauceError::Database(err.to_string()))?;
            return Ok(None);
        };
        let Some(upper_id) = row
            .try_get::<Option<i64>, _>("backfill_before_id")
            .map_err(|err| SauceError::Database(err.to_string()))?
        else {
            tx.commit()
                .await
                .map_err(|err| SauceError::Database(err.to_string()))?;
            return Ok(None);
        };
        if upper_id <= 1 {
            tx.commit()
                .await
                .map_err(|err| SauceError::Database(err.to_string()))?;
            return Ok(None);
        }

        let lower_id = upper_id.saturating_sub(range_size + 1).max(0);
        let next_before_id = lower_id + 1;
        sqlx::query(
            "UPDATE crawl_states
            SET backfill_before_id = $3, updated_at = now()
            WHERE source_site = $1 AND scope_key = $2",
        )
        .bind(source_site)
        .bind(scope_key)
        .bind(next_before_id)
        .execute(&mut *tx)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        let lease_row = sqlx::query(
            "INSERT INTO crawl_ranges (
                source_site, scope_key, direction, lower_id, upper_id, status, worker_id, attempts, leased_at
            ) VALUES ($1, $2, 'backfill', $3, $4, 'running', $5, 1, now())
            ON CONFLICT (source_site, scope_key, direction, lower_id, upper_id)
            DO UPDATE SET
                status = 'running',
                worker_id = EXCLUDED.worker_id,
                attempts = crawl_ranges.attempts + 1,
                leased_at = now(),
                completed_at = NULL
            RETURNING id, attempts",
        )
        .bind(source_site)
        .bind(scope_key)
        .bind(lower_id)
        .bind(upper_id)
        .bind(worker_id)
        .fetch_one(&mut *tx)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        tx.commit()
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(Some(CrawlRangeLease {
            id: lease_row
                .try_get("id")
                .map_err(|err| SauceError::Database(err.to_string()))?,
            lower_id,
            upper_id,
            attempts: lease_row
                .try_get("attempts")
                .map_err(|err| SauceError::Database(err.to_string()))?,
        }))
    }

    pub async fn finish_crawl_range(
        &self,
        lease_id: i64,
        attempts: i32,
        status: &str,
    ) -> SauceResult<()> {
        sqlx::query(
            "UPDATE crawl_ranges
            SET status = $3, completed_at = now()
            WHERE id = $1 AND attempts = $2 AND status = 'running'",
        )
        .bind(lease_id)
        .bind(attempts)
        .bind(status)
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }

    pub async fn recover_running_crawl_ranges(
        &self,
        source_site: &str,
        scope_key: &str,
        direction: &str,
    ) -> SauceResult<i64> {
        let row = sqlx::query(
            "WITH recovered AS (
                UPDATE crawl_ranges
                SET status = 'failed',
                    completed_at = now()
                WHERE source_site = $1
                    AND scope_key = $2
                    AND direction = $3
                    AND status = 'running'
                RETURNING id
            )
            SELECT COUNT(*) AS count FROM recovered",
        )
        .bind(source_site)
        .bind(scope_key)
        .bind(direction)
        .fetch_one(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        row.try_get("count")
            .map_err(|err| SauceError::Database(err.to_string()))
    }

    pub async fn crawl_progress_stats(
        &self,
        source_site: &str,
        scope_key: &str,
    ) -> SauceResult<Option<CrawlProgressStats>> {
        let state = sqlx::query(
            "SELECT high_watermark_id, backfill_before_id
            FROM crawl_states
            WHERE source_site = $1 AND scope_key = $2",
        )
        .bind(source_site)
        .bind(scope_key)
        .fetch_optional(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        let Some(state) = state else {
            return Ok(None);
        };

        let ranges = sqlx::query(
            "SELECT
                COALESCE(SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END), 0) AS running,
                COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) AS failed,
                COALESCE(SUM(CASE WHEN status IN ('completed', 'completed_with_retries') THEN 1 ELSE 0 END), 0) AS completed,
                COALESCE(SUM(CASE WHEN status = 'empty' THEN 1 ELSE 0 END), 0) AS empty
            FROM crawl_ranges
            WHERE source_site = $1 AND scope_key = $2",
        )
        .bind(source_site)
        .bind(scope_key)
        .fetch_one(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(Some(CrawlProgressStats {
            high_watermark_id: state
                .try_get("high_watermark_id")
                .map_err(|err| SauceError::Database(err.to_string()))?,
            backfill_before_id: state
                .try_get("backfill_before_id")
                .map_err(|err| SauceError::Database(err.to_string()))?,
            running_ranges: ranges
                .try_get("running")
                .map_err(|err| SauceError::Database(err.to_string()))?,
            failed_ranges: ranges
                .try_get("failed")
                .map_err(|err| SauceError::Database(err.to_string()))?,
            completed_ranges: ranges
                .try_get("completed")
                .map_err(|err| SauceError::Database(err.to_string()))?,
            empty_ranges: ranges
                .try_get("empty")
                .map_err(|err| SauceError::Database(err.to_string()))?,
        }))
    }
}
