use std::env;
use std::time::Duration;

use crate::cli::{Cli, Commands, IndexCommand, NormalizeCommand, ServiceCommand, VpnCommand};
use crate::process::ManagedChild;

pub async fn run(cli: Cli) -> anyhow::Result<()> {
    if cli.daemon {
        return start_daemon(cli.command).await;
    }

    let root = crate::paths::workspace_root()?;
    let sync_report = crate::env_config::sync_existing(&root).await?;
    if sync_report.changed() && !allows_env_review_after_sync(&cli.command) {
        print_env_review_required(sync_report);
        return Ok(());
    }
    dotenvy::from_path_override(root.join(".env")).ok();
    if requires_runtime_outbound(&cli.command) {
        crate::vpn::ensure_outbound_ready_for_runtime()?;
    }

    match cli.command {
        None => crate::auto::run().await,
        Some(Commands::Bootstrap { skip_python }) => crate::bootstrap::run(skip_python).await,
        Some(Commands::Menu) => crate::menu::run().await,
        Some(Commands::Start {
            no_indexer,
            no_telegram,
            index_limit,
            poll_secs,
            backfill_workers,
            backfill_range_size,
        }) => {
            crate::service::start_background(serve_args(
                no_indexer,
                no_telegram,
                index_limit,
                poll_secs,
                backfill_workers,
                backfill_range_size,
            ))
            .await
        }
        Some(Commands::Stop) => crate::service::stop_background().await,
        Some(Commands::Status) => crate::service::status_background().await,
        Some(Commands::Service { command }) => run_service(command).await,
        Some(Commands::Serve {
            no_indexer,
            no_telegram,
            index_limit,
            poll_secs,
            backfill_workers,
            backfill_range_size,
        }) => {
            serve(
                no_indexer,
                no_telegram,
                index_limit,
                poll_secs,
                backfill_workers,
                backfill_range_size,
            )
            .await
        }
        Some(Commands::DbUp) => crate::db::start_postgres().await,
        Some(Commands::DbDown) => crate::db::down().await,
        Some(Commands::Env { command }) => crate::env_config::run(command).await,
        Some(Commands::Index { command }) => run_index(command).await,
        Some(Commands::Normalize { command }) => run_normalize(command).await,
        Some(Commands::Vpn { command }) => run_vpn(command).await,
        Some(Commands::Verify) => run_verify().await,
        Some(Commands::Stats) => crate::stats::run().await,
        Some(Commands::ResetDev) => run_reset_dev().await,
        Some(Commands::Doctor) => crate::doctor::run().await,
        Some(Commands::InternalApi) => sauce_api::run().await,
        Some(Commands::InternalTelegram) => sauce_telegram::run().await,
        Some(Commands::InternalIndexer { command }) => run_index_internal(command).await,
    }
}

async fn start_daemon(command: Option<Commands>) -> anyhow::Result<()> {
    if command.is_some() {
        anyhow::bail!("-d는 단독으로 사용하십시오. 예: ./saucedust -d");
    }

    let root = crate::paths::workspace_root()?;
    let sync_report = crate::env_config::sync_existing(&root).await?;
    if sync_report.changed() {
        print_env_review_required(sync_report);
        return Ok(());
    }
    if !root.join(".env").exists() {
        crate::ui::warn(".env 파일이 아직 없어 백그라운드 시작을 중단합니다.");
        crate::ui::info("먼저 환경값을 설정한 뒤 다시 실행하십시오.");
        crate::ui::command("./run_saucedust.sh env setup");
        return Ok(());
    }
    dotenvy::from_path_override(root.join(".env")).ok();
    crate::vpn::ensure_outbound_ready_for_runtime()?;

    crate::service::start_background(serve_args(
        false,
        false,
        env_u32("INDEX_LIMIT", 200),
        env_u64("SAUCEDUST_POLL_SECS", 15),
        env_usize("CRAWL_BACKFILL_WORKERS"),
        env_i64("CRAWL_BACKFILL_RANGE_SIZE"),
    ))
    .await
}

