use std::sync::Arc;
use std::time::Duration;

use futures::stream::{self, StreamExt};
use tokio::sync::Semaphore;
use tokio::time::sleep;
use tracing::warn;

use sauce_core::config::AppConfig;
use sauce_core::embedding::EmbeddingClient;
use sauce_db::postgres::{CrawlPostRetryLease, PostgresRepo};
use sauce_db::qdrant::QdrantRepo;

use crate::danbooru::DanbooruClient;
use crate::danbooru::DanbooruPost;
use crate::downloader::ImageDownloader;

use super::indexing::{
    index_danbooru_posts, IndexingBatchOutcome, IndexingLimiters, IndexingRunOptions,
    IndexingStatsTarget,
};

pub struct ContinuousOptions {
    pub batch_limit: u32,
    pub poll_interval: Duration,
    pub backfill_workers: usize,
    pub backfill_range_size: i64,
}

pub async fn run_danbooru_continuous(
    config: &AppConfig,
    client: &DanbooruClient,
    downloader: &ImageDownloader,
    embedding: &EmbeddingClient,
    postgres: &PostgresRepo,
    qdrant: &QdrantRepo,
    options: ContinuousOptions,
) -> anyhow::Result<()> {
    let batch_limit = options.batch_limit;
    let poll_interval = options.poll_interval;
    let backfill_workers = options.backfill_workers.max(1);
    let backfill_range_size = options.backfill_range_size.max(1);
    let limiter = Arc::new(Semaphore::new(config.crawl_concurrency.max(1)));
    let embedding_limiter = Arc::new(Semaphore::new(config.crawl_embedding_concurrency.max(1)));
    let database_limiter = Arc::new(Semaphore::new(config.crawl_database_concurrency.max(1)));
    let discovery_limiter = Arc::new(Semaphore::new(config.crawl_concurrency.clamp(1, 16)));
    let lease_limiter = Arc::new(Semaphore::new(config.crawl_concurrency.clamp(4, 64)));
    let active_backfill_limiter =
        Arc::new(Semaphore::new(config.crawl_backfill_active_ranges.max(1)));
    let deps = WorkerDeps {
        config: config.clone(),
        client: client.clone(),
        downloader: downloader.clone(),
        embedding: embedding.clone(),
        postgres: postgres.clone(),
        qdrant: qdrant.clone(),
        limiter,
        embedding_limiter,
        database_limiter,
        discovery_limiter,
        lease_limiter,
        active_backfill_limiter,
    };
    let source_site = "danbooru";
    let scope_key = normalized_scope_key(&config.index_tags);
    println!(
        "Danbooru 최신 post id 확인 중: latest_tags={}",
        config.danbooru_latest_tags
    );
    let latest_id =
        fetch_latest_post_id_until_available(client, &config.danbooru_latest_tags).await;
    let state = postgres
        .get_crawl_state(source_site, &scope_key)
        .await?
        .unwrap_or(sauce_db::postgres::CrawlState {
            high_watermark_id: Some(latest_id),
            backfill_before_id: latest_id.checked_add(1),
        });
    let high_watermark_id = state.high_watermark_id.unwrap_or(latest_id);
    let backfill_before_id = state
        .backfill_before_id
        .unwrap_or_else(|| latest_id.saturating_add(1));
    postgres
        .upsert_crawl_state(
            source_site,
            &scope_key,
            Some(high_watermark_id),
            Some(backfill_before_id),
        )
        .await?;
    let recovered = postgres
        .recover_running_crawl_ranges(source_site, &scope_key, "backfill")
        .await?;
    if recovered > 0 {
        println!("과거 백필 재시작 준비: 미완료 running 구간 {recovered}개 회수");
    }
    println!(
        "연속 인덱싱 시작: latest_id={latest_id}, high_watermark_id={high_watermark_id}, backfill_before_id={backfill_before_id}, backfill_workers={backfill_workers}, active_backfill_ranges={}, range_size={backfill_range_size}, batch_limit={batch_limit}",
        config.crawl_backfill_active_ranges
    );
    tracing::info!(
        "danbooru continuous parallel start high_watermark_id={} backfill_before_id={} batch_limit={} backfill_workers={} active_backfill_ranges={} backfill_range_size={}",
        high_watermark_id,
        backfill_before_id,
        batch_limit,
        backfill_workers,
        config.crawl_backfill_active_ranges,
        backfill_range_size
    );

    let catchup = run_catchup_worker(
        deps.clone(),
        WorkerState {
            source_site: source_site.to_owned(),
            scope_key: scope_key.clone(),
            cursor_id: high_watermark_id,
            batch_limit,
            poll_interval,
        },
    );
    let backfill_workers = (0..backfill_workers)
        .map(|index| {
            run_backfill_worker(
                deps.clone(),
                BackfillWorkerState {
                    source_site: source_site.to_owned(),
                    scope_key: scope_key.clone(),
                    worker_id: format!("backfill-{index}"),
                    batch_limit,
                    poll_interval,
                    range_size: backfill_range_size,
                },
            )
        })
        .collect::<Vec<_>>();

    let backfill = futures::future::try_join_all(backfill_workers);
    let retry = run_post_retry_worker(
        deps.clone(),
        RetryWorkerState {
            source_site: source_site.to_owned(),
            scope_key: scope_key.clone(),
            batch_limit,
            poll_interval,
        },
    );
    tokio::try_join!(catchup, backfill, retry)?;
    Ok(())
}

