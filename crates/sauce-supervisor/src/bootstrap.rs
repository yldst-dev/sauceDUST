use std::path::Path;

pub async fn run(skip_python: bool) -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    crate::runtime_assets::ensure(&root).await?;
    crate::system_setup::ensure_bootstrap_dependencies().await?;
    crate::doctor::run().await?;
    ensure_env_file(&root).await?;
    crate::env_config::sync_existing(&root).await?;
    dotenvy::from_path_override(root.join(".env")).ok();
    crate::db::bootstrap().await?;

    if !skip_python {
        crate::python::ensure_worker(&root).await?;
    }

    println!("bootstrap ok");
    Ok(())
}

async fn ensure_env_file(root: &Path) -> anyhow::Result<()> {
    let env_path = root.join(".env");
    if env_path.exists() {
        return Ok(());
    }
    crate::runtime_assets::ensure_env_example(root).await?;
    tokio::fs::write(&env_path, crate::runtime_assets::ENV_EXAMPLE).await?;
    println!("created {}", env_path.display());
    Ok(())
}