pub(crate) async fn serve(
    no_indexer: bool,
    no_telegram: bool,
    index_limit: u32,
    poll_secs: u64,
    backfill_workers: Option<usize>,
    backfill_range_size: Option<i64>,
) -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    crate::runtime_assets::ensure(&root).await?;
    let sync_report = crate::env_config::sync_existing(&root).await?;
    if sync_report.changed() {
        print_env_review_required(sync_report);
        return Ok(());
    }
    dotenvy::from_path_override(root.join(".env")).ok();
    crate::vpn::ensure_outbound_ready_for_runtime()?;
    let mut children = Vec::new();
    crate::vpn::autostart_if_enabled().await?;
    crate::ui::info("PostgreSQL/Qdrant 시작 중");
    children.push(crate::db::up().await?);
    crate::ui::info("Qdrant 준비 대기 중");
    crate::wait::http("http://localhost:6333/readyz", Duration::from_secs(60)).await?;
    crate::ui::ok("Qdrant 준비 완료");

    crate::ui::info("embedding-worker 시작 중");
    children.push(ManagedChild::spawn(
        "embedding-worker",
        crate::python::embedding_worker_command(&root)?,
    )?);
    crate::ui::info("embedding-worker 모델 로딩 및 health 확인 중");
    crate::wait::http("http://localhost:8100/health", Duration::from_secs(120)).await?;
    crate::ui::ok("embedding-worker 준비 완료");

    crate::ui::info("검색 API 시작 중");
    children.push(ManagedChild::spawn(
        "sauce-api",
        crate::process::cargo_binary_command("sauce-api")?,
    )?);
    crate::ui::info("검색 API health 확인 중");
    crate::wait::http("http://localhost:8000/health", Duration::from_secs(60)).await?;
    crate::ui::ok("검색 API 준비 완료");

    if !no_telegram && telegram_token_is_configured() {
        crate::ui::info("Telegram bot 시작 중");
        children.push(ManagedChild::spawn(
            "sauce-telegram",
            crate::process::cargo_binary_command("sauce-telegram")?,
        )?);
    }

    if !no_indexer {
        crate::ui::info("Danbooru 인덱서 시작 중");
        let mut command = crate::process::cargo_binary_command("sauce-indexer")?;
        command.args([
            "danbooru",
            "--continuous",
            "--limit",
            &index_limit.to_string(),
            "--poll-secs",
            &poll_secs.max(1).to_string(),
        ]);
        if let Some(backfill_workers) = backfill_workers {
            command
                .arg("--backfill-workers")
                .arg(backfill_workers.max(1).to_string());
        }
        if let Some(backfill_range_size) = backfill_range_size {
            command
                .arg("--backfill-range-size")
                .arg(backfill_range_size.max(1).to_string());
        }
        children.push(ManagedChild::spawn("sauce-indexer", command)?);
    } else {
        crate::ui::warn("인덱서가 비활성화되어 다운로드/인덱싱 로그가 출력되지 않습니다.");
    }

    println!("saucedust running");
    crate::process::monitor(children).await
}

async fn run_index(command: IndexCommand) -> anyhow::Result<()> {
    match command {
        IndexCommand::Danbooru {
            limit,
            dry_run,
            continuous,
            poll_secs,
            backfill_workers,
            backfill_range_size,
        } => {
            let mut args = vec!["danbooru".to_owned()];
            if let Some(limit) = limit {
                args.push("--limit".to_owned());
                args.push(limit.to_string());
            }
            if dry_run {
                args.push("--dry-run".to_owned());
            }
            if continuous {
                args.push("--continuous".to_owned());
                args.push("--poll-secs".to_owned());
                args.push(poll_secs.max(1).to_string());
            }
            if let Some(backfill_workers) = backfill_workers {
                args.push("--backfill-workers".to_owned());
                args.push(backfill_workers.max(1).to_string());
            }
            if let Some(backfill_range_size) = backfill_range_size {
                args.push("--backfill-range-size".to_owned());
                args.push(backfill_range_size.max(1).to_string());
            }
            if dry_run {
                return run_indexer_args(&args).await;
            }
            run_indexer_with_services(&args, true, continuous).await
        }
        IndexCommand::CheckPost {
            source_site,
            post_id,
        } => {
            sauce_indexer::runner::check_post(sauce_indexer::runner::CheckPostOptions {
                source_site,
                post_id,
            })
            .await
        }
    }
}