struct WorkerState {
    source_site: String,
    scope_key: String,
    cursor_id: i64,
    batch_limit: u32,
    poll_interval: Duration,
}

struct BackfillWorkerState {
    source_site: String,
    scope_key: String,
    worker_id: String,
    batch_limit: u32,
    poll_interval: Duration,
    range_size: i64,
}

struct RetryWorkerState {
    source_site: String,
    scope_key: String,
    batch_limit: u32,
    poll_interval: Duration,
}

#[derive(Clone)]
struct WorkerDeps {
    config: AppConfig,
    client: DanbooruClient,
    downloader: ImageDownloader,
    embedding: EmbeddingClient,
    postgres: PostgresRepo,
    qdrant: QdrantRepo,
    limiter: Arc<Semaphore>,
    embedding_limiter: Arc<Semaphore>,
    database_limiter: Arc<Semaphore>,
    discovery_limiter: Arc<Semaphore>,
    lease_limiter: Arc<Semaphore>,
    active_backfill_limiter: Arc<Semaphore>,
}

struct BackfillRangeContext<'a> {
    config: &'a AppConfig,
    client: &'a DanbooruClient,
    downloader: &'a ImageDownloader,
    embedding: &'a EmbeddingClient,
    postgres: &'a PostgresRepo,
    qdrant: &'a QdrantRepo,
    batch_limit: u32,
    limiter: Arc<Semaphore>,
    embedding_limiter: Arc<Semaphore>,
    database_limiter: Arc<Semaphore>,
    discovery_limiter: Arc<Semaphore>,
}

struct FetchPostsRequest<'a> {
    client: &'a DanbooruClient,
    tags: &'a str,
    limit: u32,
    after_id: Option<i64>,
    before_id: Option<i64>,
    label: &'a str,
    initial_delay: Duration,
    discovery_limiter: Arc<Semaphore>,
}

