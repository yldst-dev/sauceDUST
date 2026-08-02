use futures::stream::{self, StreamExt};
use std::collections::BTreeMap;
use std::env;
use std::sync::Arc;
use std::time::Duration;

use tokio::sync::{OwnedSemaphorePermit, Semaphore};
use tokio::time::{sleep, timeout};

use sauce_core::config::AppConfig;
use sauce_core::embedding::EmbeddingClient;
use sauce_core::hashing::calculate_hashes_and_embedding_image;
use sauce_db::postgres::{CrawlPostRetryInput, PostgresRepo};
use sauce_db::qdrant::QdrantRepo;

use crate::danbooru::{is_supported_image_url, DanbooruClient, DanbooruPost};
use crate::downloader::ImageDownloader;
use crate::storage::danbooru_metadata;

#[derive(Clone)]
pub(super) struct IndexingLimiters {
    pipeline: Option<Arc<Semaphore>>,
    embedding: Arc<Semaphore>,
    database: Arc<Semaphore>,
}

#[derive(Clone)]
pub(super) struct IndexingStatsTarget {
    pub source_site: String,
    pub scope_key: String,
    pub direction: String,
}

#[derive(Clone)]
pub(super) struct IndexingRunOptions {
    pub limiters: IndexingLimiters,
    pub stats_target: Option<IndexingStatsTarget>,
}

pub(super) struct IndexingBatchOutcome {
    pub failed: i32,
    pub failures: Vec<CrawlPostRetryInput>,
}

impl IndexingBatchOutcome {
    pub(super) fn has_failures(&self) -> bool {
        !self.failures.is_empty()
    }
}

impl IndexingLimiters {
    pub(super) fn new(config: &AppConfig, pipeline: Option<Arc<Semaphore>>) -> Self {
        Self {
            pipeline,
            embedding: Arc::new(Semaphore::new(config.crawl_embedding_concurrency.max(1))),
            database: Arc::new(Semaphore::new(config.crawl_database_concurrency.max(1))),
        }
    }

    pub(super) fn with_shared(
        pipeline: Option<Arc<Semaphore>>,
        embedding: Arc<Semaphore>,
        database: Arc<Semaphore>,
    ) -> Self {
        Self {
            pipeline,
            embedding,
            database,
        }
    }
}

pub async fn run_danbooru_pipeline(
    config: &AppConfig,
    client: &DanbooruClient,
    downloader: &ImageDownloader,
    embedding: &EmbeddingClient,
    postgres: &PostgresRepo,
    qdrant: &QdrantRepo,
    limit: u32,
) -> anyhow::Result<()> {
    let posts = client.fetch_posts(&config.index_tags, limit).await?;
    index_danbooru_posts(
        config,
        downloader,
        embedding,
        postgres,
        qdrant,
        posts,
        IndexingRunOptions {
            limiters: IndexingLimiters::new(config, None),
            stats_target: None,
        },
    )
    .await
    .map(|_| ())
}

