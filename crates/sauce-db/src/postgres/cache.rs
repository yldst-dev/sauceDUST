use sauce_core::errors::{SauceError, SauceResult};
use sqlx::Row;

use super::PostgresRepo;

impl PostgresRepo {
    pub async fn get_cached_query_embedding(&self, sha256: &str) -> SauceResult<Option<Vec<f32>>> {
        let row = sqlx::query("SELECT vector FROM query_embedding_cache WHERE sha256 = $1")
            .bind(sha256)
            .fetch_optional(&self.pool)
            .await
            .map_err(|err| SauceError::Database(err.to_string()))?;

        let Some(row) = row else {
            return Ok(None);
        };

        sqlx::query(
            "UPDATE query_embedding_cache SET hits = hits + 1, last_used_at = now() WHERE sha256 = $1",
        )
        .bind(sha256)
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        row.try_get("vector")
            .map(Some)
            .map_err(|err| SauceError::Database(err.to_string()))
    }

    pub async fn upsert_cached_query_embedding(
        &self,
        sha256: &str,
        vector: &[f32],
    ) -> SauceResult<()> {
        sqlx::query(
            "INSERT INTO query_embedding_cache (sha256, vector, vector_size)
            VALUES ($1, $2, $3)
            ON CONFLICT (sha256) DO UPDATE SET
                vector = EXCLUDED.vector,
                vector_size = EXCLUDED.vector_size,
                last_used_at = now()",
        )
        .bind(sha256)
        .bind(vector)
        .bind(vector.len() as i32)
        .execute(&self.pool)
        .await
        .map_err(|err| SauceError::Database(err.to_string()))?;

        Ok(())
    }
}