async fn run_catchup_worker(deps: WorkerDeps, mut state: WorkerState) -> anyhow::Result<()> {
    loop {
        let latest_result = {
            let _permit = deps
                .discovery_limiter
                .clone()
                .acquire_owned()
                .await
                .map_err(|_| anyhow::anyhow!("discovery limiter closed"))?;
            deps.client
                .fetch_latest_post_id(&deps.config.danbooru_latest_tags)
                .await
        };
        let latest_now = match latest_result {
            Ok(Some(latest_id)) => {
                println!(
                    "최신 확인 완료: latest_id={}, 현재 high_watermark_id={}",
                    latest_id, state.cursor_id
                );
                latest_id
            }
            Ok(None) => {
                println!("최신 확인 대기: Danbooru 결과 없음");
                warn!("danbooru latest id query returned no posts");
                sleep(state.poll_interval).await;
                continue;
            }
            Err(error) => {
                println!("최신 확인 실패: {error}");
                warn!("danbooru latest id query failed: {error}");
                sleep(state.poll_interval).await;
                continue;
            }
        };

        if latest_now > state.cursor_id {
            println!(
                "최신 catch-up 시작: {} 초과부터 {} 이하까지 확인",
                state.cursor_id, latest_now
            );
            let mut catchup_before_id = latest_now.checked_add(1);
            let mut catchup_failed = false;
            loop {
                let catchup_posts = fetch_posts_between_until_available(FetchPostsRequest {
                    client: &deps.client,
                    tags: &deps.config.index_tags,
                    limit: state.batch_limit,
                    after_id: Some(state.cursor_id),
                    before_id: catchup_before_id,
                    label: "catchup",
                    initial_delay: state.poll_interval,
                    discovery_limiter: deps.discovery_limiter.clone(),
                })
                .await;
                if catchup_posts.is_empty() {
                    println!("최신 catch-up 대기: 새 포스트 없음");
                    break;
                }
                let next_before_id = catchup_posts
                    .iter()
                    .map(|post| post.id)
                    .min()
                    .unwrap_or(state.cursor_id);
                let outcome = match index_danbooru_posts(
                    &deps.config,
                    &deps.downloader,
                    &deps.embedding,
                    &deps.postgres,
                    &deps.qdrant,
                    catchup_posts,
                    IndexingRunOptions {
                        limiters: IndexingLimiters::with_shared(
                            Some(deps.limiter.clone()),
                            deps.embedding_limiter.clone(),
                            deps.database_limiter.clone(),
                        ),
                        stats_target: Some(IndexingStatsTarget {
                            source_site: state.source_site.clone(),
                            scope_key: state.scope_key.clone(),
                            direction: "catchup".to_owned(),
                        }),
                    },
                )
                .await
                {
                    Ok(outcome) => outcome,
                    Err(error) => {
                        println!("최신 catch-up 배치 실패: {error}");
                        warn!("danbooru catchup batch failed: {error}");
                        catchup_failed = true;
                        break;
                    }
                };
                if outcome.has_failures() {
                    println!(
                        "최신 catch-up 실패 post 재시도 큐 등록: {}개",
                        outcome.failed
                    );
                    if let Err(error) = enqueue_indexing_failures(
                        &deps.postgres,
                        &state.source_site,
                        &state.scope_key,
                        "catchup",
                        &outcome,
                    )
                    .await
                    {
                        println!("최신 catch-up 실패 post 재시도 큐 등록 실패: {error}");
                        warn!("danbooru catchup failure enqueue failed: {error}");
                    }
                }
                catchup_before_id = Some(next_before_id);
            }
            if catchup_failed {
                println!("최신 high watermark 유지: catch-up 배치 실패로 다음 루프에서 재시도");
            } else {
                state.cursor_id = latest_now;
                if let Err(error) = deps
                    .postgres
                    .update_crawl_high_watermark(
                        &state.source_site,
                        &state.scope_key,
                        state.cursor_id,
                    )
                    .await
                {
                    println!("최신 high watermark 저장 실패: {error}");
                    warn!("danbooru high watermark update failed: {error}");
                }
            }
        } else {
            println!(
                "최신 catch-up 대기: 새 포스트 없음, {}초 후 재확인",
                state.poll_interval.as_secs()
            );
        }

        sleep(state.poll_interval).await;
    }
}