pub(super) async fn index_danbooru_posts(
    config: &AppConfig,
    downloader: &ImageDownloader,
    embedding: &EmbeddingClient,
    postgres: &PostgresRepo,
    qdrant: &QdrantRepo,
    posts: Vec<DanbooruPost>,
    options: IndexingRunOptions,
) -> anyhow::Result<IndexingBatchOutcome> {
    let limiters = options.limiters;
    let stats_target = options.stats_target;
    let mut unindexable_counts = BTreeMap::new();
    let mut indexable_posts = Vec::with_capacity(posts.len());
    for post in posts {
        if let Some(reason) = unindexable_reason(&post) {
            *unindexable_counts.entry(reason).or_insert(0usize) += 1;
        } else {
            indexable_posts.push(post);
        }
    }
    let unindexable = unindexable_counts.values().sum::<usize>();
    if unindexable > 0 {
        println!(
            "인덱싱 배치 사전 스킵: 처리 불가 {}개 ({})",
            unindexable,
            format_unindexable_counts(&unindexable_counts)
        );
    }
    let source_post_ids = indexable_posts
        .iter()
        .map(|post| post.id.to_string())
        .collect::<Vec<_>>();
    let batch_prep_timeout = batch_prep_timeout(config);
    let database_timeout = database_operation_timeout(config);
    let existing_images = {
        let _permit =
            acquire_database_permit(&limiters, database_timeout, "기존 메타데이터 확인").await?;
        timeout(
            batch_prep_timeout,
            postgres.existing_source_post_image_ids("danbooru", &source_post_ids),
        )
        .await
        .map_err(|_| anyhow::anyhow!("기존 메타데이터 확인 timeout"))??
    };
    let existing_image_ids = existing_images.values().copied().collect::<Vec<_>>();
    let existing_vectors = {
        let _permit =
            acquire_database_permit(&limiters, database_timeout, "기존 벡터 확인").await?;
        timeout(
            batch_prep_timeout,
            qdrant.existing_point_ids(&existing_image_ids),
        )
        .await
        .map_err(|_| anyhow::anyhow!("기존 벡터 확인 timeout"))??
    };
    let existing = existing_images
        .into_iter()
        .filter_map(|(source_post_id, image_id)| {
            existing_vectors
                .contains(&image_id)
                .then_some(source_post_id)
        })
        .collect::<std::collections::HashSet<_>>();
    let skipped = existing.len();
    let posts = indexable_posts
        .into_iter()
        .filter(|post| !existing.contains(&post.id.to_string()))
        .collect::<Vec<_>>();
    if posts.is_empty() {
        println!(
            "인덱싱: 새로 처리할 포스트 없음, 처리 불가 스킵 {unindexable}개, 기존 중복 {skipped}개 건너뜀"
        );
        return Ok(IndexingBatchOutcome {
            failed: 0,
            failures: Vec::new(),
        });
    }
    if limiters.pipeline.is_some() {
        println!(
            "인덱싱 배치 시작: 새 포스트 {}개, 기존 중복 {skipped}개 건너뜀, 전역 동시 처리 {}개, 임베딩 {}개, DB {}개",
            posts.len(),
            config.crawl_concurrency,
            config.crawl_embedding_concurrency,
            config.crawl_database_concurrency
        );
    } else {
        println!(
            "인덱싱 배치 시작: 새 포스트 {}개, 기존 중복 {skipped}개 건너뜀, 동시 처리 {}개, 임베딩 {}개, DB {}개",
            posts.len(),
            config.crawl_concurrency,
            config.crawl_embedding_concurrency,
            config.crawl_database_concurrency
        );
    }
    let job_id = {
        let _permit =
            acquire_database_permit(&limiters, database_timeout, "index job 생성").await?;
        timeout(
            database_timeout,
            postgres.create_index_job("danbooru", posts.len() as i32),
        )
        .await
        .map_err(|_| anyhow::anyhow!("index job 생성 timeout"))??
    };
    let mut succeeded = 0i32;
    let mut failed = 0i32;
    let mut failures = Vec::new();

    let tasks = stream::iter(posts.into_iter().map(|post| {
        let config = config.clone();
        let downloader = downloader.clone();
        let embedding = embedding.clone();
        let postgres = postgres.clone();
        let qdrant = qdrant.clone();
        let limiters = limiters.clone();

        async move {
            let _permit = if let Some(limiter) = limiters.pipeline.clone() {
                match limiter.acquire_owned().await {
                    Ok(permit) => Some(permit),
                    Err(_) => return (post, Err(anyhow::anyhow!("indexing limiter closed"))),
                }
            } else {
                None
            };
            sleep(config.crawl_delay).await;
            let result = index_post_with_retry(
                &config,
                &downloader,
                &embedding,
                &postgres,
                &qdrant,
                &post,
                &limiters,
            )
            .await;
            (post, result)
        }
    }))
    .buffer_unordered(config.crawl_concurrency);

    futures::pin_mut!(tasks);

    while let Some((post, result)) = tasks.next().await {
        match result {
            Ok(()) => {
                succeeded += 1;
                stage_log(config, &format!("인덱싱 완료: Danbooru post {}", post.id));
            }
            Err(err) => {
                failed += 1;
                println!("인덱싱 실패: Danbooru post {} - {}", post.id, err);
                let primary_file_url = post.primary_file_url();
                let url = primary_file_url.as_deref().or(post.source.as_deref());
                let reason = err.to_string();
                failures.push(CrawlPostRetryInput {
                    source_post_id: post.id.to_string(),
                    url: url.map(str::to_owned),
                    reason: reason.clone(),
                });
                if let Ok(_permit) =
                    acquire_database_permit(&limiters, database_timeout, "실패 로그 저장").await
                {
                    let _ = timeout(
                        database_timeout,
                        postgres.record_failure(
                            job_id,
                            "danbooru",
                            Some(&post.id.to_string()),
                            url,
                            &reason,
                        ),
                    )
                    .await;
                }
            }
        }
    }

    let status = if failed == 0 {
        "succeeded"
    } else {
        "completed"
    };
    match acquire_database_permit(&limiters, database_timeout, "index job 완료").await {
        Ok(_permit) => {
            if let Err(error) = timeout(
                database_timeout,
                postgres.finish_index_job(job_id, status, succeeded, failed),
            )
            .await
            .map_err(|_| anyhow::anyhow!("index job 완료 timeout"))
            .and_then(|result| result.map_err(Into::into))
            {
                println!("index job 완료 기록 실패: {error}");
            }
            if let Some(stats_target) = &stats_target {
                if let Err(error) = timeout(
                    database_timeout,
                    postgres.increment_crawl_direction_saved(
                        &stats_target.source_site,
                        &stats_target.scope_key,
                        &stats_target.direction,
                        i64::from(succeeded),
                    ),
                )
                .await
                .map_err(|_| anyhow::anyhow!("crawl stats 저장 timeout"))
                .and_then(|result| result.map_err(Into::into))
                {
                    println!("crawl stats 저장 실패: {error}");
                }
            }
        }
        Err(error) => {
            println!("index job 완료 기록 건너뜀: {error}");
        }
    }
    println!(
        "인덱싱 배치 완료: 성공 {succeeded}개, 실패 {failed}개, 처리 불가 스킵 {unindexable}개, 기존 중복 {skipped}개"
    );

    Ok(IndexingBatchOutcome { failed, failures })
}

