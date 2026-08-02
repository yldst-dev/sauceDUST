use sauce_core::errors::{SauceError, SauceResult};
use sqlx::Row;

use super::PostgresRepo;

#[derive(Debug, Clone, Default)]
pub struct CrawlDirectionCounts {
    pub catchup_saved: i64,
    pub backfill_saved: i64,
}

impl PostgresRepo {
    pub async fn increment_crawl_direction_saved(
        &self,
        source_site: &str,
        scope_key: &str,
        direction: &str,
        saved_count: i64,
    ) -> SauceResult<()> {
        if saved_count <= 0 {
            return Ok(());
        }

        sqlx::query(
            "INSERT INTO crawl_direction_stats (
                source_site, scope_key, direction, saved_count
            ) VALUES ($1, $2, $3, $4)
            ON CONFLICT (source_site, scope_key, direction) DO UPDATE SET
                saved_count = crawl_direction_stats.saved_count + EXCLUDED.saved_count,
                updated_at = now()",
        )
        .bind(source_site)
        .bind(scope_key)
        .bind(direction)
        .bind(saved_count)
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }

    pub async fn crawl_direction_counts(
        &self,
        source_site: &str,
        scope_key: &str,
    ) -> SauceResult<CrawlDirectionCounts> {
        let rows = sqlx::query(
            "SELECT direction, saved_count
            FROM crawl_direction_stats
            WHERE source_site = $1 AND scope_key = $2",
        )
        .bind(source_site)
        .bind(scope_key)
        .fetch_all(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        let mut counts = CrawlDirectionCounts::default();
        for row in rows {
            let direction = row
                .try_get::<String, _>("direction")
                .map_err(|err| SauceError::Database(err.to_string()))?;
            let saved_count = row
                .try_get::<i64, _>("saved_count")
                .map_err(|err| SauceError::Database(err.to_string()))?;
            match direction.as_str() {
                "catchup" => counts.catchup_saved = saved_count,
                "backfill" => counts.backfill_saved = saved_count,
                _ => {}
            }
        }

        Ok(counts)
    }
}