async fn run_index_internal(command: IndexCommand) -> anyhow::Result<()> {
    match command {
        IndexCommand::Danbooru {
            limit,
            dry_run,
            continuous,
            poll_secs,
            backfill_workers,
            backfill_range_size,
        } => {
            sauce_indexer::runner::run_danbooru(sauce_indexer::runner::DanbooruOptions {
                limit,
                dry_run,
                continuous,
                poll_secs,
                backfill_workers,
                backfill_range_size,
            })
            .await
        }
        IndexCommand::CheckPost {
            source_site,
            post_id,
        } => {
            sauce_indexer::runner::check_post(sauce_indexer::runner::CheckPostOptions {
                source_site,
                post_id,
            })
            .await
        }
    }
}

async fn run_service(command: ServiceCommand) -> anyhow::Result<()> {
    match command {
        ServiceCommand::Install {
            no_indexer,
            no_telegram,
            index_limit,
            poll_secs,
            backfill_workers,
            backfill_range_size,
        } => {
            crate::service::install_launchd(serve_args(
                no_indexer,
                no_telegram,
                index_limit,
                poll_secs,
                backfill_workers,
                backfill_range_size,
            ))
            .await
        }
        ServiceCommand::Start => crate::service::start_launchd().await,
        ServiceCommand::Stop => crate::service::stop_launchd().await,
        ServiceCommand::Status => crate::service::status_launchd().await,
        ServiceCommand::Uninstall => crate::service::uninstall_launchd().await,
    }
}

async fn run_verify() -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    crate::runtime_assets::ensure(&root).await?;
    let mut children = Vec::new();
    children.push(crate::db::up().await?);
    crate::wait::http("http://localhost:6333/readyz", Duration::from_secs(60)).await?;
    children.push(ManagedChild::spawn(
        "embedding-worker",
        crate::python::embedding_worker_command(&root)?,
    )?);
    crate::wait::http("http://localhost:8100/health", Duration::from_secs(120)).await?;
    let result = sauce_indexer::runner::verify().await;
    crate::process::stop_children(&mut children).await;
    result
}

async fn run_reset_dev() -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    crate::runtime_assets::ensure(&root).await?;
    let mut children = Vec::new();
    children.push(crate::db::up().await?);
    crate::wait::http("http://localhost:6333/readyz", Duration::from_secs(60)).await?;
    let result = sauce_indexer::runner::reset_dev().await;
    crate::process::stop_children(&mut children).await;
    result
}

async fn run_indexer_with_services(
    args: &[String],
    start_embedding: bool,
    long_running: bool,
) -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    crate::runtime_assets::ensure(&root).await?;
    crate::vpn::autostart_if_enabled().await?;
    let mut children = Vec::new();
    children.push(crate::db::up().await?);
    crate::wait::http("http://localhost:6333/readyz", Duration::from_secs(60)).await?;

    if start_embedding {
        children.push(ManagedChild::spawn(
            "embedding-worker",
            crate::python::embedding_worker_command(&root)?,
        )?);
        crate::wait::http("http://localhost:8100/health", Duration::from_secs(120)).await?;
    }

    let mut command = crate::process::cargo_binary_command("sauce-indexer")?;
    command.args(args);
    if long_running {
        children.push(ManagedChild::spawn("sauce-indexer", command)?);
        crate::process::monitor(children).await
    } else {
        let result = crate::command::run(&mut command, "sauce-indexer").await;
        crate::process::stop_children(&mut children).await;
        result
    }
}

async fn run_indexer_args(args: &[String]) -> anyhow::Result<()> {
    crate::vpn::autostart_if_enabled().await?;
    let mut command = crate::process::cargo_binary_command("sauce-indexer")?;
    command.args(args);
    crate::command::run(&mut command, "sauce-indexer").await
}

async fn run_normalize(command: NormalizeCommand) -> anyhow::Result<()> {
    match command {
        NormalizeCommand::Danbooru { tags, limit, out } => {
            crate::vpn::autostart_if_enabled().await?;
            sauce_indexer::runner::normalize_danbooru(sauce_indexer::runner::NormalizeOptions {
                tags,
                limit: limit.min(200),
                out: out.into(),
            })
            .await
        }
    }
}

