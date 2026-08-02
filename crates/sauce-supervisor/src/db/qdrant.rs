use std::env;
use std::path::PathBuf;
use std::process::Stdio;

use tokio::process::Command;

pub async fn ensure_binary() -> anyhow::Result<()> {
    if binary().is_some() {
        return Ok(());
    }

    let data_dir = crate::paths::data_dir()?;
    let source_dir = data_dir.join("qdrant-src");
    tokio::fs::create_dir_all(&data_dir).await?;
    if !source_dir.exists() {
        crate::command::run(
            Command::new(crate::paths::git_bin())
                .arg("clone")
                .arg("--depth")
                .arg("1")
                .arg("https://github.com/qdrant/qdrant.git")
                .arg(&source_dir),
            "clone qdrant",
        )
        .await?;
    }
    crate::command::run(
        Command::new(crate::paths::cargo_bin())
            .arg("build")
            .arg("--release")
            .arg("--bin")
            .arg("qdrant")
            .env("PROTOC", crate::paths::protoc_bin())
            .env("CARGO_PROFILE_RELEASE_LTO", "off")
            .env("CARGO_PROFILE_RELEASE_CODEGEN_UNITS", "16")
            .current_dir(&source_dir),
        "build qdrant",
    )
    .await
}

pub fn command() -> anyhow::Result<Command> {
    let binary = binary()
        .ok_or_else(|| anyhow::anyhow!("qdrant binary not found; run saucedust bootstrap"))?;
    let qdrant_storage = crate::paths::native_qdrant_storage_dir()?;
    std::fs::create_dir_all(&qdrant_storage)?;
    let mut command = Command::new(binary);
    command
        .env(
            "QDRANT__STORAGE__STORAGE_PATH",
            qdrant_storage.to_string_lossy().to_string(),
        )
        .env("QDRANT__SERVICE__HTTP_PORT", "6333")
        .env("QDRANT__SERVICE__GRPC_PORT", "6334")
        .stdin(Stdio::null());
    Ok(command)
}

fn binary() -> Option<PathBuf> {
    if let Ok(path) = env::var("QDRANT_BIN") {
        let path = PathBuf::from(path);
        if path.exists() {
            return Some(path);
        }
    }

    if let Ok(data_dir) = crate::paths::data_dir() {
        let path = data_dir
            .join("qdrant-src")
            .join("target")
            .join("release")
            .join("qdrant");
        if path.exists() {
            return Some(path);
        }
    }

    crate::paths::path_in_path("qdrant")
}
