use std::env;
use std::fs::OpenOptions;
use std::path::{Path, PathBuf};
use std::process::Stdio;

use tokio::process::Command;
use tokio::time::{sleep, Duration};

const LABEL: &str = "local.saucedust";

pub async fn start_background(args: Vec<String>) -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    crate::runtime_assets::ensure(&root).await?;
    tokio::fs::create_dir_all(root.join("run")).await?;
    tokio::fs::create_dir_all(root.join("logs")).await?;
    let pid_path = pid_path(&root);
    if let Some(pid) = read_pid(&pid_path).await? {
        if pid_alive(pid).await {
            println!("already running: pid {pid}");
            return Ok(());
        }
    }

    env::remove_var("SAUCEDUST_RUN_LOG_PATH");
    let log_path = crate::process::prepare_run_log_env()?;
    let stdout = OpenOptions::new()
        .create(true)
        .append(true)
        .open(&log_path)?;
    let stderr = stdout.try_clone()?;
    let mut command = Command::new(env::current_exe()?);
    command
        .arg("serve")
        .args(args)
        .current_dir(&root)
        .stdin(Stdio::null())
        .stdout(Stdio::from(stdout))
        .stderr(Stdio::from(stderr));
    let child = command.spawn()?;
    let Some(pid) = child.id() else {
        anyhow::bail!("failed to read spawned process id");
    };
    tokio::fs::write(&pid_path, pid.to_string()).await?;
    println!("started saucedust: pid {pid}");
    println!("logs: {}", log_path.display());
    Ok(())
}

pub async fn stop_background() -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    let pid_path = pid_path(&root);
    let Some(pid) = read_pid(&pid_path).await? else {
        println!("not running");
        let _ = crate::db::down().await;
        return Ok(());
    };
    if !pid_alive(pid).await {
        let _ = tokio::fs::remove_file(&pid_path).await;
        println!("stale pid removed");
        let _ = crate::db::down().await;
        return Ok(());
    }
    let _ = Command::new("kill").arg(pid.to_string()).status().await;
    for _ in 0..20 {
        if !pid_alive(pid).await {
            let _ = tokio::fs::remove_file(&pid_path).await;
            let _ = crate::db::down().await;
            println!("stopped saucedust");
            return Ok(());
        }
        sleep(Duration::from_millis(500)).await;
    }
    let _ = Command::new("kill")
        .arg("-9")
        .arg(pid.to_string())
        .status()
        .await;
    let _ = tokio::fs::remove_file(&pid_path).await;
    let _ = crate::db::down().await;
    println!("force stopped saucedust");
    Ok(())
}

pub async fn status_background() -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    let pid_path = pid_path(&root);
    match read_pid(&pid_path).await? {
        Some(pid) if pid_alive(pid).await => {
            println!("running: pid {pid}");
            println!("logs: {}", crate::process::latest_log_path()?.display());
        }
        Some(pid) => {
            println!("not running, stale pid {pid}");
        }
        None => {
            println!("not running");
        }
    }
    Ok(())
}

pub async fn install_launchd(args: Vec<String>) -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    crate::runtime_assets::ensure(&root).await?;
    tokio::fs::create_dir_all(root.join("logs")).await?;
    let plist = plist_path()?;
    let content = launchd_plist(&root, &args)?;
    tokio::fs::write(&plist, content).await?;
    let _ = Command::new("launchctl")
        .arg("unload")
        .arg(&plist)
        .status()
        .await;
    crate::command::run(
        Command::new("launchctl").arg("load").arg(&plist),
        "launchctl load",
    )
    .await?;
    println!("installed launchd service: {}", plist.display());
    Ok(())
}

pub async fn start_launchd() -> anyhow::Result<()> {
    crate::command::run(
        Command::new("launchctl").arg("start").arg(LABEL),
        "launchctl start",
    )
    .await
}

