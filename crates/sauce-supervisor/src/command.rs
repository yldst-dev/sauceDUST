use std::collections::VecDeque;
use std::process::{ExitStatus, Stdio};
use std::sync::{Arc, Mutex};
use std::time::Instant;

use anyhow::Context;
use tokio::io::{AsyncBufReadExt, AsyncRead, BufReader};
use tokio::process::Command;
use tokio::time::{sleep, Duration};

pub async fn run(command: &mut Command, label: &str) -> anyhow::Result<()> {
    let started = Instant::now();
    let live = Arc::new(Mutex::new(LiveProgress::new(label, started)));
    if let Ok(mut live) = live.lock() {
        live.redraw();
    }

    let mut child = command
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .with_context(|| format!("failed to start {label}; command not found or not executable"))?;

    let tail = Arc::new(Mutex::new(VecDeque::with_capacity(80)));
    let stdout = child.stdout.take();
    let stderr = child.stderr.take();
    let stdout_task =
        stdout.map(|stream| spawn_reader(stream, "stdout", tail.clone(), live.clone()));
    let stderr_task =
        stderr.map(|stream| spawn_reader(stream, "stderr", tail.clone(), live.clone()));
    let progress_task = spawn_progress(live.clone());

    let status = child.wait().await?;
    if let Ok(mut live) = live.lock() {
        live.finish();
    }
    if let Some(task) = stdout_task {
        let _ = task.await;
    }
    if let Some(task) = stderr_task {
        let _ = task.await;
    }
    progress_task.abort();
    let _ = progress_task.await;

    let seconds = started.elapsed().as_secs();
    if status.success() {
        crate::ui::task_ok(label, seconds);
        Ok(())
    } else {
        crate::ui::task_fail(label, seconds);
        let lines = tail
            .lock()
            .map(|tail| tail.iter().cloned().collect::<Vec<_>>())
            .unwrap_or_default();
        crate::ui::failure_box(label, &lines);
        anyhow::bail!("{label} failed with {status}")
    }
}

pub async fn check(command: &str, args: &[&str]) -> anyhow::Result<()> {
    let status = Command::new(command)
        .args(args)
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .await
        .with_context(|| format!("required command not found: {command}"))?;
    ensure_success(command, status)
}

pub fn ensure_success(label: &str, status: ExitStatus) -> anyhow::Result<()> {
    if status.success() {
        Ok(())
    } else {
        anyhow::bail!("{label} failed with {status}")
    }
}

fn spawn_reader<R>(
    stream: R,
    name: &'static str,
    tail: Arc<Mutex<VecDeque<String>>>,
    live: Arc<Mutex<LiveProgress>>,
) -> tokio::task::JoinHandle<()>
where
    R: AsyncRead + Unpin + Send + 'static,
{
    tokio::spawn(async move {
        let mut lines = BufReader::new(stream).lines();
        while let Ok(Some(line)) = lines.next_line().await {
            if let Ok(mut tail) = tail.lock() {
                if tail.len() >= 80 {
                    tail.pop_front();
                }
                tail.push_back(format!("{name}: {line}"));
            }
            if let Ok(mut live) = live.lock() {
                live.log(name, &line);
            }
        }
    })
}

fn spawn_progress(live: Arc<Mutex<LiveProgress>>) -> tokio::task::JoinHandle<()> {
    tokio::spawn(async move {
        loop {
            sleep(Duration::from_secs(1)).await;
            if let Ok(mut live) = live.lock() {
                if !live.active {
                    break;
                }
                live.tick();
            }
        }
    })
}

struct LiveProgress {
    label: String,
    started: Instant,
    tick: usize,
    active: bool,
}

impl LiveProgress {
    fn new(label: &str, started: Instant) -> Self {
        Self {
            label: label.to_owned(),
            started,
            tick: 0,
            active: true,
        }
    }

    fn redraw(&mut self) {
        let line = crate::ui::live_line(&self.label, self.tick, self.started.elapsed().as_secs());
        crate::ui::inline(&line);
    }

    fn tick(&mut self) {
        self.tick += 1;
        self.redraw();
    }

    fn log(&mut self, stream: &str, line: &str) {
        crate::ui::clear_line();
        crate::ui::log_line(stream, line);
        self.redraw();
    }

    fn finish(&mut self) {
        self.active = false;
        crate::ui::clear_line();
    }
}
