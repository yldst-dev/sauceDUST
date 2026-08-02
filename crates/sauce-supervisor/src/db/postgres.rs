use std::path::Path;
use std::process::Stdio;
use std::time::Duration;
use std::{env, net::TcpListener};

use anyhow::Context;
use tokio::process::Command;
use tokio::time::{sleep, Instant};

const POSTGRES_HOST: &str = "127.0.0.1";
const DEFAULT_POSTGRES_PORT: u16 = 5432;
const FALLBACK_POSTGRES_PORT_START: u16 = 55432;
const FALLBACK_POSTGRES_PORT_END: u16 = 55452;

pub async fn start() -> anyhow::Result<()> {
    ensure_cluster().await?;
    let data_dir = crate::paths::native_postgres_data_dir()?;
    let requested_port = configured_port();

    if status().await {
        let port = existing_cluster_port(&data_dir)
            .await?
            .unwrap_or(requested_port);
        set_database_url(port);
        wait(port).await?;
        return ensure_database(port).await;
    }

    if let Some(port) = existing_cluster_process(&data_dir).await? {
        set_database_url(port);
        wait(port).await?;
        return ensure_database(port).await;
    }

    if server_ready(requested_port).await {
        crate::ui::warn("localhost:5432에서 이미 실행 중인 PostgreSQL을 감지했습니다.");
        crate::ui::info("새 PostgreSQL을 시작하지 않고 기존 서버를 재사용합니다.");
        match ensure_database(requested_port).await {
            Ok(()) => {
                set_database_url(requested_port);
                return Ok(());
            }
            Err(error) => {
                crate::ui::warn(&format!("기존 PostgreSQL 재사용 실패: {error:#}"));
                crate::ui::info("saucedust 전용 PostgreSQL을 다른 포트로 시작합니다.");
            }
        }
    }

    cleanup_stale_pid(&data_dir).await?;
    let start_port = if requested_port == DEFAULT_POSTGRES_PORT || port_available(requested_port) {
        requested_port
    } else {
        choose_fallback_port().with_context(|| {
            format!(
                "configured PostgreSQL port {requested_port} is unavailable and no fallback port is free"
            )
        })?
    };
    match start_cluster(&data_dir, start_port).await {
        Ok(()) => {
            set_database_url(start_port);
            wait(start_port).await?;
            ensure_database(start_port).await
        }
        Err(error) => {
            let fallback_port = choose_fallback_port().with_context(|| {
                format!(
                    "PostgreSQL start failed on port {start_port}: {error:#}. No fallback port is free."
                )
            })?;
            crate::ui::warn(&format!(
                "PostgreSQL {start_port} 포트 시작 실패: {error:#}"
            ));
            crate::ui::info(&format!(
                "fallback 포트 {fallback_port}로 PostgreSQL을 다시 시작합니다."
            ));
            start_cluster(&data_dir, fallback_port).await?;
            set_database_url(fallback_port);
            wait(fallback_port).await?;
            ensure_database(fallback_port).await
        }
    }
}

async fn start_cluster(data_dir: &Path, port: u16) -> anyhow::Result<()> {
    let log_path = crate::paths::data_dir()?.join("postgres.log");
    let status = Command::new(crate::paths::postgres_bin("pg_ctl"))
        .arg("-D")
        .arg(data_dir)
        .arg("start")
        .arg("-l")
        .arg(&log_path)
        .arg("-o")
        .arg(format!("-h {POSTGRES_HOST} -p {port}"))
        .stdin(Stdio::null())
        .status()
        .await?;
    if !status.success() {
        let tail = tail_log(&log_path).await.unwrap_or_default();
        anyhow::bail!("pg_ctl start postgres failed with {status}\n{tail}");
    }
    Ok(())
}

pub async fn stop() -> anyhow::Result<()> {
    crate::command::run(
        Command::new(crate::paths::postgres_bin("pg_ctl"))
            .arg("-D")
            .arg(crate::paths::native_postgres_data_dir()?)
            .arg("stop")
            .arg("-m")
            .arg("fast"),
        "pg_ctl stop postgres",
    )
    .await
}

async fn ensure_cluster() -> anyhow::Result<()> {
    let postgres_data_dir = crate::paths::native_postgres_data_dir()?;
    if postgres_data_dir.join("PG_VERSION").exists() {
        return Ok(());
    }
    if let Some(parent) = postgres_data_dir.parent() {
        tokio::fs::create_dir_all(parent).await?;
    }
    crate::command::run(
        Command::new(crate::paths::postgres_bin("initdb"))
            .arg("-D")
            .arg(&postgres_data_dir)
            .arg("-U")
            .arg("sauce")
            .arg("--auth=trust"),
        "initdb postgres",
    )
    .await
}

async fn status() -> bool {
    Command::new(crate::paths::postgres_bin("pg_ctl"))
        .arg("-D")
        .arg(match crate::paths::native_postgres_data_dir() {
            Ok(path) => path,
            Err(_) => return false,
        })
        .arg("status")
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .await
        .is_ok_and(|status| status.success())
}

async fn existing_cluster_process(data_dir: &Path) -> anyhow::Result<Option<u16>> {
    let Some(pid) = read_postmaster_pid(data_dir).await? else {
        return Ok(None);
    };
    if !pid_alive(pid).await {
        return Ok(None);
    }
    println!(
        "PostgreSQL already running for {} with pid {pid}",
        data_dir.display()
    );
    Ok(existing_cluster_port(data_dir)
        .await?
        .or(Some(DEFAULT_POSTGRES_PORT)))
}

