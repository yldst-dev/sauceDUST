use std::path::{Path, PathBuf};
use std::process::Stdio;

use anyhow::Context;
use tokio::process::Command;

pub async fn ensure_bootstrap_dependencies() -> anyhow::Result<()> {
    crate::paths::augment_runtime_path();
    ensure_homebrew_available().await?;
    ensure_xcode_tools().await?;
    ensure_brew_package("git").await?;
    ensure_brew_package("rust").await?;
    ensure_brew_package("python@3.12").await?;
    ensure_brew_package("postgresql@16").await?;
    ensure_brew_package("protobuf").await?;
    ensure_brew_package("cmake").await?;
    ensure_brew_package("pkg-config").await?;
    crate::paths::augment_runtime_path();
    ensure_required_command(crate::paths::git_bin(), "git").await?;
    ensure_required_command(crate::paths::cargo_bin(), "cargo").await?;
    ensure_required_command(crate::paths::postgres_bin("pg_ctl"), "pg_ctl").await?;
    ensure_required_command(crate::paths::protoc_bin(), "protoc").await?;
    ensure_required_command(crate::paths::cmake_bin(), "cmake").await?;
    ensure_required_command(crate::paths::pkg_config_bin(), "pkg-config").await?;
    Ok(())
}

async fn ensure_xcode_tools() -> anyhow::Result<()> {
    if command_success(Command::new("xcode-select").arg("-p")).await {
        return Ok(());
    }
    println!("Xcode Command Line Tools가 없어 설치 창을 엽니다.");
    let _ = Command::new("xcode-select")
        .arg("--install")
        .stdin(Stdio::null())
        .status()
        .await;
    anyhow::bail!(
        "Xcode Command Line Tools 설치를 완료한 뒤 ./saucedust bootstrap을 다시 실행하십시오"
    )
}

pub async fn ensure_homebrew_available() -> anyhow::Result<()> {
    if crate::paths::path_in_path("brew").is_some() {
        return Ok(());
    }
    println!("Homebrew가 없어 자동 설치를 시도합니다.");
    let install = r#"NONINTERACTIVE=1 /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)""#;
    crate::command::run(
        Command::new("/bin/bash").arg("-c").arg(install),
        "install Homebrew",
    )
    .await?;
    crate::paths::augment_runtime_path();
    load_brew_shellenv().await;
    if crate::paths::path_in_path("brew").is_none() {
        anyhow::bail!("Homebrew 설치 후 brew가 PATH에서 감지되지 않습니다. 터미널을 다시 열고 ./saucedust bootstrap을 다시 실행하십시오");
    }
    Ok(())
}

async fn load_brew_shellenv() {
    for brew in [
        "/opt/homebrew/bin/brew",
        "/usr/local/bin/brew",
        "/home/linuxbrew/.linuxbrew/bin/brew",
    ] {
        if !Path::new(brew).exists() {
            continue;
        }
        let output = Command::new(brew)
            .arg("shellenv")
            .stdin(Stdio::null())
            .output()
            .await;
        let Ok(output) = output else {
            continue;
        };
        if !output.status.success() {
            continue;
        }
        let raw = String::from_utf8_lossy(&output.stdout);
        for line in raw.lines() {
            let Some(value) = line.strip_prefix("export PATH=\"") else {
                continue;
            };
            let Some(value) = value.strip_suffix("\";") else {
                continue;
            };
            std::env::set_var("PATH", value);
        }
        crate::paths::augment_runtime_path();
        return;
    }
}

async fn ensure_brew_package(package: &str) -> anyhow::Result<()> {
    let brew = crate::paths::brew_bin();
    if command_success(
        Command::new(&brew)
            .arg("list")
            .arg("--versions")
            .arg(package),
    )
    .await
    {
        return Ok(());
    }
    println!("installing {package}");
    crate::command::run(
        Command::new(&brew).arg("install").arg(package),
        &format!("brew install {package}"),
    )
    .await
}

async fn ensure_required_command(path: PathBuf, label: &str) -> anyhow::Result<()> {
    if executable_exists(&path) || crate::paths::path_in_path(label).is_some() {
        return Ok(());
    }
    anyhow::bail!("{label} command was not found after automatic setup")
}

fn executable_exists(path: &Path) -> bool {
    path.exists() && path.is_file()
}

async fn command_success(command: &mut Command) -> bool {
    command
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .await
        .with_context(|| "command check failed")
        .is_ok_and(|status| status.success())
}
