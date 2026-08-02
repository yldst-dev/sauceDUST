use std::path::{Path, PathBuf};

pub const ENV_EXAMPLE: &str = include_str!("../../../.env.example");
const PYTHON_MAIN: &str = include_str!("../../../python/embedding-worker/main.py");
const PYTHON_EMBEDDER: &str = include_str!("../../../python/embedding-worker/embedder.py");
const PYTHON_REQUIREMENTS: &str = include_str!("../../../python/embedding-worker/requirements.txt");
const PYTHON_README: &str = include_str!("../../../python/embedding-worker/README.md");

pub async fn ensure(root: &Path) -> anyhow::Result<()> {
    ensure_env_example(root).await?;
    ensure_python_worker(root).await
}

pub async fn ensure_env_example(root: &Path) -> anyhow::Result<()> {
    write_if_changed(&root.join(".env.example"), ENV_EXAMPLE).await
}

pub async fn ensure_python_worker(_root: &Path) -> anyhow::Result<()> {
    let worker = python_worker_dir()?;
    tokio::fs::create_dir_all(&worker).await?;
    write_if_changed(&worker.join("main.py"), PYTHON_MAIN).await?;
    write_if_changed(&worker.join("embedder.py"), PYTHON_EMBEDDER).await?;
    write_if_changed(&worker.join("requirements.txt"), PYTHON_REQUIREMENTS).await?;
    write_if_missing(&worker.join("README.md"), PYTHON_README).await?;
    Ok(())
}

pub fn python_worker_dir() -> anyhow::Result<PathBuf> {
    Ok(crate::paths::data_dir()?
        .join("runtime")
        .join("python")
        .join("embedding-worker"))
}

async fn write_if_missing(path: &Path, content: &str) -> anyhow::Result<()> {
    if path.exists() {
        return Ok(());
    }
    if let Some(parent) = path.parent() {
        tokio::fs::create_dir_all(parent).await?;
    }
    tokio::fs::write(path, content).await?;
    Ok(())
}

async fn write_if_changed(path: &Path, content: &str) -> anyhow::Result<()> {
    if path.exists() && tokio::fs::read_to_string(path).await.unwrap_or_default() == content {
        return Ok(());
    }
    if let Some(parent) = path.parent() {
        tokio::fs::create_dir_all(parent).await?;
    }
    tokio::fs::write(path, content).await?;
    Ok(())
}