async fn cleanup_stale_pid(data_dir: &Path) -> anyhow::Result<()> {
    let Some(pid) = read_postmaster_pid(data_dir).await? else {
        return Ok(());
    };
    if pid_alive(pid).await {
        return Ok(());
    }
    let pid_path = data_dir.join("postmaster.pid");
    tokio::fs::remove_file(&pid_path).await?;
    println!("removed stale {}", pid_path.display());
    Ok(())
}

async fn read_postmaster_pid(data_dir: &Path) -> anyhow::Result<Option<u32>> {
    let pid_path = data_dir.join("postmaster.pid");
    let raw = match tokio::fs::read_to_string(pid_path).await {
        Ok(raw) => raw,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error.into()),
    };
    Ok(raw.lines().next().and_then(|line| line.trim().parse().ok()))
}

async fn pid_alive(pid: u32) -> bool {
    Command::new("kill")
        .arg("-0")
        .arg(pid.to_string())
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .await
        .is_ok_and(|status| status.success())
}

async fn tail_log(path: &Path) -> anyhow::Result<String> {
    let raw = tokio::fs::read_to_string(path).await?;
    let lines = raw.lines().rev().take(80).collect::<Vec<_>>();
    Ok(lines.into_iter().rev().collect::<Vec<_>>().join("\n"))
}

async fn existing_cluster_port(data_dir: &Path) -> anyhow::Result<Option<u16>> {
    let pid_path = data_dir.join("postmaster.pid");
    let raw = match tokio::fs::read_to_string(pid_path).await {
        Ok(raw) => raw,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error.into()),
    };
    Ok(raw.lines().nth(3).and_then(|line| line.trim().parse().ok()))
}

async fn wait(port: u16) -> anyhow::Result<()> {
    let deadline = Instant::now() + Duration::from_secs(60);
    while Instant::now() < deadline {
        if server_ready(port).await {
            return Ok(());
        }
        sleep(Duration::from_secs(2)).await;
    }
    anyhow::bail!("timed out waiting for native PostgreSQL on port {port}")
}

async fn ensure_database(port: u16) -> anyhow::Result<()> {
    let role_sql = "DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'sauce') THEN CREATE ROLE sauce LOGIN PASSWORD 'saucepass'; ELSE ALTER ROLE sauce WITH LOGIN PASSWORD 'saucepass'; END IF; END $$;";
    if !try_role_sql(port, Some("sauce"), role_sql).await?
        && !try_role_sql(port, None, role_sql).await?
    {
        anyhow::bail!(
            "could not connect to PostgreSQL on port {port} as sauce or current macOS user to prepare role"
        );
    }

    let output = Command::new(crate::paths::postgres_bin("psql"))
        .arg("postgres")
        .arg("-h")
        .arg(POSTGRES_HOST)
        .arg("-p")
        .arg(port.to_string())
        .arg("-U")
        .arg("sauce")
        .env("PGPASSWORD", "saucepass")
        .arg("-tAc")
        .arg("SELECT 1 FROM pg_database WHERE datname = 'sauce'")
        .stdin(Stdio::null())
        .output()
        .await?;
    crate::command::ensure_success("check postgres database", output.status)?;
    if String::from_utf8_lossy(&output.stdout).trim() != "1" {
        crate::command::run(
            Command::new(crate::paths::postgres_bin("createdb"))
                .arg("-h")
                .arg(POSTGRES_HOST)
                .arg("-p")
                .arg(port.to_string())
                .arg("-U")
                .arg("sauce")
                .env("PGPASSWORD", "saucepass")
                .arg("-O")
                .arg("sauce")
                .arg("sauce"),
            "createdb sauce",
        )
        .await?;
    }

    Ok(())
}

async fn server_ready(port: u16) -> bool {
    Command::new(crate::paths::postgres_bin("pg_isready"))
        .arg("-h")
        .arg(POSTGRES_HOST)
        .arg("-p")
        .arg(port.to_string())
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .await
        .is_ok_and(|status| status.success())
}

async fn try_role_sql(port: u16, user: Option<&str>, sql: &str) -> anyhow::Result<bool> {
    let mut command = Command::new(crate::paths::postgres_bin("psql"));
    command
        .arg("postgres")
        .arg("-h")
        .arg(POSTGRES_HOST)
        .arg("-p")
        .arg(port.to_string())
        .arg("-v")
        .arg("ON_ERROR_STOP=1")
        .arg("-c")
        .arg(sql)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null());
    if let Some(user) = user {
        command.arg("-U").arg(user).env("PGPASSWORD", "saucepass");
    }
    let status = command.status().await?;
    Ok(status.success())
}

fn configured_port() -> u16 {
    env::var("DATABASE_URL")
        .ok()
        .and_then(|url| postgres_port_from_url(&url))
        .unwrap_or(DEFAULT_POSTGRES_PORT)
}

fn postgres_port_from_url(url: &str) -> Option<u16> {
    let after_at = url.rsplit_once('@')?.1;
    let host_port = after_at.split('/').next()?;
    let port = host_port.rsplit_once(':')?.1;
    port.parse().ok()
}

fn set_database_url(port: u16) {
    env::set_var(
        "DATABASE_URL",
        format!("postgres://sauce:saucepass@{POSTGRES_HOST}:{port}/sauce"),
    );
}

fn choose_fallback_port() -> Option<u16> {
    (FALLBACK_POSTGRES_PORT_START..=FALLBACK_POSTGRES_PORT_END).find(|port| port_available(*port))
}

fn port_available(port: u16) -> bool {
    TcpListener::bind((POSTGRES_HOST, port)).is_ok()
}
