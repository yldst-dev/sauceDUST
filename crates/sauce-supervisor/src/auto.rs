use std::env;
use std::path::Path;

pub async fn run() -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    crate::ui::title("saucedust 자동 실행");

    crate::ui::step(1, 6, "런타임 파일 확인");
    crate::runtime_assets::ensure_env_example(&root).await?;
    crate::ui::ok("기본 런타임 파일 확인 완료");

    crate::ui::step(2, 6, "시스템 의존성 자동 점검");
    crate::system_setup::ensure_bootstrap_dependencies().await?;
    crate::ui::ok("시스템 의존성 준비 완료");

    crate::ui::step(3, 6, ".env 설정 확인");
    if !env_exists(&root) {
        print_env_required(&root);
        return Ok(());
    }
    dotenvy::from_path_override(root.join(".env")).ok();
    crate::ui::ok(".env 설정 로드 완료");

    crate::ui::step(4, 6, "DB와 Qdrant 준비");
    crate::db::bootstrap().await?;
    crate::ui::ok("DB와 Qdrant 준비 완료");

    crate::ui::step(5, 6, "Python embedding worker 준비");
    crate::python::ensure_worker(&root).await?;
    crate::ui::ok("Python embedding worker 준비 완료");

    crate::ui::step(6, 6, "프로젝트 실행");
    print_runtime_summary();
    crate::ui::flush();
    crate::app::serve(
        false,
        false,
        env_u32("INDEX_LIMIT", 200),
        env_u64("SAUCEDUST_POLL_SECS", 15),
        env_usize("CRAWL_BACKFILL_WORKERS"),
        env_i64("CRAWL_BACKFILL_RANGE_SIZE"),
    )
    .await
}

fn env_exists(root: &Path) -> bool {
    root.join(".env").exists()
}

fn print_env_required(root: &Path) {
    crate::ui::warn(".env 파일이 아직 없습니다.");
    crate::ui::info("시스템 의존성은 준비됐지만, 실행 설정이 없어 프로젝트를 시작하지 않습니다.");
    crate::ui::info(&format!("설정 파일 위치: {}", root.join(".env").display()));
    println!();
    crate::ui::info("대화형 설정을 실행하십시오.");
    crate::ui::command("./run_saucedust.sh env setup");
    println!();
    crate::ui::info("최소 설정 예시입니다.");
    crate::ui::command(
        "./run_saucedust.sh env set SAUCE_ENGINE_DATA_DIR /Users/you/Documents/sauceDUST/data",
    );
    crate::ui::command("./run_saucedust.sh env set TELEGRAM_BOT_TOKEN <token>");
    crate::ui::command("./run_saucedust.sh env set SAUCEDUST_VPN_AUTOSTART 1");
    crate::ui::command("./run_saucedust.sh env set EXPRESSVPN_OVPN_DIR <ovpn-directory>");
    crate::ui::command("./run_saucedust.sh env set EXPRESSVPN_OVPN_PATH <client.ovpn>");
    crate::ui::command("./run_saucedust.sh env set EXPRESSVPN_USERNAME <username>");
    crate::ui::command("./run_saucedust.sh env set EXPRESSVPN_PASSWORD <password>");
    println!();
    crate::ui::info("설정 후 다시 실행하면 DB, API, 크롤러, Telegram 봇이 자동으로 시작됩니다.");
    crate::ui::command("./run_saucedust.sh");
}

fn print_runtime_summary() {
    crate::ui::info("DB, Qdrant, embedding worker, API, Danbooru continuous crawler를 시작합니다.");
    if env::var("TELEGRAM_BOT_TOKEN")
        .ok()
        .is_some_and(|value| !value.trim().is_empty())
    {
        crate::ui::info("Telegram 봇 토큰이 감지되어 봇도 함께 시작합니다.");
    } else {
        crate::ui::warn("Telegram 봇 토큰이 없어 봇은 시작하지 않습니다.");
    }
    crate::ui::info("종료하려면 Ctrl-C를 누르십시오.");
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