async fn index_post_with_retry(
    config: &AppConfig,
    downloader: &ImageDownloader,
    embedding: &EmbeddingClient,
    postgres: &PostgresRepo,
    qdrant: &QdrantRepo,
    post: &DanbooruPost,
    limiters: &IndexingLimiters,
) -> anyhow::Result<()> {
    let mut delay = Duration::from_secs(2);
    let mut last_error = None;
    for attempt in 1..=3 {
        match index_post(
            config, downloader, embedding, postgres, qdrant, post, limiters,
        )
        .await
        {
            Ok(()) => return Ok(()),
            Err(error) if attempt < 3 && is_transient_index_error(&error) => {
                println!(
                    "인덱싱 재시도: Danbooru post {} - 일시 오류 {}회차: {}",
                    post.id, attempt, error
                );
                last_error = Some(error);
                sleep(delay).await;
                delay = delay.saturating_mul(2).min(Duration::from_secs(30));
            }
            Err(error) => return Err(error),
        }
    }
    Err(last_error.unwrap_or_else(|| anyhow::anyhow!("index retry failed")))
}

fn unindexable_reason(post: &DanbooruPost) -> Option<&'static str> {
    let Some(file_url) = post.primary_file_url() else {
        return Some("missing file_url");
    };

    if !is_supported_image_url(&file_url) {
        return Some("unsupported image url");
    }

    None
}

fn format_unindexable_counts(counts: &BTreeMap<&'static str, usize>) -> String {
    counts
        .iter()
        .map(|(reason, count)| format!("{reason} {count}개"))
        .collect::<Vec<_>>()
        .join(", ")
}

fn is_transient_index_error(error: &anyhow::Error) -> bool {
    let message = error.to_string();
    message.contains("pool timed out")
        || message.contains("error communicating with database")
        || message.contains("error sending request")
        || message.contains("error decoding response body")
        || message.contains("connection")
        || message.contains("timeout")
}

