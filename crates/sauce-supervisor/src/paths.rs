use std::env;
use std::path::{Path, PathBuf};

const EXTRA_PATHS: &[&str] = &[
    "/opt/homebrew/bin",
    "/opt/homebrew/sbin",
    "/usr/local/bin",
    "/usr/local/sbin",
    "/opt/homebrew/opt/postgresql@16/bin",
    "/usr/local/opt/postgresql@16/bin",
    "/opt/homebrew/opt/python@3.12/bin",
    "/usr/local/opt/python@3.12/bin",
    "/opt/homebrew/opt/python@3.11/bin",
    "/usr/local/opt/python@3.11/bin",
    "/opt/homebrew/opt/protobuf/bin",
    "/usr/local/opt/protobuf/bin",
    "/usr/bin",
    "/bin",
    "/usr/sbin",
    "/sbin",
];

pub fn workspace_root() -> anyhow::Result<PathBuf> {
    let mut dir = env::current_dir()?;
    let original = dir.clone();
    loop {
        if is_workspace_root(&dir) {
            return Ok(dir);
        }
        if !dir.pop() {
            return Ok(original);
        }
    }
}

fn is_workspace_root(dir: &Path) -> bool {
    dir.join("Cargo.toml").exists()
        || dir.join(".env").exists()
        || dir.join(".env.example").exists()
        || dir.join("python").join("embedding-worker").exists()
}

pub fn data_dir() -> anyhow::Result<PathBuf> {
    if let Some(path) = env_path("SAUCE_ENGINE_DATA_DIR") {
        return Ok(path);
    }
    let home = env::var("HOME")?;
    Ok(PathBuf::from(home).join(".saucedust"))
}

pub fn native_postgres_data_dir() -> anyhow::Result<PathBuf> {
    Ok(env_path("SAUCE_ENGINE_POSTGRES_DATA_DIR").unwrap_or(data_dir()?.join("postgres-data")))
}

pub fn native_qdrant_storage_dir() -> anyhow::Result<PathBuf> {
    Ok(env_path("SAUCE_ENGINE_QDRANT_STORAGE_DIR").unwrap_or(data_dir()?.join("qdrant-storage")))
}

pub fn postgres_bin(name: &str) -> PathBuf {
    tool_bin(name)
}

pub fn brew_bin() -> PathBuf {
    tool_bin("brew")
}

pub fn cargo_bin() -> PathBuf {
    tool_bin("cargo")
}

pub fn git_bin() -> PathBuf {
    tool_bin("git")
}

pub fn protoc_bin() -> PathBuf {
    tool_bin("protoc")
}

pub fn cmake_bin() -> PathBuf {
    tool_bin("cmake")
}

pub fn pkg_config_bin() -> PathBuf {
    tool_bin("pkg-config")
}

pub fn tool_bin(name: &str) -> PathBuf {
    path_in_path(name).unwrap_or_else(|| PathBuf::from(name))
}

pub fn path_in_path(name: &str) -> Option<PathBuf> {
    let path_env = env::var_os("PATH").unwrap_or_default();
    env::split_paths(&path_env)
        .chain(home_tool_paths())
        .chain(EXTRA_PATHS.iter().map(PathBuf::from))
        .map(|path| path.join(name))
        .find(|path| path.exists())
}

pub fn augment_runtime_path() {
    let mut paths: Vec<PathBuf> = env::var_os("PATH")
        .map(|raw| env::split_paths(&raw).collect())
        .unwrap_or_default();
    for path in home_tool_paths() {
        if path.exists() && !paths.contains(&path) {
            paths.push(path);
        }
    }
    for extra in EXTRA_PATHS {
        let path = PathBuf::from(extra);
        if path.exists() && !paths.contains(&path) {
            paths.push(path);
        }
    }
    if let Ok(joined) = env::join_paths(paths) {
        env::set_var("PATH", joined);
    }
}

fn home_tool_paths() -> Vec<PathBuf> {
    let Some(home) = env::var_os("HOME") else {
        return Vec::new();
    };
    let home = PathBuf::from(home);
    vec![
        home.join(".cargo").join("bin"),
        home.join(".local").join("bin"),
    ]
}

fn env_path(name: &str) -> Option<PathBuf> {
    let raw = env::var(name).ok()?;
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return None;
    }
    Some(expand_path(trimmed))
}

pub fn expand_path(raw: &str) -> PathBuf {
    let expanded = if raw == "~" {
        env::var("HOME").unwrap_or_else(|_| raw.to_owned())
    } else if let Some(rest) = raw.strip_prefix("~/") {
        env::var("HOME")
            .map(|home| format!("{home}/{rest}"))
            .unwrap_or_else(|_| raw.to_owned())
    } else {
        raw.to_owned()
    };
    let path = PathBuf::from(expanded);
    if path.is_absolute() {
        path
    } else {
        workspace_root()
            .map(|root| root.join(&path))
            .unwrap_or(path)
    }
}