pub async fn stop_launchd() -> anyhow::Result<()> {
    let _ = Command::new("launchctl")
        .arg("stop")
        .arg(LABEL)
        .status()
        .await;
    let _ = crate::db::down().await;
    println!("stopped launchd service");
    Ok(())
}

pub async fn status_launchd() -> anyhow::Result<()> {
    let output = Command::new("launchctl").arg("list").output().await?;
    let raw = String::from_utf8_lossy(&output.stdout);
    if let Some(line) = raw.lines().find(|line| line.contains(LABEL)) {
        println!("{line}");
    } else {
        println!("launchd service is not loaded");
    }
    Ok(())
}

pub async fn uninstall_launchd() -> anyhow::Result<()> {
    let plist = plist_path()?;
    let _ = Command::new("launchctl")
        .arg("unload")
        .arg(&plist)
        .status()
        .await;
    let _ = tokio::fs::remove_file(&plist).await;
    let _ = crate::db::down().await;
    println!("uninstalled launchd service");
    Ok(())
}

pub async fn run_foreground(args: Vec<String>) -> anyhow::Result<()> {
    let root = crate::paths::workspace_root()?;
    let mut command = Command::new(env::current_exe()?);
    command
        .args(args)
        .current_dir(root)
        .stdin(Stdio::inherit())
        .stdout(Stdio::inherit())
        .stderr(Stdio::inherit());
    crate::command::run(&mut command, "saucedust").await
}

fn pid_path(root: &Path) -> PathBuf {
    root.join("run").join("saucedust.pid")
}

async fn read_pid(path: &Path) -> anyhow::Result<Option<u32>> {
    let raw = match tokio::fs::read_to_string(path).await {
        Ok(raw) => raw,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(None),
        Err(error) => return Err(error.into()),
    };
    Ok(raw.trim().parse::<u32>().ok())
}

async fn pid_alive(pid: u32) -> bool {
    Command::new("kill")
        .arg("-0")
        .arg(pid.to_string())
        .status()
        .await
        .map(|status| status.success())
        .unwrap_or(false)
}

fn plist_path() -> anyhow::Result<PathBuf> {
    Ok(PathBuf::from(env::var("HOME")?)
        .join("Library")
        .join("LaunchAgents")
        .join(format!("{LABEL}.plist")))
}

fn launchd_plist(root: &Path, args: &[String]) -> anyhow::Result<String> {
    let exe = env::current_exe()?;
    let mut arguments = vec!["serve".to_owned()];
    arguments.extend(args.iter().cloned());
    let mut program_arguments = format!(
        "        <string>{}</string>\n",
        xml_escape(&exe.display().to_string())
    );
    for argument in arguments {
        program_arguments.push_str(&format!(
            "        <string>{}</string>\n",
            xml_escape(&argument)
        ));
    }
    let path = "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin";
    Ok(format!(
        r#"<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{}</string>
    <key>ProgramArguments</key>
    <array>
{program_arguments}    </array>
    <key>WorkingDirectory</key>
    <string>{}</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>{}</string>
    <key>StandardErrorPath</key>
    <string>{}</string>
    <key>EnvironmentVariables</key>
    <dict>
        <key>LANG</key>
        <string>en_US.UTF-8</string>
        <key>LC_ALL</key>
        <string>en_US.UTF-8</string>
        <key>PATH</key>
        <string>{}</string>
    </dict>
</dict>
</plist>
"#,
        xml_escape(LABEL),
        xml_escape(&root.display().to_string()),
        xml_escape(
            &root
                .join("logs")
                .join("saucedust.launchd.log")
                .display()
                .to_string()
        ),
        xml_escape(
            &root
                .join("logs")
                .join("saucedust.launchd.err.log")
                .display()
                .to_string()
        ),
        xml_escape(path)
    ))
}

fn xml_escape(value: &str) -> String {
    value
        .replace('&', "&amp;")
        .replace('<', "&lt;")
        .replace('>', "&gt;")
        .replace('"', "&quot;")
        .replace('\'', "&apos;")
}