async fn run_backfill_worker(deps: WorkerDeps, state: BackfillWorkerState) -> anyhow::Result<()> {
    loop {
        let _active_range_permit = deps
            .active_backfill_limiter
            .clone()
            .acquire_owned()
            .await
            .map_err(|_| anyhow::anyhow!("active backfill limiter closed"))?;
        let lease = {
            let _permit = deps
                .lease_limiter
                .clone()
                .acquire_owned()
                .await
                .map_err(|_| anyhow::anyhow!("lease limiter closed"))?;
            deps.postgres
                .allocate_backfill_range(
                    &state.source_site,
                    &state.scope_key,
                    state.range_size,
                    &state.worker_id,
                )
                .await
        };
        let lease = match lease {
            Ok(lease) => lease,
            Err(error) => {
                println!(
                    "과거 백필 구간 할당 실패: worker={}, {error}, {}초 후 재시도",
                    state.worker_id,
                    backfill_idle_sleep(deps.config.crawl_backfill_idle, &state.worker_id)
                        .as_secs()
                );
                warn!("danbooru backfill lease allocation failed: {error}");
                drop(_active_range_permit);
                sleep(backfill_idle_sleep(
                    deps.config.crawl_backfill_idle,
                    &state.worker_id,
                ))
                .await;
                continue;
            }
        };
        if let Some(lease) = lease {
            println!(
                "과거 백필 구간 할당: worker={}, id {}..{}",
                state.worker_id, lease.lower_id, lease.upper_id
            );
            tracing::info!(
                "danbooru backfill lease worker={} lower_id={} upper_id={}",
                state.worker_id,
                lease.lower_id,
                lease.upper_id
            );
            let status = process_backfill_range(
                &BackfillRangeContext {
                    config: &deps.config,
                    client: &deps.client,
                    downloader: &deps.downloader,
                    embedding: &deps.embedding,
                    postgres: &deps.postgres,
                    qdrant: &deps.qdrant,
                    batch_limit: state.batch_limit,
                    limiter: deps.limiter.clone(),
                    embedding_limiter: deps.embedding_limiter.clone(),
                    database_limiter: deps.database_limiter.clone(),
                    discovery_limiter: deps.discovery_limiter.clone(),
                },
                lease.lower_id,
                lease.upper_id,
            )
            .await;
            match status {
                Ok(status) => {
                    if let Err(error) = deps
                        .postgres
                        .finish_crawl_range(lease.id, lease.attempts, status)
                        .await
                    {
                        println!(
                            "과거 백필 구간 완료 기록 실패: worker={}, id {}..{}, {error}",
                            state.worker_id, lease.lower_id, lease.upper_id
                        );
                        warn!("danbooru backfill range finish failed: {error}");
                    }
                }
                Err(err) => {
                    let _ = deps
                        .postgres
                        .finish_crawl_range(lease.id, lease.attempts, "failed")
                        .await;
                    println!(
                        "과거 백필 구간 처리 실패: worker={}, id {}..{}, {err}",
                        state.worker_id, lease.lower_id, lease.upper_id
                    );
                    warn!("danbooru backfill range processing failed: {err}");
                }
            }
        } else {
            if is_primary_backfill_worker(&state.worker_id) {
                println!(
                    "과거 백필 대기: 할당 가능한 구간 없음, {}초 후 재확인",
                    backfill_idle_sleep(deps.config.crawl_backfill_idle, &state.worker_id)
                        .as_secs()
                );
                tracing::info!("danbooru backfill has no assignable range");
            }
            drop(_active_range_permit);
            sleep(backfill_idle_sleep(
                deps.config.crawl_backfill_idle,
                &state.worker_id,
            ))
            .await;
            continue;
        }

        drop(_active_range_permit);
        sleep(state.poll_interval).await;
    }
}

async fn process_backfill_range(
    context: &BackfillRangeContext<'_>,
    lower_id: i64,
    upper_id: i64,
) -> anyhow::Result<&'static str> {
    let mut cursor_before_id = Some(upper_id);
    let mut processed = false;
    let mut had_failures = false;

    loop {
        let posts = fetch_posts_between_until_available(FetchPostsRequest {
            client: context.client,
            tags: &context.config.index_tags,
            limit: context.batch_limit,
            after_id: Some(lower_id),
            before_id: cursor_before_id,
            label: "backfill",
            initial_delay: context.config.crawl_delay.max(Duration::from_secs(1)),
            discovery_limiter: context.discovery_limiter.clone(),
        })
        .await;
        if posts.is_empty() {
            println!("과거 백필 구간 비어 있음: id {lower_id}..{upper_id}");
            break;
        }
        println!(
            "과거 백필 목록 수집 완료: id {lower_id}..{upper_id}, {}개",
            posts.len()
        );
        let next_before_id = posts.iter().map(|post| post.id).min();
        let outcome = match index_danbooru_posts(
            context.config,
            context.downloader,
            context.embedding,
            context.postgres,
            context.qdrant,
            posts,
            IndexingRunOptions {
                limiters: IndexingLimiters::with_shared(
                    Some(context.limiter.clone()),
                    context.embedding_limiter.clone(),
                    context.database_limiter.clone(),
                ),
                stats_target: Some(IndexingStatsTarget {
                    source_site: "danbooru".to_owned(),
                    scope_key: normalized_scope_key(&context.config.index_tags),
                    direction: "backfill".to_owned(),
                }),
            },
        )
        .await
        {
            Ok(outcome) => outcome,
            Err(error) => {
                println!("과거 백필 배치 실패: id {lower_id}..{upper_id}, {error}");
                warn!("danbooru backfill batch failed: {error}");
                return Ok("failed");
            }
        };
        if outcome.has_failures() {
            had_failures = true;
            println!(
                "과거 백필 실패 post 재시도 큐 등록: id {lower_id}..{upper_id}, 실패 {}개",
                outcome.failed
            );
            if let Err(error) = enqueue_indexing_failures(
                context.postgres,
                "danbooru",
                &normalized_scope_key(&context.config.index_tags),
                "backfill",
                &outcome,
            )
            .await
            {
                println!(
                    "과거 백필 실패 post 재시도 큐 등록 실패: id {lower_id}..{upper_id}, {error}"
                );
                warn!("danbooru backfill failure enqueue failed: {error}");
            }
        }
        processed = true;
        match next_before_id {
            Some(next_before_id) if next_before_id > lower_id + 1 => {
                cursor_before_id = Some(next_before_id);
            }
            _ => break,
        }
    }

    Ok(if !processed {
        "empty"
    } else if had_failures {
        "completed_with_retries"
    } else {
        "completed"
    })
}

