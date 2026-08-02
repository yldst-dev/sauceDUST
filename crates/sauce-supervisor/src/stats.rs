use std::path::{Path, PathBuf};

use sauce_core::config::AppConfig;
use sauce_db::postgres::PostgresRepo;
use sauce_db::qdrant::QdrantRepo;

pub async fn run() -> anyhow::Result<()> {
    let stats = snapshot().await?;

    println!("saucedust storage stats");
    println!();

    match &stats.database {
        Some(database) => {
            println!(
                "다운로드 완료 이미지 수: {}",
                format_count(database.image_count)
            );
            match database.vector_count {
                Some(vector_count) => {
                    println!("Qdrant 벡터 point 수: {}", format_count(vector_count));
                }
                None => println!("Qdrant 벡터 point 수: 확인 실패"),
            }
            println!(
                "PostgreSQL 논리 DB 용량: {}",
                format_bytes(database.postgres_database_size)
            );
            if let Some(crawl) = &database.crawl {
                println!();
                println!("최신 이미지 저장 수: {}", format_count(crawl.catchup_saved));
                println!(
                    "과거 이미지 저장 수: {}",
                    format_count(crawl.backfill_saved)
                );
                println!(
                    "Danbooru high watermark id: {}",
                    crawl
                        .high_watermark_id
                        .map(format_count)
                        .unwrap_or_else(|| "-".to_owned())
                );
                println!(
                    "Danbooru backfill before id: {}",
                    crawl
                        .backfill_before_id
                        .map(format_count)
                        .unwrap_or_else(|| "-".to_owned())
                );
                println!(
                    "백필 구간 상태: running {}개, failed {}개, completed {}개, empty {}개",
                    format_count(crawl.running_ranges),
                    format_count(crawl.failed_ranges),
                    format_count(crawl.completed_ranges),
                    format_count(crawl.empty_ranges)
                );
            }
        }
        None => {
            println!("인덱싱된 이미지 수: 확인 실패");
            println!("Qdrant 벡터 point 수: 확인 실패");
            println!("PostgreSQL 논리 DB 용량: 확인 실패");
            if let Some(error) = &stats.database_error {
                println!("DB 연결 오류: {error}");
            }
        }
    }

    println!();
    println!(
        "PostgreSQL 데이터 디렉터리: {}",
        stats.postgres_data_dir.display()
    );
    println!(
        "PostgreSQL 디스크 사용량: {}",
        format_bytes(stats.postgres_disk_size)
    );
    println!(
        "Qdrant 저장소 디렉터리: {}",
        stats.qdrant_storage_dir.display()
    );
    println!(
        "Qdrant 디스크 사용량: {}",
        format_bytes(stats.qdrant_disk_size)
    );
    println!(
        "DB 디스크 사용량 합계: {}",
        format_bytes(stats.total_disk_size())
    );

    Ok(())
}

pub(crate) struct StorageSnapshot {
    pub database: Option<DatabaseStats>,
    pub database_error: Option<String>,
    pub postgres_data_dir: PathBuf,
    pub qdrant_storage_dir: PathBuf,
    pub postgres_disk_size: u64,
    pub qdrant_disk_size: u64,
}

impl StorageSnapshot {
    pub fn total_disk_size(&self) -> u64 {
        self.postgres_disk_size
            .saturating_add(self.qdrant_disk_size)
    }
}

pub(crate) struct DatabaseStats {
    pub image_count: i64,
    pub vector_count: Option<u64>,
    pub postgres_database_size: u64,
    pub crawl: Option<CrawlStats>,
}

pub(crate) struct CrawlStats {
    pub high_watermark_id: Option<i64>,
    pub backfill_before_id: Option<i64>,
    pub catchup_saved: i64,
    pub backfill_saved: i64,
    pub running_ranges: i64,
    pub failed_ranges: i64,
    pub completed_ranges: i64,
    pub empty_ranges: i64,
}

pub(crate) async fn snapshot() -> anyhow::Result<StorageSnapshot> {
    let config = AppConfig::from_env()?;
    let postgres_data_dir = crate::paths::native_postgres_data_dir()?;
    let qdrant_storage_dir = crate::paths::native_qdrant_storage_dir()?;
    let database_result = load_database_stats(&config).await;
    let (database, database_error) = match database_result {
        Ok(database) => (Some(database), None),
        Err(error) => (None, Some(format!("{error:#}"))),
    };
    let postgres_disk_size = directory_size(&postgres_data_dir).await?;
    let qdrant_disk_size = directory_size(&qdrant_storage_dir).await?;

    Ok(StorageSnapshot {
        database,
        database_error,
        postgres_data_dir,
        qdrant_storage_dir,
        postgres_disk_size,
        qdrant_disk_size,
    })
}

