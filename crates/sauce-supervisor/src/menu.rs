use std::io::{self, Write};
use std::path::PathBuf;

use reqwest::multipart;

pub async fn run() -> anyhow::Result<()> {
    loop {
        print_main_menu();
        match read_line("선택: ")?.trim() {
            "1" => run_action(crate::bootstrap::run(false).await).await?,
            "2" => {
                run_action(
                    crate::env_config::run(crate::cli::EnvCommand::Setup { advanced: false }).await,
                )
                .await?
            }
            "3" => {
                run_action(
                    crate::env_config::run(crate::cli::EnvCommand::Setup { advanced: true }).await,
                )
                .await?
            }
            "4" => run_action(crate::service::start_background(Vec::new()).await).await?,
            "5" => {
                run_action(
                    crate::service::start_background(vec![
                        "--no-indexer".to_owned(),
                        "--no-telegram".to_owned(),
                    ])
                    .await,
                )
                .await?
            }
            "6" => run_action(crate::service::stop_background().await).await?,
            "7" => run_action(crate::service::status_background().await).await?,
            "8" => service_menu().await?,
            "9" => index_menu().await?,
            "10" => test_search_menu().await?,
            "11" => {
                run_action(crate::service::run_foreground(vec!["verify".to_owned()]).await).await?
            }
            "12" => run_action(crate::stats::run().await).await?,
            "13" => run_action(crate::doctor::run().await).await?,
            "14" => vpn_menu().await?,
            "0" => return Ok(()),
            _ => {
                println!("알 수 없는 선택입니다.");
                pause()?;
            }
        }
    }
}

fn print_main_menu() {
    println!();
    println!("saucedust");
    println!("1. 최초 설치/bootstrap");
    println!("2. 환경값 설정");
    println!("3. 환경값 고급 설정");
    println!("4. 백그라운드 시작");
    println!("5. 백그라운드 시작(API만)");
    println!("6. 백그라운드 중지");
    println!("7. 백그라운드 상태");
    println!("8. macOS launchd 서비스 관리");
    println!("9. Danbooru 인덱싱 실행");
    println!("10. 이미지 검색 테스트");
    println!("11. 전체 연결 검증");
    println!("12. 저장소 통계 보기");
    println!("13. 시스템 의존성 점검");
    println!("14. ExpressVPN 프록시 관리");
    println!("0. 종료");
}

async fn vpn_menu() -> anyhow::Result<()> {
    loop {
        println!();
        println!("ExpressVPN 프록시 관리");
        println!("1. 설정 및 VM 준비");
        println!("2. 시작");
        println!("3. 중지");
        println!("4. 상태");
        println!("5. 연결 테스트");
        println!("0. 뒤로");
        match read_line("선택: ")?.trim() {
            "1" => run_action(crate::vpn::setup().await).await?,
            "2" => run_action(crate::vpn::start().await).await?,
            "3" => run_action(crate::vpn::stop().await).await?,
            "4" => run_action(crate::vpn::status().await).await?,
            "5" => run_action(crate::vpn::test().await).await?,
            "0" => return Ok(()),
            _ => {
                println!("알 수 없는 선택입니다.");
                pause()?;
            }
        }
    }
}

async fn service_menu() -> anyhow::Result<()> {
    loop {
        println!();
        println!("launchd 서비스 관리");
        println!("1. 설치 및 자동 시작 등록");
        println!("2. API만 설치 및 자동 시작 등록");
        println!("3. 시작");
        println!("4. 중지");
        println!("5. 상태");
        println!("6. 제거");
        println!("0. 뒤로");
        match read_line("선택: ")?.trim() {
            "1" => run_action(crate::service::install_launchd(Vec::new()).await).await?,
            "2" => {
                run_action(
                    crate::service::install_launchd(vec![
                        "--no-indexer".to_owned(),
                        "--no-telegram".to_owned(),
                    ])
                    .await,
                )
                .await?
            }
            "3" => run_action(crate::service::start_launchd().await).await?,
            "4" => run_action(crate::service::stop_launchd().await).await?,
            "5" => run_action(crate::service::status_launchd().await).await?,
            "6" => run_action(crate::service::uninstall_launchd().await).await?,
            "0" => return Ok(()),
            _ => {
                println!("알 수 없는 선택입니다.");
                pause()?;
            }
        }
    }
}

async fn index_menu() -> anyhow::Result<()> {
    let limit = read_line("인덱싱 목표 수 [1000]: ")?;
    let limit = if limit.trim().is_empty() {
        "1000".to_owned()
    } else {
        limit.trim().to_owned()
    };
    let continuous = read_line("계속 실행 모드로 실행하시겠습니까? [y/N]: ")?;
    let mut args = vec![
        "index".to_owned(),
        "danbooru".to_owned(),
        "--limit".to_owned(),
        limit,
    ];
    if matches!(continuous.trim(), "y" | "Y" | "yes" | "YES") {
        args.push("--continuous".to_owned());
    }
    run_action(crate::service::run_foreground(args).await).await
}

async fn test_search_menu() -> anyhow::Result<()> {
    let path = read_line("검색할 이미지 파일 경로: ")?;
    let path = expand_path(path.trim());
    let result = test_search(path).await;
    run_action(result).await
}

async fn test_search(path: PathBuf) -> anyhow::Result<()> {
    if !path.exists() {
        anyhow::bail!("file not found: {}", path.display());
    }
    let api_url =
        std::env::var("SAUCE_API_URL").unwrap_or_else(|_| "http://localhost:8000".to_owned());
    let bytes = tokio::fs::read(&path).await?;
    let file_name = path
        .file_name()
        .and_then(|value| value.to_str())
        .unwrap_or("query.jpg")
        .to_owned();
    let part = multipart::Part::bytes(bytes).file_name(file_name);
    let form = multipart::Form::new().part("file", part);
    let client = reqwest::Client::new();
    let response = client
        .post(format!("{}/search", api_url.trim_end_matches('/')))
        .multipart(form)
        .send()
        .await?;
    let status = response.status();
    let body = response.text().await?;
    if !status.is_success() {
        anyhow::bail!("search failed with {status}: {body}");
    }
    println!("{body}");
    Ok(())
}

async fn run_action(result: anyhow::Result<()>) -> anyhow::Result<()> {
    if let Err(error) = result {
        println!("실패: {error:#}");
    }
    pause()?;
    Ok(())
}

fn read_line(prompt: &str) -> anyhow::Result<String> {
    print!("{prompt}");
    io::stdout().flush()?;
    let mut input = String::new();
    io::stdin().read_line(&mut input)?;
    Ok(input.trim_end_matches(['\r', '\n']).to_owned())
}

fn pause() -> anyhow::Result<()> {
    let _ = read_line("Enter를 누르면 계속합니다.")?;
    Ok(())
}

fn expand_path(raw: &str) -> PathBuf {
    if raw == "~" {
        return std::env::var("HOME")
            .map(PathBuf::from)
            .unwrap_or_else(|_| PathBuf::from(raw));
    }
    if let Some(rest) = raw.strip_prefix("~/") {
        return std::env::var("HOME")
            .map(|home| PathBuf::from(home).join(rest))
            .unwrap_or_else(|_| PathBuf::from(raw));
    }
    PathBuf::from(raw)
}