async fn run_vpn(command: VpnCommand) -> anyhow::Result<()> {
    match command {
        VpnCommand::Setup => crate::vpn::setup().await,
        VpnCommand::Start => crate::vpn::start().await,
        VpnCommand::Rotate => crate::vpn::rotate().await,
        VpnCommand::Stop => crate::vpn::stop().await,
        VpnCommand::Status => crate::vpn::status().await,
        VpnCommand::Test => crate::vpn::test().await,
    }
}

fn serve_args(
    no_indexer: bool,
    no_telegram: bool,
    index_limit: u32,
    poll_secs: u64,
    backfill_workers: Option<usize>,
    backfill_range_size: Option<i64>,
) -> Vec<String> {
    let mut args = Vec::new();
    if no_indexer {
        args.push("--no-indexer".to_owned());
    }
    if no_telegram {
        args.push("--no-telegram".to_owned());
    }
    args.push("--index-limit".to_owned());
    args.push(index_limit.to_string());
    args.push("--poll-secs".to_owned());
    args.push(poll_secs.max(1).to_string());
    if let Some(backfill_workers) = backfill_workers {
        args.push("--backfill-workers".to_owned());
        args.push(backfill_workers.max(1).to_string());
    }
    if let Some(backfill_range_size) = backfill_range_size {
        args.push("--backfill-range-size".to_owned());
        args.push(backfill_range_size.max(1).to_string());
    }
    args
}

fn telegram_token_is_configured() -> bool {
    env::var("TELEGRAM_BOT_TOKEN")
        .ok()
        .is_some_and(|value| !value.trim().is_empty())
}

fn env_u32(key: &str, default: u32) -> u32 {
    env::var(key)
        .ok()
        .and_then(|value| value.parse().ok())
        .unwrap_or(default)
}

fn env_u64(key: &str, default: u64) -> u64 {
    env::var(key)
        .ok()
        .and_then(|value| value.parse().ok())
        .unwrap_or(default)
}

fn env_usize(key: &str) -> Option<usize> {
    env::var(key).ok().and_then(|value| value.parse().ok())
}

fn env_i64(key: &str) -> Option<i64> {
    env::var(key).ok().and_then(|value| value.parse().ok())
}

fn allows_env_review_after_sync(command: &Option<Commands>) -> bool {
    matches!(
        command,
        Some(Commands::Env { .. })
            | Some(Commands::Stop)
            | Some(Commands::Status)
            | Some(Commands::Stats)
            | Some(Commands::DbDown)
            | Some(Commands::Doctor)
            | Some(Commands::Service {
                command: ServiceCommand::Stop | ServiceCommand::Status | ServiceCommand::Uninstall
            })
            | Some(Commands::Vpn {
                command: VpnCommand::Rotate | VpnCommand::Stop | VpnCommand::Status
            })
    )
}

fn requires_runtime_outbound(command: &Option<Commands>) -> bool {
    matches!(
        command,
        None | Some(Commands::Start { .. })
            | Some(Commands::Serve { .. })
            | Some(Commands::Index {
                command: IndexCommand::Danbooru { .. }
            })
            | Some(Commands::Normalize {
                command: NormalizeCommand::Danbooru { .. }
            })
            | Some(Commands::InternalIndexer {
                command: IndexCommand::Danbooru { .. }
            })
            | Some(Commands::Service {
                command: ServiceCommand::Install { .. } | ServiceCommand::Start
            })
    )
}

fn print_env_review_required(report: crate::env_config::EnvSyncReport) {
    crate::ui::warn(".env가 현재 saucedust 버전에 맞게 자동 업데이트되었습니다.");
    crate::ui::info(&format!(
        "추가된 값 {}개, 제거된 값 {}개가 있습니다.",
        report.added, report.removed
    ));
    crate::ui::info("값을 점검할 수 있도록 이번 실행은 여기서 중단합니다.");
    crate::ui::command("./run_saucedust.sh env show --show-secrets");
    crate::ui::command("./run_saucedust.sh env setup --advanced");
    crate::ui::info("필요한 값을 확인한 뒤 다시 실행하십시오.");
}