async fn run_post_retry_worker(deps: WorkerDeps, state: RetryWorkerState) -> anyhow::Result<()> {
    loop {
        let leases = match deps
            .postgres
            .lease_crawl_post_retries(
                &state.source_site,
                &state.scope_key,
                i64::from(state.batch_limit.clamp(1, 100)),
            )
            .await
        {
            Ok(leases) => leases,
            Err(error) => {
                println!("실패 post 재시도 lease 조회 실패: {error}");
                warn!("danbooru retry lease query failed: {error}");
                sleep(state.poll_interval.max(Duration::from_secs(30))).await;
                continue;
            }
        };

        if leases.is_empty() {
            sleep(state.poll_interval.max(Duration::from_secs(30))).await;
            continue;
        }

        println!("실패 post 재시도 시작: {}개", leases.len());
        let concurrency = deps.config.crawl_concurrency.clamp(1, 16);
        let tasks = stream::iter(leases.into_iter().map(|lease| {
            let deps = deps.clone();
            let source_site = state.source_site.clone();
            let scope_key = state.scope_key.clone();
            async move { process_post_retry_lease(deps, source_site, scope_key, lease).await }
        }))
        .buffer_unordered(concurrency);

        futures::pin_mut!(tasks);
        while let Some(result) = tasks.next().await {
            if let Err(error) = result {
                println!("실패 post 재시도 처리 실패: {error}");
                warn!("danbooru retry processing failed: {error}");
            }
        }

        sleep(state.poll_interval).await;
    }
}

async fn process_post_retry_lease(
    deps: WorkerDeps,
    source_site: String,
    scope_key: String,
    lease: CrawlPostRetryLease,
) -> anyhow::Result<()> {
    let Ok(post_id) = lease.source_post_id.parse::<i64>() else {
        deps.postgres
            .finish_crawl_post_retry(
                lease.id,
                lease.attempts,
                "skipped",
                Some("invalid source_post_id"),
            )
            .await?;
        return Ok(());
    };

    let post = match deps
        .client
        .fetch_post_by_id(&deps.config.index_tags, post_id)
        .await
    {
        Ok(Some(post)) => post,
        Ok(None) => {
            deps.postgres
                .finish_crawl_post_retry(
                    lease.id,
                    lease.attempts,
                    "skipped",
                    Some("post not found or outside scope"),
                )
                .await?;
            return Ok(());
        }
        Err(error) => {
            deps.postgres
                .reschedule_crawl_post_retry(
                    lease.id,
                    lease.attempts,
                    &error.to_string(),
                    retry_delay_for_attempt(lease.attempts),
                )
                .await?;
            return Ok(());
        }
    };

    if let Some(reason) = retry_unindexable_reason(&post) {
        deps.postgres
            .finish_crawl_post_retry(lease.id, lease.attempts, "skipped", Some(reason))
            .await?;
        return Ok(());
    }

    let outcome = index_danbooru_posts(
        &deps.config,
        &deps.downloader,
        &deps.embedding,
        &deps.postgres,
        &deps.qdrant,
        vec![post],
        IndexingRunOptions {
            limiters: IndexingLimiters::with_shared(
                Some(deps.limiter.clone()),
                deps.embedding_limiter.clone(),
                deps.database_limiter.clone(),
            ),
            stats_target: Some(IndexingStatsTarget {
                source_site: source_site.clone(),
                scope_key: scope_key.clone(),
                direction: "retry".to_owned(),
            }),
        },
    )
    .await?;

    if outcome.has_failures() {
        let reason = outcome
            .failures
            .first()
            .map(|failure| failure.reason.as_str())
            .unwrap_or("retry failed");
        deps.postgres
            .reschedule_crawl_post_retry(
                lease.id,
                lease.attempts,
                reason,
                retry_delay_for_attempt(lease.attempts),
            )
            .await?;
    } else {
        deps.postgres
            .finish_crawl_post_retry(lease.id, lease.attempts, "succeeded", None)
            .await?;
    }

    Ok(())
}