async fn index_post(
    config: &AppConfig,
    downloader: &ImageDownloader,
    embedding: &EmbeddingClient,
    postgres: &PostgresRepo,
    qdrant: &QdrantRepo,
    post: &DanbooruPost,
    limiters: &IndexingLimiters,
) -> anyhow::Result<()> {
    let supported_file_url = post
        .supported_image_url()
        .ok_or_else(|| anyhow::anyhow!("missing supported image url"))?;
    stage_log(config, &format!("다운로드 시작: Danbooru post {}", post.id));
    let bytes = downloader.download(&supported_file_url).await?;
    stage_log(
        config,
        &format!(
            "다운로드 완료: Danbooru post {} ({} bytes)",
            post.id,
            bytes.len()
        ),
    );
    stage_log(
        config,
        &format!("해시 계산 시작: Danbooru post {}", post.id),
    );
    let prepared = calculate_hashes_and_embedding_image(&bytes)?;
    let hashes = prepared.hashes;
    stage_log(
        config,
        &format!(
            "해시 계산 완료: Danbooru post {} ({}x{}, pHash, dHash 저장 준비, 임베딩 입력 {} bytes)",
            post.id,
            hashes.width,
            hashes.height,
            prepared.embedding_bytes.len()
        ),
    );
    stage_log(config, &format!("임베딩 시작: Danbooru post {}", post.id));
    let embedding_response = {
        let embedding_timeout = embedding_operation_timeout(config);
        let _permit = limiters
            .embedding
            .clone()
            .acquire_owned()
            .await
            .map_err(|_| anyhow::anyhow!("embedding limiter closed"))?;
        timeout(
            embedding_timeout,
            embedding.embed_image(
                prepared.embedding_bytes,
                &format!("danbooru-{}-embedding.jpg", post.id),
            ),
        )
        .await
        .map_err(|_| anyhow::anyhow!("embedding timeout"))??
    };
    stage_log(
        config,
        &format!(
            "임베딩 완료: Danbooru post {} ({}차원, {})",
            post.id, embedding_response.vector_size, embedding_response.device
        ),
    );
    stage_log(
        config,
        &format!("메타데이터 DB 저장 시작: Danbooru post {}", post.id),
    );
    let metadata = danbooru_metadata(
        post,
        &config.danbooru_base_url,
        hashes.phash,
        hashes.dhash,
        hashes.width,
        hashes.height,
    );
    let saved = {
        let database_timeout = database_operation_timeout(config);
        let _permit =
            acquire_database_permit(limiters, database_timeout, "메타데이터 DB 저장").await?;
        timeout(database_timeout, postgres.upsert_image(&metadata))
            .await
            .map_err(|_| anyhow::anyhow!("메타데이터 DB 저장 timeout"))??
    };
    stage_log(
        config,
        &format!(
            "메타데이터 DB 저장 완료: Danbooru post {} -> image_id {}",
            post.id, saved.id
        ),
    );
    stage_log(
        config,
        &format!("벡터 DB 저장 시작: Danbooru post {}", post.id),
    );
    {
        let database_timeout = database_operation_timeout(config);
        let _permit = acquire_database_permit(limiters, database_timeout, "벡터 DB 저장").await?;
        timeout(
            database_timeout,
            qdrant.upsert_image_vector(&saved, embedding_response.vector),
        )
        .await
        .map_err(|_| anyhow::anyhow!("벡터 DB 저장 timeout"))??;
        timeout(
            database_timeout,
            postgres.mark_image_vector_indexed(saved.id),
        )
        .await
        .map_err(|_| anyhow::anyhow!("벡터 인덱싱 표시 timeout"))??;
    }
    stage_log(
        config,
        &format!("벡터 DB 저장 완료: Danbooru post {}", post.id),
    );

    Ok(())
}

fn stage_log(config: &AppConfig, message: &str) {
    if config.verbose_index_logs {
        println!("{message}");
    }
}

async fn acquire_database_permit(
    limiters: &IndexingLimiters,
    _timeout_duration: Duration,
    _label: &str,
) -> anyhow::Result<OwnedSemaphorePermit> {
    limiters
        .database
        .clone()
        .acquire_owned()
        .await
        .map_err(|_| anyhow::anyhow!("database limiter closed"))
}

fn batch_prep_timeout(config: &AppConfig) -> Duration {
    env_duration_secs(
        "INDEX_BATCH_PREP_TIMEOUT_SECS",
        config
            .request_timeout
            .saturating_mul(4)
            .max(Duration::from_secs(120)),
    )
}

fn database_operation_timeout(config: &AppConfig) -> Duration {
    env_duration_secs(
        "INDEX_DATABASE_OP_TIMEOUT_SECS",
        config
            .request_timeout
            .saturating_mul(3)
            .max(Duration::from_secs(90)),
    )
}

fn embedding_operation_timeout(config: &AppConfig) -> Duration {
    env_duration_secs(
        "INDEX_EMBEDDING_OP_TIMEOUT_SECS",
        config
            .request_timeout
            .saturating_mul(4)
            .max(Duration::from_secs(120)),
    )
}

fn env_duration_secs(key: &str, default: Duration) -> Duration {
    env::var(key)
        .ok()
        .and_then(|value| value.trim().parse::<u64>().ok())
        .filter(|value| *value > 0)
        .map(Duration::from_secs)
        .unwrap_or(default)
}
