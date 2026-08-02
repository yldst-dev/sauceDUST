use std::path::{Path, PathBuf};
use std::process::Stdio;

use tokio::process::Command;

pub async fn ensure_worker(root: &Path) -> anyhow::Result<()> {
    crate::runtime_assets::ensure_python_worker(root).await?;
    let worker_dir = crate::runtime_assets::python_worker_dir()?;
    let venv_dir = worker_dir.join(".venv");
    if !venv_dir.exists() {
        crate::command::run(
            Command::new(base_python()?)
                .arg("-m")
                .arg("venv")
                .arg(&venv_dir)
                .current_dir(&worker_dir),
            "python venv",
        )
        .await?;
    }

    let python = venv_dir.join("bin").join("python");
    crate::command::run(
        Command::new(&python)
            .arg("-m")
            .arg("pip")
            .arg("install")
            .arg("--upgrade")
            .arg("pip")
            .current_dir(&worker_dir),
        "pip upgrade",
    )
    .await?;
    crate::command::run(
        Command::new(&python)
            .arg("-m")
            .arg("pip")
            .arg("install")
            .arg("-r")
            .arg("requirements.txt")
            .current_dir(&worker_dir),
        "pip install requirements",
    )
    .await
}

pub fn embedding_worker_command(_root: &Path) -> anyhow::Result<Command> {
    let worker_dir = crate::runtime_assets::python_worker_dir()?;
    std::fs::create_dir_all(&worker_dir)?;
    let venv_python = worker_dir.join(".venv").join("bin").join("python");
    let python = if venv_python.exists() {
        venv_python
    } else {
        base_python()?
    };
    let mut command = Command::new(python);
    command
        .arg("-m")
        .arg("uvicorn")
        .arg("main:app")
        .arg("--host")
        .arg("0.0.0.0")
        .arg("--port")
        .arg("8100")
        .arg("--no-access-log")
        .arg("--log-level")
        .arg("warning")
        .current_dir(worker_dir)
        .stdin(Stdio::null());
    Ok(command)
}

pub fn base_python() -> anyhow::Result<PathBuf> {
    if let Ok(path) = std::env::var("SAUCE_PYTHON_BIN") {
        let path = PathBuf::from(path);
        if path.exists() {
            return Ok(path);
        }
    }
    for name in ["python3.12", "python3.11", "python3"] {
        if let Some(path) = crate::paths::path_in_path(name) {
            return Ok(path);
        }
    }
    for path in [
        "/opt/homebrew/opt/python@3.12/bin/python3.12",
        "/usr/local/opt/python@3.12/bin/python3.12",
        "/opt/homebrew/opt/python@3.11/bin/python3.11",
        "/usr/local/opt/python@3.11/bin/python3.11",
    ] {
        let path = PathBuf::from(path);
        if path.exists() {
            return Ok(path);
        }
    }
    anyhow::bail!("python3.12, python3.11, or python3 is required")
}
