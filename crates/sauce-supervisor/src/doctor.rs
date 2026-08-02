pub async fn run() -> anyhow::Result<()> {
    crate::paths::augment_runtime_path();
    let python = crate::python::base_python()?;
    let python = python.to_string_lossy().to_string();
    crate::command::check(&python, &["--version"]).await?;
    let brew = crate::paths::brew_bin();
    let git = crate::paths::git_bin();
    let cargo = crate::paths::cargo_bin();
    crate::command::check(&brew.to_string_lossy(), &["--version"]).await?;
    crate::command::check(&git.to_string_lossy(), &["--version"]).await?;
    crate::command::check(&cargo.to_string_lossy(), &["--version"]).await?;
    println!("doctor ok");
    Ok(())
}