async fn load_database_stats(config: &AppConfig) -> anyhow::Result<DatabaseStats> {
    let postgres = PostgresRepo::connect(&config.database_url).await?;
    postgres.apply_migrations().await?;
    let qdrant = QdrantRepo::new(&config.qdrant_url, "images", config.vector_size)?;
    let image_count = postgres.image_count().await?;
    let postgres_database_size = postgres.database_size_bytes().await?.max(0) as u64;
    let vector_count = qdrant.count().await.ok();
    let crawl = load_crawl_stats(&postgres, &config.index_tags)
        .await
        .ok()
        .flatten();

    Ok(DatabaseStats {
        image_count,
        vector_count,
        postgres_database_size,
        crawl,
    })
}

async fn load_crawl_stats(
    postgres: &PostgresRepo,
    index_tags: &str,
) -> anyhow::Result<Option<CrawlStats>> {
    let scope_key = index_tags.split_whitespace().collect::<Vec<_>>().join(" ");
    let counts = postgres
        .crawl_direction_counts("danbooru", &scope_key)
        .await
        .unwrap_or_default();
    Ok(postgres
        .crawl_progress_stats("danbooru", &scope_key)
        .await?
        .map(|stats| CrawlStats {
            high_watermark_id: stats.high_watermark_id,
            backfill_before_id: stats.backfill_before_id,
            catchup_saved: counts.catchup_saved,
            backfill_saved: counts.backfill_saved,
            running_ranges: stats.running_ranges,
            failed_ranges: stats.failed_ranges,
            completed_ranges: stats.completed_ranges,
            empty_ranges: stats.empty_ranges,
        }))
}

async fn directory_size(path: &Path) -> anyhow::Result<u64> {
    let path = path.to_path_buf();
    tokio::task::spawn_blocking(move || directory_size_blocking(&path)).await?
}

fn directory_size_blocking(path: &Path) -> anyhow::Result<u64> {
    if !path.exists() {
        return Ok(0);
    }

    let mut total = 0u64;
    let mut stack = vec![PathBuf::from(path)];

    while let Some(path) = stack.pop() {
        let metadata = match std::fs::symlink_metadata(&path) {
            Ok(metadata) => metadata,
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => continue,
            Err(error) => return Err(error.into()),
        };

        if metadata.is_file() {
            total = total.saturating_add(metadata.len());
        } else if metadata.is_dir() {
            for entry in std::fs::read_dir(&path)? {
                stack.push(entry?.path());
            }
        }
    }

    Ok(total)
}

pub(crate) fn format_count(value: impl Into<i128>) -> String {
    let raw = value.into().to_string();
    let mut out = String::new();
    for (index, ch) in raw.chars().rev().enumerate() {
        if index > 0 && index % 3 == 0 {
            out.push(',');
        }
        out.push(ch);
    }
    out.chars().rev().collect()
}

pub(crate) fn format_bytes(bytes: u64) -> String {
    const UNITS: [&str; 6] = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];
    let mut value = bytes as f64;
    let mut unit = 0usize;
    while value >= 1024.0 && unit < UNITS.len() - 1 {
        value /= 1024.0;
        unit += 1;
    }

    if unit == 0 {
        format!("{bytes} {}", UNITS[unit])
    } else {
        format!("{value:.2} {} ({bytes} bytes)", UNITS[unit])
    }
}

#[cfg(test)]
mod tests {
    use super::{format_bytes, format_count};

    #[test]
    fn formats_counts() {
        assert_eq!(format_count(0i64), "0");
        assert_eq!(format_count(1234i64), "1,234");
        assert_eq!(format_count(1234567u64), "1,234,567");
    }

    #[test]
    fn formats_bytes() {
        assert_eq!(format_bytes(0), "0 B");
        assert_eq!(format_bytes(512), "512 B");
        assert_eq!(format_bytes(1024), "1.00 KiB (1024 bytes)");
    }
}
