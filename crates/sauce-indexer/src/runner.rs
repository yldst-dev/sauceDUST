use std::path::PathBuf;
use std::time::Duration;

use sauce_core::config::AppConfig;
use sauce_core::embedding::EmbeddingClient;
use sauce_db::postgres::PostgresRepo;
use sauce_db::qdrant::QdrantRepo;
use tokio::fs;

use crate::danbooru::DanbooruClient;
use crate::downloader::ImageDownloader;
use crate::normalizer::normalize_danbooru_posts;
use crate::pipeline;

pub struct DanbooruOptions {
    pub limit: Option<u32>,
    pub dry_run: bool,
    pub continuous: bool,
    pub poll_secs: u64,
    pub backfill_workers: Option<usize>,
    pub backfill_range_size: Option<i64>,
}

pub struct NormalizeOptions {
    pub tags: String,
    pub limit: u32,
    pub out: PathBuf,
}

pub struct CheckPostOptions {
    pub source_site: String,
    pub post_id: String,
}

pub async fn run_danbooru(options: DanbooruOptions) -> anyhow::Result<()> {
    let config = AppConfig::from_env()?;
    println!(
        "Danbooru 인덱서 설정: limit={}, continuous={}, pipeline={}, embedding={}, database={}, tags={}",
        options.limit.unwrap_or(config.index_limit),
        options.continuous,
        config.crawl_concurrency,
        config.crawl_embedding_concurrency,
        config.crawl_database_concurrency,
        compact_text(&config.index_tags, 120)
    );
    let client = DanbooruClient::new(
        config.danbooru_connect_base_url.clone(),
        config.danbooru_host_header.clone(),
        config.danbooru_user_agent.clone(),
        config.request_timeout,
    )?;
    let limit = options.limit.unwrap_or(config.index_limit);
    if options.dry_run {
        let posts = client.fetch_posts(&config.index_tags, limit).await?;
        for post in posts {
            println!(
                "{} {} {} {:?}",
                post.id,
                post.rating.clone().unwrap_or_default(),
                post.score.unwrap_or_default(),
                post.file_url
            );
        }
        return Ok(());
    }
    let postgres = PostgresRepo::connect(&config.database_url).await?;
    postgres.apply_migrations().await?;
    let qdrant = QdrantRepo::new(&config.qdrant_url, "images", config.vector_size)?;
    qdrant.ensure_collection().await?;
    let embedding = EmbeddingClient::new(&config.embedding_worker_url, config.request_timeout)?;
    let downloader =
        ImageDownloader::new(config.danbooru_user_agent.clone(), config.request_timeout)?;
    if options.continuous {
        pipeline::run_danbooru_continuous(
            &config,
            &client,
            &downloader,
            &embedding,
            &postgres,
            &qdrant,
            pipeline::ContinuousOptions {
                batch_limit: limit.clamp(1, 200),
                poll_interval: Duration::from_secs(options.poll_secs.max(1)),
                backfill_workers: options
                    .backfill_workers
                    .unwrap_or(config.crawl_backfill_workers)
                    .max(1),
                backfill_range_size: options
                    .backfill_range_size
                    .unwrap_or(config.crawl_backfill_range_size)
                    .max(1),
            },
        )
        .await?;
    } else {
        pipeline::run_danbooru_pipeline(
            &config,
            &client,
            &downloader,
            &embedding,
            &postgres,
            &qdrant,
            limit,
        )
        .await?;
    }
    Ok(())
}

pub async fn verify() -> anyhow::Result<()> {
    let config = AppConfig::from_env()?;
    let postgres = PostgresRepo::connect(&config.database_url).await?;
    postgres.apply_migrations().await?;
    let qdrant = QdrantRepo::new(&config.qdrant_url, "images", config.vector_size)?;
    qdrant.ensure_collection().await?;
    let embedding = EmbeddingClient::new(&config.embedding_worker_url, config.request_timeout)?;
    let image_count = postgres.image_count().await?;
    let vector_count = qdrant.count().await.unwrap_or_default();
    println!("postgres_images={image_count}");
    println!("qdrant_points={vector_count}");
    println!("embedding_worker={}", embedding.health().await);
    Ok(())
}

pub async fn check_post(options: CheckPostOptions) -> anyhow::Result<()> {
    let config = AppConfig::from_env()?;
    let postgres = PostgresRepo::connect(&config.database_url).await?;
    postgres.apply_migrations().await?;
    match postgres
        .get_image_by_source_post_id(&options.source_site, &options.post_id)
        .await?
    {
        Some(image) => {
            println!("indexed=true");
            println!("image_id={}", image.id);
            println!("source_site={}", image.source_site);
            println!("source_post_id={}", image.source_post_id);
            println!(
                "canonical_url={}",
                image.canonical_url.unwrap_or_else(|| "-".to_owned())
            );
            println!("rating={}", image.rating.unwrap_or_else(|| "-".to_owned()));
            println!("tags={}", compact_text(&image.tags.join(" "), 160));
            println!(
                "artist_tags={}",
                compact_text(&image.artist_tags.join(" "), 160)
            );
        }
        None => {
            println!("indexed=false");
            println!("source_site={}", options.source_site);
            println!("source_post_id={}", options.post_id);
        }
    }
    Ok(())
}

fn compact_text(text: &str, max_chars: usize) -> String {
    if text.chars().count() <= max_chars {
        return text.to_owned();
    }
    let mut out = text
        .chars()
        .take(max_chars.saturating_sub(1))
        .collect::<String>();
    out.push('…');
    out
}

pub async fn reset_dev() -> anyhow::Result<()> {
    let confirm = std::env::var("CONFIRM_RESET").unwrap_or_default();
    if confirm != "1" {
        anyhow::bail!("CONFIRM_RESET=1 is required");
    }
    let config = AppConfig::from_env()?;
    let postgres = PostgresRepo::connect(&config.database_url).await?;
    postgres.apply_migrations().await?;
    let qdrant = QdrantRepo::new(&config.qdrant_url, "images", config.vector_size)?;
    qdrant.ensure_collection().await?;
    postgres.clear_dev().await?;
    qdrant.clear_collection().await?;
    println!("reset complete");
    Ok(())
}

pub async fn normalize_danbooru(options: NormalizeOptions) -> anyhow::Result<()> {
    let config = AppConfig::from_env()?;
    let client = DanbooruClient::new(
        &config.danbooru_connect_base_url,
        config.danbooru_host_header.clone(),
        &config.danbooru_user_agent,
        config.request_timeout,
    )?;
    let posts = client
        .fetch_posts(&options.tags, options.limit.min(200))
        .await?;
    let records = normalize_danbooru_posts(&posts, &config.danbooru_base_url);
    let json = serde_json::to_string_pretty(&records)?;

    if let Some(parent) = options.out.parent() {
        if !parent.as_os_str().is_empty() {
            fs::create_dir_all(parent).await?;
        }
    }

    fs::write(&options.out, json).await?;
    println!(
        "wrote {} records to {}",
        records.len(),
        options.out.display()
    );
    Ok(())
}