async fn enqueue_indexing_failures(
    postgres: &PostgresRepo,
    source_site: &str,
    scope_key: &str,
    direction: &str,
    outcome: &IndexingBatchOutcome,
) -> anyhow::Result<()> {
    postgres
        .enqueue_crawl_post_retries(
            source_site,
            scope_key,
            direction,
            &outcome.failures,
            Duration::from_secs(60),
        )
        .await?;
    Ok(())
}

fn retry_unindexable_reason(post: &DanbooruPost) -> Option<&'static str> {
    if post.primary_file_url().is_none() {
        return Some("missing file_url");
    }

    if post.supported_image_url().is_none() {
        return Some("unsupported image url");
    }

    None
}

fn retry_delay_for_attempt(attempts: i32) -> Duration {
    let exponent = attempts.saturating_sub(1).min(5) as u32;
    Duration::from_secs(60 * 2u64.saturating_pow(exponent))
}

fn normalized_scope_key(tags: &str) -> String {
    tags.split_whitespace().collect::<Vec<_>>().join(" ")
}

async fn fetch_latest_post_id_until_available(client: &DanbooruClient, latest_tags: &str) -> i64 {
    let mut delay = Duration::from_secs(5);
    loop {
        match client.fetch_latest_post_id(latest_tags).await {
            Ok(Some(latest_id)) => {
                println!("Danbooru 최신 post id 확인 완료: {latest_id}");
                return latest_id;
            }
            Ok(None) => {
                println!(
                    "Danbooru 최신 post id 확인 결과 없음, {}초 후 재시도",
                    delay.as_secs()
                );
                warn!("danbooru latest id query returned no posts");
            }
            Err(error) => {
                println!(
                    "Danbooru 최신 post id 확인 실패: {error}, {}초 후 재시도",
                    delay.as_secs()
                );
                warn!("danbooru latest id query failed: {error}");
            }
        }
        sleep(delay).await;
        delay = next_retry_delay(delay);
    }
}

async fn fetch_posts_between_until_available(request: FetchPostsRequest<'_>) -> Vec<DanbooruPost> {
    let mut delay = request.initial_delay.max(Duration::from_secs(1));
    loop {
        println!(
            "Danbooru 목록 조회 시작: {}, limit={}, after_id={:?}, before_id={:?}",
            request.label, request.limit, request.after_id, request.before_id
        );
        let response = match request.discovery_limiter.clone().acquire_owned().await {
            Ok(_permit) => {
                request
                    .client
                    .fetch_posts_between(
                        request.tags,
                        request.limit,
                        request.after_id,
                        request.before_id,
                    )
                    .await
            }
            Err(_) => {
                warn!("danbooru {} discovery limiter closed", request.label);
                sleep(delay).await;
                delay = next_retry_delay(delay);
                continue;
            }
        };
        match response {
            Ok(posts) => {
                println!(
                    "Danbooru 목록 조회 완료: {}, {}개",
                    request.label,
                    posts.len()
                );
                return posts;
            }
            Err(error) => {
                println!(
                    "Danbooru 목록 조회 실패: {}, {error}, {}초 후 재시도",
                    request.label,
                    delay.as_secs()
                );
                warn!("danbooru {} page query failed: {error}", request.label);
                sleep(delay).await;
                delay = next_retry_delay(delay);
            }
        }
    }
}

fn next_retry_delay(delay: Duration) -> Duration {
    delay.saturating_mul(2).min(Duration::from_secs(300))
}

fn is_primary_backfill_worker(worker_id: &str) -> bool {
    worker_id == "backfill-0"
}

fn backfill_idle_sleep(idle_interval: Duration, worker_id: &str) -> Duration {
    let base = idle_interval.max(Duration::from_secs(1));
    let jitter = backfill_worker_index(worker_id)
        .map(|index| Duration::from_secs((index % 60) as u64))
        .unwrap_or_default();
    base.saturating_add(jitter)
}

fn backfill_worker_index(worker_id: &str) -> Option<usize> {
    worker_id.strip_prefix("backfill-")?.parse().ok()
}
