use std::collections::VecDeque;
use std::env;
use std::ffi::OsString;
use std::fs::{File, OpenOptions};
use std::io::Write;
use std::io::{stdout, IsTerminal};
use std::path::PathBuf;
use std::process::{ExitStatus, Stdio};
use std::sync::{Arc, Mutex};
use std::time::{Instant, SystemTime, UNIX_EPOCH};

use chrono::Local;
use crossterm::event::{
    self, DisableMouseCapture, EnableMouseCapture, Event, KeyCode, KeyEventKind, MouseEventKind,
};
use crossterm::execute;
use crossterm::terminal::{
    disable_raw_mode, enable_raw_mode, EnterAlternateScreen, LeaveAlternateScreen,
};
use ratatui::backend::CrosstermBackend;
use ratatui::layout::{Constraint, Direction, Layout};
use ratatui::style::{Color, Modifier, Style};
use ratatui::text::{Line, Span};
use ratatui::widgets::{Block, Borders, Paragraph, Wrap};
use ratatui::Terminal;
use tokio::io::{AsyncBufReadExt, AsyncRead, BufReader};
use tokio::process::{Child, Command};
use tokio::signal;
use tokio::time::{interval, sleep, Duration};

pub struct ManagedChild {
    name: String,
    child: Child,
    spec: ManagedChildSpec,
    restarts: u32,
    last_started: Instant,
}

#[derive(Clone)]
struct ManagedChildSpec {
    program: OsString,
    args: Vec<OsString>,
    envs: Vec<(OsString, Option<OsString>)>,
    current_dir: Option<PathBuf>,
}

impl ManagedChildSpec {
    fn from_command(command: &Command) -> Self {
        let command = command.as_std();
        Self {
            program: command.get_program().to_os_string(),
            args: command.get_args().map(|arg| arg.to_os_string()).collect(),
            envs: command
                .get_envs()
                .map(|(key, value)| (key.to_os_string(), value.map(|value| value.to_os_string())))
                .collect(),
            current_dir: command.get_current_dir().map(PathBuf::from),
        }
    }

    fn command(&self) -> Command {
        let mut command = Command::new(&self.program);
        command.args(&self.args);
        for (key, value) in &self.envs {
            if let Some(value) = value {
                command.env(key, value);
            } else {
                command.env_remove(key);
            }
        }
        if let Some(current_dir) = &self.current_dir {
            command.current_dir(current_dir);
        }
        command.stdin(Stdio::null());
        command
    }
}

impl ManagedChild {
    pub fn spawn(name: &str, mut command: Command) -> anyhow::Result<Self> {
        let spec = ManagedChildSpec::from_command(&command);
        command.stdout(Stdio::piped()).stderr(Stdio::piped());
        let child = command.spawn()?;
        if let Some(pid) = child.id() {
            crate::ui::info(&format!("프로세스 시작: {name} pid={pid}"));
        } else {
            crate::ui::info(&format!("프로세스 시작: {name}"));
        }
        Ok(Self {
            name: name.to_owned(),
            child,
            spec,
            restarts: 0,
            last_started: Instant::now(),
        })
    }

    fn restart_delay(&self) -> Duration {
        if self.last_started.elapsed() > Duration::from_secs(300) {
            return Duration::from_secs(1);
        }
        let exponent = self.restarts.min(5);
        Duration::from_secs(2u64.saturating_pow(exponent).min(30))
    }

    fn restart(&mut self) -> anyhow::Result<()> {
        if self.last_started.elapsed() > Duration::from_secs(300) {
            self.restarts = 0;
        }
        let mut command = self.spec.command();
        command.stdout(Stdio::piped()).stderr(Stdio::piped());
        self.child = command.spawn()?;
        self.restarts = self.restarts.saturating_add(1);
        self.last_started = Instant::now();
        if let Some(pid) = self.child.id() {
            crate::ui::info(&format!(
                "프로세스 재시작: {} pid={} restart={}",
                self.name, pid, self.restarts
            ));
        } else {
            crate::ui::info(&format!(
                "프로세스 재시작: {} restart={}",
                self.name, self.restarts
            ));
        }
        Ok(())
    }
}

pub fn cargo_binary_command(name: &str) -> anyhow::Result<Command> {
    let current_exe = env::current_exe()?;
    let internal = match name {
        "sauce-api" => Some("internal-api"),
        "sauce-telegram" => Some("internal-telegram"),
        "sauce-indexer" => Some("internal-indexer"),
        _ => None,
    };
    if let Some(subcommand) = internal {
        let mut command = Command::new(current_exe);
        command.arg(subcommand).stdin(Stdio::null());
        return Ok(command);
    }

    let current_exe = env::current_exe()?;
    let debug_dir = current_exe
        .parent()
        .ok_or_else(|| anyhow::anyhow!("cannot resolve current executable directory"))?;
    let sibling = debug_dir.join(name);
    if sibling.exists() {
        let mut command = Command::new(sibling);
        command.stdin(Stdio::null());
        return Ok(command);
    }

    let mut command = Command::new(crate::paths::cargo_bin());
    let package = package_for_binary(name);
    if package == name {
        command.args(["run", "-p", name, "--"]);
    } else {
        command.args(["run", "-p", package, "--bin", name, "--"]);
    }
    command.stdin(Stdio::null());
    Ok(command)
}

fn package_for_binary(name: &str) -> &str {
    match name {
        "danbooru-normalize" => "sauce-indexer",
        _ => name,
    }
}

pub async fn monitor(mut children: Vec<ManagedChild>) -> anyhow::Result<()> {
    let logger = RunLogger::open().ok();
    if let Some(logger) = &logger {
        logger.write_line("saucedust", "foreground monitor started");
        if let Ok(path) = run_log_path() {
            crate::ui::info(&format!("실행 로그 기록 중: {}", path.display()));
        }
        for child in &children {
            let pid = child
                .child
                .id()
                .map(|pid| pid.to_string())
                .unwrap_or_else(|| "unknown".to_owned());
            logger.write_line(
                "saucedust",
                &format!("child started: {} pid={pid}", child.name),
            );
        }
    }

    if std::io::stdout().is_terminal() && !env_bool("SAUCEDUST_PLAIN_LOGS", false) {
        return monitor_tui(children, logger).await;
    }

    let status_enabled = std::io::stdout().is_terminal();
    let pids = children
        .iter()
        .filter_map(|child| child.child.id())
        .collect::<Vec<_>>();
    let status = status_enabled.then(|| Arc::new(Mutex::new(StatusBar::new(pids))));
    let mut readers = Vec::new();
    for child in children.iter_mut() {
        spawn_plain_child_readers(child, &mut readers, status.clone(), logger.clone());
    }
    let status_task = status.clone().map(spawn_status);

    let exited = loop {
        tokio::select! {
            _ = signal::ctrl_c() => {
                log_runtime_event(&logger, "received Ctrl-C, stopping children");
                stop_children(&mut children).await;
                break None;
            }
            _ = terminate() => {
                log_runtime_event(&logger, "received SIGTERM, stopping children");
                stop_children(&mut children).await;
                break None;
            }
            result = wait_any_child(&mut children) => {
                let (index, name, status_code) = result?;
                log_runtime_event(&logger, &format!("child exited: {name} {status_code}"));
                if let Some(status) = &status {
                    if let Ok(mut status_bar) = status.lock() {
                        status_bar.clear_area();
                        status_bar.observe_line(&format!("프로세스 종료 감지: {name} {status_code}"));
                    }
                }
                let delay = children[index].restart_delay();
                log_runtime_event(&logger, &format!("child restart scheduled: {name} after {}s", delay.as_secs()));
                sleep(delay).await;
                match children[index].restart() {
                    Ok(()) => {
                        log_runtime_event(&logger, &format!("child restarted: {name}"));
                        spawn_plain_child_readers(&mut children[index], &mut readers, status.clone(), logger.clone());
                        let pids = live_child_pids(&children);
                        if let Some(status) = &status {
                            if let Ok(mut status_bar) = status.lock() {
                                status_bar.pids = pids;
                                status_bar.observe_line(&format!("프로세스 재시작 완료: {name}"));
                            }
                        }
                    }
                    Err(error) => {
                        log_runtime_event(&logger, &format!("child restart failed: {name} {error}"));
                        stop_children(&mut children).await;
                        break Some((name, status_code));
                    }
                }
            }
        }
    };

    finish_status(status, status_task, readers).await;
    if let Some((name, status)) = exited {
        anyhow::bail!("{name} exited: {status}");
    }
    log_runtime_event(&logger, "foreground monitor stopped");
    Ok(())
}

async fn monitor_tui(
    mut children: Vec<ManagedChild>,
    logger: Option<RunLogger>,
) -> anyhow::Result<()> {
    let pids = children
        .iter()
        .filter_map(|child| child.child.id())
        .collect::<Vec<_>>();
    let dashboard = Arc::new(Mutex::new(DashboardState::new(pids)));
    let mut readers = Vec::new();
    for child in children.iter_mut() {
        spawn_tui_child_readers(child, &mut readers, dashboard.clone(), logger.clone());
    }

    let mut terminal = setup_terminal()?;
    let mut ticker = interval(Duration::from_millis(250));
    let mut stats_tick = 0usize;
    let mut storage_tick = 119usize;
    let exited = loop {
        tokio::select! {
            _ = signal::ctrl_c() => {
                log_runtime_event(&logger, "received Ctrl-C, stopping children");
                stop_children(&mut children).await;
                break None;
            }
            _ = terminate() => {
                log_runtime_event(&logger, "received SIGTERM, stopping children");
                stop_children(&mut children).await;
                break None;
            }
            result = wait_any_child(&mut children) => {
                let (index, name, status) = result?;
                log_runtime_event(&logger, &format!("child exited: {name} {status}"));
                if let Ok(mut dashboard) = dashboard.lock() {
                    dashboard.observe_line("saucedust", &format!("프로세스 종료 감지: {name} {status}"));
                }
                let delay = children[index].restart_delay();
                log_runtime_event(&logger, &format!("child restart scheduled: {name} after {}s", delay.as_secs()));
                sleep(delay).await;
                match children[index].restart() {
                    Ok(()) => {
                        log_runtime_event(&logger, &format!("child restarted: {name}"));
                        spawn_tui_child_readers(&mut children[index], &mut readers, dashboard.clone(), logger.clone());
                        let pids = live_child_pids(&children);
                        if let Ok(mut dashboard) = dashboard.lock() {
                            dashboard.pids = pids;
                            dashboard.observe_line("saucedust", &format!("프로세스 재시작 완료: {name}"));
                        }
                    }
                    Err(error) => {
                        log_runtime_event(&logger, &format!("child restart failed: {name} {error}"));
                        stop_children(&mut children).await;
                        break Some((name, status));
                    }
                }
            }
            _ = ticker.tick() => {
                if handle_tui_input(&dashboard)? {
                    stop_children(&mut children).await;
                    break None;
                }
                stats_tick = stats_tick.saturating_add(1);
                if stats_tick.is_multiple_of(4) {
                    storage_tick = storage_tick.saturating_add(1);
                    let pids = dashboard
                        .lock()
                        .map(|dashboard| dashboard.pids.clone())
                        .unwrap_or_default();
                    let compute = compute_snapshot(&pids).await;
                    let storage = if storage_tick >= 120 {
                        storage_tick = 0;
                        Some(storage_summary().await)
                    } else {
                        None
                    };
                    if let Ok(mut dashboard) = dashboard.lock() {
                        dashboard.compute = compute;
                        if let Some(storage) = storage {
                            dashboard.storage = storage;
                        }
                    }
                }
                if let Ok(mut dashboard) = dashboard.lock() {
                    dashboard.tick = dashboard.tick.saturating_add(1);
                    dashboard.expire_important();
                    draw_dashboard(&mut terminal, &dashboard)?;
                }
            }
        }
    };

    restore_terminal(&mut terminal)?;
    for reader in readers {
        reader.abort();
        let _ = reader.await;
    }
    if let Some((name, status)) = exited {
        anyhow::bail!("{name} exited: {status}");
    }
    log_runtime_event(&logger, "foreground TUI monitor stopped");
    Ok(())
}

type DashboardTerminal = Terminal<CrosstermBackend<std::io::Stdout>>;

fn setup_terminal() -> anyhow::Result<DashboardTerminal> {
    enable_raw_mode()?;
    let mut output = stdout();
    execute!(output, EnterAlternateScreen, EnableMouseCapture)?;
    let backend = CrosstermBackend::new(output);
    let mut terminal = Terminal::new(backend)?;
    terminal.clear()?;
    Ok(terminal)
}

fn restore_terminal(terminal: &mut DashboardTerminal) -> anyhow::Result<()> {
    disable_raw_mode()?;
    execute!(
        terminal.backend_mut(),
        DisableMouseCapture,
        LeaveAlternateScreen
    )?;
    terminal.show_cursor()?;
    Ok(())
}

fn handle_tui_input(dashboard: &Arc<Mutex<DashboardState>>) -> anyhow::Result<bool> {
    let mut quit = false;
    while event::poll(Duration::from_millis(0))? {
        match event::read()? {
            Event::Key(key) => {
                if key.kind != KeyEventKind::Press {
                    continue;
                }
                match key.code {
                    KeyCode::Char('q') | KeyCode::Esc => quit = true,
                    KeyCode::Up | KeyCode::Char('k') => {
                        if let Ok(mut dashboard) = dashboard.lock() {
                            dashboard.scroll_logs_up(1);
                        }
                    }
                    KeyCode::Down | KeyCode::Char('j') => {
                        if let Ok(mut dashboard) = dashboard.lock() {
                            dashboard.scroll_logs_down(1);
                        }
                    }
                    KeyCode::PageUp => {
                        if let Ok(mut dashboard) = dashboard.lock() {
                            dashboard.scroll_logs_up(10);
                        }
                    }
                    KeyCode::PageDown => {
                        if let Ok(mut dashboard) = dashboard.lock() {
                            dashboard.scroll_logs_down(10);
                        }
                    }
                    KeyCode::Home => {
                        if let Ok(mut dashboard) = dashboard.lock() {
                            dashboard.scroll_logs_top();
                        }
                    }
                    KeyCode::End => {
                        if let Ok(mut dashboard) = dashboard.lock() {
                            dashboard.scroll_logs_bottom();
                        }
                    }
                    _ => {}
                }
            }
            Event::Mouse(mouse) => match mouse.kind {
                MouseEventKind::ScrollUp => {
                    if let Ok(mut dashboard) = dashboard.lock() {
                        dashboard.scroll_logs_up(3);
                    }
                }
                MouseEventKind::ScrollDown => {
                    if let Ok(mut dashboard) = dashboard.lock() {
                        dashboard.scroll_logs_down(3);
                    }
                }
                _ => {}
            },
            _ => {}
        }
    }
    Ok(quit)
}

fn spawn_tui_child_readers(
    child: &mut ManagedChild,
    readers: &mut Vec<tokio::task::JoinHandle<()>>,
    dashboard: Arc<Mutex<DashboardState>>,
    logger: Option<RunLogger>,
) {
    if let Some(stdout) = child.child.stdout.take() {
        readers.push(spawn_tui_reader(
            stdout,
            child.name.clone(),
            dashboard.clone(),
            logger.clone(),
        ));
    }
    if let Some(stderr) = child.child.stderr.take() {
        readers.push(spawn_tui_reader(
            stderr,
            child.name.clone(),
            dashboard,
            logger,
        ));
    }
}

fn spawn_tui_reader<R>(
    stream: R,
    source: String,
    dashboard: Arc<Mutex<DashboardState>>,
    logger: Option<RunLogger>,
) -> tokio::task::JoinHandle<()>
where
    R: AsyncRead + Unpin + Send + 'static,
{
    tokio::spawn(async move {
        let mut lines = BufReader::new(stream).lines();
        while let Ok(Some(line)) = lines.next_line().await {
            if let Some(logger) = &logger {
                logger.write_line(&source, &line);
            }
            if let Ok(mut dashboard) = dashboard.lock() {
                dashboard.observe_line(&source, &line);
            }
        }
    })
}

fn draw_dashboard(
    terminal: &mut DashboardTerminal,
    dashboard: &DashboardState,
) -> anyhow::Result<()> {
    terminal.draw(|frame| {
        let area = frame.area();
        if area.height < 18 {
            let chunks = Layout::default()
                .direction(Direction::Vertical)
                .constraints([
                    Constraint::Length(3),
                    Constraint::Length(4),
                    Constraint::Min(3),
                    Constraint::Length(1),
                ])
                .split(area);

            frame.render_widget(header_widget(dashboard), chunks[0]);
            frame.render_widget(metrics_widget(dashboard), chunks[1]);
            frame.render_widget(logs_widget(dashboard, chunks[2].height), chunks[2]);
            frame.render_widget(footer_widget(), chunks[3]);
            return;
        }

        if area.height < 24 {
            let chunks = Layout::default()
                .direction(Direction::Vertical)
                .constraints([
                    Constraint::Length(3),
                    Constraint::Length(4),
                    Constraint::Length(4),
                    Constraint::Min(4),
                    Constraint::Length(1),
                ])
                .split(area);

            frame.render_widget(header_widget(dashboard), chunks[0]);
            frame.render_widget(metrics_widget(dashboard), chunks[1]);
            frame.render_widget(events_widget(dashboard), chunks[2]);
            frame.render_widget(logs_widget(dashboard, chunks[3].height), chunks[3]);
            frame.render_widget(footer_widget(), chunks[4]);
            return;
        }

        let chunks = Layout::default()
            .direction(Direction::Vertical)
            .constraints([
                Constraint::Length(3),
                Constraint::Length(5),
                Constraint::Length(5),
                Constraint::Length(6),
                Constraint::Min(8),
                Constraint::Length(1),
            ])
            .split(area);

        frame.render_widget(header_widget(dashboard), chunks[0]);
        frame.render_widget(metrics_widget(dashboard), chunks[1]);
        frame.render_widget(pipeline_widget(dashboard), chunks[2]);
        frame.render_widget(events_widget(dashboard), chunks[3]);
        frame.render_widget(logs_widget(dashboard, chunks[4].height), chunks[4]);
        frame.render_widget(footer_widget(), chunks[5]);
    })?;
    Ok(())
}

fn header_widget(dashboard: &DashboardState) -> Paragraph<'static> {
    let title = Line::from(vec![
        Span::styled(
            "saucedust",
            Style::default()
                .fg(Color::Cyan)
                .add_modifier(Modifier::BOLD),
        ),
        Span::raw("  "),
        Span::styled("실행 중", Style::default().fg(Color::Green)),
        Span::raw(format!("  {}", tui_moving_bar(dashboard.tick))),
    ]);
    Paragraph::new(title).block(Block::default().borders(Borders::ALL).title("상태"))
}

fn metrics_widget(dashboard: &DashboardState) -> Paragraph<'static> {
    Paragraph::new(storage_lines(&dashboard.storage).join("\n"))
        .block(Block::default().borders(Borders::ALL).title("저장 현황"))
        .wrap(Wrap { trim: false })
}

fn pipeline_widget(dashboard: &DashboardState) -> Paragraph<'static> {
    let text = format!(
        "CPU {:.0}% | MEM {}\n최근 로그 {}개 | 중요 이벤트 {}개",
        dashboard.compute.cpu_percent,
        format_bytes_compact(dashboard.compute.memory_bytes),
        dashboard.logs.len(),
        dashboard.important.len()
    );
    Paragraph::new(text)
        .block(
            Block::default()
                .borders(Borders::ALL)
                .title("컴퓨트/파이프라인"),
        )
        .wrap(Wrap { trim: false })
}

fn events_widget(dashboard: &DashboardState) -> Paragraph<'static> {
    let text = dashboard
        .important
        .iter()
        .rev()
        .take(4)
        .map(|event| event.text.clone())
        .collect::<Vec<_>>()
        .join("\n");
    Paragraph::new(text)
        .block(Block::default().borders(Borders::ALL).title("중요 이벤트"))
        .wrap(Wrap { trim: false })
}

fn logs_widget(dashboard: &DashboardState, height: u16) -> Paragraph<'static> {
    let visible = usize::from(height.saturating_sub(2)).max(1);
    let total = dashboard.logs.len();
    let scroll = dashboard
        .log_scroll_from_bottom
        .min(total.saturating_sub(1));
    let end = total.saturating_sub(scroll);
    let start = end.saturating_sub(visible);
    let text = dashboard
        .logs
        .iter()
        .skip(start)
        .take(end.saturating_sub(start))
        .cloned()
        .collect::<Vec<_>>()
        .join("\n");
    let title = if scroll == 0 {
        "로그".to_owned()
    } else {
        format!("로그 - 최신에서 {scroll}줄 위")
    };
    Paragraph::new(text)
        .block(Block::default().borders(Borders::ALL).title(title))
        .wrap(Wrap { trim: false })
}

fn footer_widget() -> Paragraph<'static> {
    Paragraph::new(
        "↑/↓/PgUp/PgDn/휠: 로그 스크롤 | Home/End: 처음/최신 | q/Esc: 종료 | 일반 로그: SAUCEDUST_PLAIN_LOGS=1",
    )
    .style(Style::default().fg(Color::DarkGray))
    .wrap(Wrap { trim: false })
}

struct DashboardState {
    pids: Vec<u32>,
    tick: usize,
    storage: String,
    compute: ComputeSnapshot,
    logs: VecDeque<String>,
    important: VecDeque<ImportantMessage>,
    log_scroll_from_bottom: usize,
}

impl DashboardState {
    fn new(pids: Vec<u32>) -> Self {
        Self {
            pids,
            tick: 0,
            storage: "저장 확인 중".to_owned(),
            compute: ComputeSnapshot::default(),
            logs: VecDeque::with_capacity(500),
            important: VecDeque::with_capacity(32),
            log_scroll_from_bottom: 0,
        }
    }

    fn observe_line(&mut self, source: &str, line: &str) {
        let line = compact_log_line(source, line);
        let was_scrolled = self.log_scroll_from_bottom > 0;
        push_bounded(&mut self.logs, line.clone(), 500);
        if was_scrolled {
            self.log_scroll_from_bottom = self
                .log_scroll_from_bottom
                .saturating_add(1)
                .min(self.max_log_scroll());
        }
        if is_important_line(&line) {
            push_bounded(
                &mut self.important,
                ImportantMessage {
                    text: compact_important_line(&line),
                    seen_at: Instant::now(),
                },
                32,
            );
        }
        self.expire_important();
    }

    fn expire_important(&mut self) {
        while self
            .important
            .front()
            .is_some_and(|message| message.seen_at.elapsed() > Duration::from_secs(300))
        {
            self.important.pop_front();
        }
    }

    fn scroll_logs_up(&mut self, amount: usize) {
        self.log_scroll_from_bottom = self
            .log_scroll_from_bottom
            .saturating_add(amount)
            .min(self.max_log_scroll());
    }

    fn scroll_logs_down(&mut self, amount: usize) {
        self.log_scroll_from_bottom = self.log_scroll_from_bottom.saturating_sub(amount);
    }

    fn scroll_logs_top(&mut self) {
        self.log_scroll_from_bottom = self.max_log_scroll();
    }

    fn scroll_logs_bottom(&mut self) {
        self.log_scroll_from_bottom = 0;
    }

    fn max_log_scroll(&self) -> usize {
        self.logs.len().saturating_sub(1)
    }
}

fn compact_log_line(source: &str, line: &str) -> String {
    let line = line.trim();
    let max_width = terminal_width().clamp(120, 220).saturating_add(120);
    truncate_visible_width(&format!("{source}: {line}"), max_width)
}

fn push_bounded<T>(items: &mut VecDeque<T>, item: T, capacity: usize) {
    while items.len() >= capacity {
        items.pop_front();
    }
    items.push_back(item);
}

fn tui_moving_bar(tick: usize) -> String {
    let width = 18;
    let pos = tick % width;
    let mut chars = vec!["░"; width];
    chars[pos] = "█";
    if pos + 1 < width {
        chars[pos + 1] = "▓";
    }
    format!("[{}]", chars.join(""))
}

fn live_child_pids(children: &[ManagedChild]) -> Vec<u32> {
    children
        .iter()
        .filter_map(|child| child.child.id())
        .collect()
}

async fn finish_status(
    status: Option<Arc<Mutex<StatusBar>>>,
    status_task: Option<tokio::task::JoinHandle<()>>,
    readers: Vec<tokio::task::JoinHandle<()>>,
) {
    if let Some(status) = status {
        if let Ok(mut status) = status.lock() {
            status.clear_area();
            status.active = false;
        } else {
            crate::ui::clear_line();
        }
    }
    if let Some(task) = status_task {
        task.abort();
        let _ = task.await;
    }
    for reader in readers {
        reader.abort();
        let _ = reader.await;
    }
}

fn spawn_plain_child_readers(
    child: &mut ManagedChild,
    readers: &mut Vec<tokio::task::JoinHandle<()>>,
    status: Option<Arc<Mutex<StatusBar>>>,
    logger: Option<RunLogger>,
) {
    if let Some(stdout) = child.child.stdout.take() {
        readers.push(spawn_reader(
            stdout,
            child.name.clone(),
            "stdout",
            status.clone(),
            logger.clone(),
        ));
    }
    if let Some(stderr) = child.child.stderr.take() {
        readers.push(spawn_reader(
            stderr,
            child.name.clone(),
            "stderr",
            status,
            logger,
        ));
    }
}

fn spawn_reader<R>(
    stream: R,
    source: String,
    stream_name: &'static str,
    status: Option<Arc<Mutex<StatusBar>>>,
    logger: Option<RunLogger>,
) -> tokio::task::JoinHandle<()>
where
    R: AsyncRead + Unpin + Send + 'static,
{
    tokio::spawn(async move {
        let mut lines = BufReader::new(stream).lines();
        while let Ok(Some(line)) = lines.next_line().await {
            if let Some(logger) = &logger {
                logger.write_line(&format!("{source}:{stream_name}"), &line);
            }
            if let Some(status) = &status {
                if let Ok(mut status) = status.lock() {
                    status.clear_area();
                    status.observe_line(&line);
                } else {
                    crate::ui::clear_line();
                }
                println!("{line}");
                redraw_status(status);
            } else {
                println!("{line}");
            }
        }
    })
}

#[derive(Clone)]
pub struct RunLogger {
    file: Arc<Mutex<File>>,
}

impl RunLogger {
    pub fn open() -> anyhow::Result<Self> {
        let path = run_log_path()?;
        if let Some(parent) = path.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let file = OpenOptions::new().create(true).append(true).open(&path)?;
        Ok(Self {
            file: Arc::new(Mutex::new(file)),
        })
    }

    fn write_line(&self, source: &str, line: &str) {
        let timestamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|duration| duration.as_secs().to_string())
            .unwrap_or_else(|_| "0".to_owned());
        if let Ok(mut file) = self.file.lock() {
            let _ = writeln!(file, "{timestamp} [{source}] {line}");
            let _ = file.flush();
        }
    }
}

pub fn append_run_log_line(source: &str, line: &str) {
    if let Ok(logger) = RunLogger::open() {
        logger.write_line(source, line);
    }
}

pub fn run_log_path() -> anyhow::Result<PathBuf> {
    let root = crate::paths::workspace_root()?;
    let log_dir = root.join("logs");
    std::fs::create_dir_all(&log_dir)?;
    if let Ok(path) = env::var("SAUCEDUST_RUN_LOG_PATH") {
        let path = path.trim();
        if !path.is_empty() {
            return Ok(PathBuf::from(path));
        }
    }
    let path = next_run_log_path(&log_dir)?;
    env::set_var("SAUCEDUST_RUN_LOG_PATH", &path);
    let latest = log_dir.join("saucedust.latest.log");
    let _ = std::fs::remove_file(&latest);
    let _ = std::os::unix::fs::symlink(&path, &latest)
        .or_else(|_| std::fs::write(&latest, format!("{}\n", path.display())));
    Ok(path)
}

pub fn latest_log_path() -> anyhow::Result<PathBuf> {
    Ok(crate::paths::workspace_root()?
        .join("logs")
        .join("saucedust.latest.log"))
}

pub fn prepare_run_log_env() -> anyhow::Result<PathBuf> {
    let path = run_log_path()?;
    env::set_var("SAUCEDUST_RUN_LOG_PATH", &path);
    Ok(path)
}

fn next_run_log_path(log_dir: &std::path::Path) -> anyhow::Result<PathBuf> {
    let stamp = Local::now().format("%Y-%m-%d_%H-%M-%S").to_string();
    for sequence in 1..=9999 {
        let candidate = log_dir.join(format!("saucedust_{stamp}_{sequence:04}.log"));
        if !candidate.exists() {
            return Ok(candidate);
        }
    }
    anyhow::bail!("cannot allocate saucedust run log path")
}

fn log_runtime_event(logger: &Option<RunLogger>, line: &str) {
    if let Some(logger) = logger {
        logger.write_line("saucedust", line);
    }
}

fn spawn_status(status: Arc<Mutex<StatusBar>>) -> tokio::task::JoinHandle<()> {
    tokio::spawn(async move {
        let mut tick = 0usize;
        let mut storage_tick = 29usize;
        loop {
            let active = status.lock().map(|status| status.active).unwrap_or(false);
            if !active {
                break;
            }

            let pids = status
                .lock()
                .map(|status| status.pids.clone())
                .unwrap_or_default();
            let compute = compute_snapshot(&pids).await;
            storage_tick = storage_tick.saturating_add(1);
            let storage = if storage_tick >= 30 {
                storage_tick = 0;
                Some(storage_summary().await)
            } else {
                None
            };

            if let Ok(mut status) = status.lock() {
                if !status.active {
                    break;
                }
                status.clear_area();
                status.tick = status.tick.saturating_add(1);
                status.compute = compute;
                if let Some(storage) = storage {
                    status.storage = storage;
                }
                status.expire_important();
                status.redraw();
            }

            tick = tick.saturating_add(1);
            sleep(Duration::from_secs(1)).await;
        }
    })
}

fn redraw_status(status: &Arc<Mutex<StatusBar>>) {
    if let Ok(mut status) = status.lock() {
        if status.active {
            status.redraw();
        }
    }
}

struct StatusBar {
    pids: Vec<u32>,
    tick: usize,
    active: bool,
    storage: String,
    compute: ComputeSnapshot,
    important: Option<ImportantMessage>,
    rendered_lines: usize,
}

impl StatusBar {
    fn new(pids: Vec<u32>) -> Self {
        Self {
            pids,
            tick: 0,
            active: true,
            storage: "저장 확인 중".to_owned(),
            compute: ComputeSnapshot::default(),
            important: None,
            rendered_lines: 1,
        }
    }

    fn lines(&self) -> Vec<String> {
        let mut lines = Vec::new();
        if let Some(important) = &self.important {
            lines.extend(wrap_important_line(&important.text));
        }
        lines.push(format!(
            "{} saucedust 실행 중",
            crate::ui::moving_bar(self.tick)
        ));
        lines.extend(storage_lines(&self.storage));
        lines.push(format!(
            "CPU {:.0}% | MEM {}",
            self.compute.cpu_percent,
            format_bytes_compact(self.compute.memory_bytes)
        ));
        lines
    }

    fn clear_area(&self) {
        crate::ui::clear_lines(self.rendered_lines.max(1));
    }

    fn observe_line(&mut self, line: &str) {
        if is_important_line(line) {
            self.important = Some(ImportantMessage {
                text: compact_important_line(line),
                seen_at: Instant::now(),
            });
        }
        self.expire_important();
    }

    fn expire_important(&mut self) {
        if let Some(important) = &self.important {
            if important.seen_at.elapsed() > Duration::from_secs(300) {
                self.important = None;
            }
        }
    }

    fn redraw(&mut self) {
        let lines = self.lines();
        self.rendered_lines = lines.len().max(1);
        crate::ui::inline(&lines.join("\n"));
    }
}

struct ImportantMessage {
    text: String,
    seen_at: Instant,
}

#[derive(Default)]
struct ComputeSnapshot {
    cpu_percent: f64,
    memory_bytes: u64,
}

fn is_important_line(line: &str) -> bool {
    const PATTERNS: &[&str] = &[
        "VPN 서버 전환",
        "ExpressVPN",
        "프록시",
        "상태 전환",
        "재생성",
        "인증에 실패",
        "Too Many Requests",
        "429",
        "검색 API 준비 완료",
        "embedding-worker 준비 완료",
        "Qdrant 준비 완료",
        "PostgreSQL",
        "프로세스 시작",
        "exited",
        "실패",
        "오류",
    ];
    PATTERNS.iter().any(|pattern| line.contains(pattern))
}

fn compact_important_line(line: &str) -> String {
    let max_chars = terminal_width().clamp(120, 220).saturating_add(120);
    let mut text = line.trim().replace('\t', " ");
    while text.contains("  ") {
        text = text.replace("  ", " ");
    }
    if visible_len(&text) <= max_chars {
        return text;
    }
    truncate_visible_width(&text, max_chars)
}

fn storage_lines(storage: &str) -> Vec<String> {
    let fields = storage
        .split(" | ")
        .map(str::trim)
        .filter(|field| !field.is_empty())
        .collect::<Vec<_>>();
    if fields.is_empty() {
        return vec!["저장 확인 중".to_owned()];
    }

    let mut lines = Vec::new();
    let mut index = 0usize;
    while index < fields.len() {
        let line = if index + 1 < fields.len()
            && short_status_field(fields[index])
            && short_status_field(fields[index + 1])
        {
            let line = format!("{} | {}", fields[index], fields[index + 1]);
            index += 2;
            line
        } else {
            let line = fields[index].to_owned();
            index += 1;
            line
        };
        lines.extend(wrap_plain_line(&line));
    }
    lines
}

fn short_status_field(field: &str) -> bool {
    visible_len(field) <= status_wrap_width().saturating_div(2).max(24)
}

fn wrap_important_line(text: &str) -> Vec<String> {
    let first_width = status_wrap_width().saturating_sub(10).max(36);
    let chunks = wrap_plain_line_at(text, first_width);
    chunks
        .into_iter()
        .enumerate()
        .map(|(index, chunk)| {
            if index == 0 {
                crate::ui::important_line(&chunk)
            } else {
                format!("  {chunk}")
            }
        })
        .collect()
}

fn wrap_plain_line(line: &str) -> Vec<String> {
    wrap_plain_line_at(line, status_wrap_width())
}

fn wrap_plain_line_at(line: &str, width: usize) -> Vec<String> {
    if visible_len(line) <= width {
        return vec![line.to_owned()];
    }

    let mut lines = Vec::new();
    let mut current = String::new();
    let mut current_len = 0usize;
    for ch in line.chars() {
        let char_len = char_width(ch);
        if current_len > 0 && current_len.saturating_add(char_len) > width {
            lines.push(current);
            current = String::new();
            current_len = 0;
        }
        current.push(ch);
        current_len = current_len.saturating_add(char_len);
    }
    if !current.is_empty() {
        lines.push(current);
    }
    lines
}

fn terminal_width() -> usize {
    env::var("COLUMNS")
        .ok()
        .and_then(|value| value.parse::<usize>().ok())
        .filter(|value| *value >= 40)
        .unwrap_or(100)
}

fn status_wrap_width() -> usize {
    terminal_width().clamp(40, 78).saturating_sub(2)
}

fn visible_len(text: &str) -> usize {
    text.chars().map(char_width).sum()
}

fn truncate_visible_width(text: &str, width: usize) -> String {
    if visible_len(text) <= width {
        return text.to_owned();
    }

    let ellipsis = '…';
    let target = width.saturating_sub(char_width(ellipsis));
    let mut out = String::new();
    let mut used = 0usize;
    for ch in text.chars() {
        let char_len = char_width(ch);
        if used.saturating_add(char_len) > target {
            break;
        }
        out.push(ch);
        used = used.saturating_add(char_len);
    }
    out.push(ellipsis);
    out
}

fn char_width(ch: char) -> usize {
    if ch.is_ascii() {
        1
    } else {
        2
    }
}

async fn compute_snapshot(pids: &[u32]) -> ComputeSnapshot {
    if pids.is_empty() {
        return ComputeSnapshot::default();
    }

    let pid_list = pids
        .iter()
        .map(u32::to_string)
        .collect::<Vec<_>>()
        .join(",");
    let output = Command::new("ps")
        .args(["-o", "%cpu=", "-o", "rss=", "-p", &pid_list])
        .stdin(Stdio::null())
        .output()
        .await;
    let Ok(output) = output else {
        return ComputeSnapshot::default();
    };
    if !output.status.success() {
        return ComputeSnapshot::default();
    }

    let raw = String::from_utf8_lossy(&output.stdout);
    let mut cpu_percent = 0.0;
    let mut memory_kib = 0u64;
    for line in raw.lines() {
        let mut parts = line.split_whitespace();
        let cpu = parts
            .next()
            .and_then(|value| value.parse::<f64>().ok())
            .unwrap_or_default();
        let rss = parts
            .next()
            .and_then(|value| value.parse::<u64>().ok())
            .unwrap_or_default();
        cpu_percent += cpu;
        memory_kib = memory_kib.saturating_add(rss);
    }

    ComputeSnapshot {
        cpu_percent,
        memory_bytes: memory_kib.saturating_mul(1024),
    }
}

async fn storage_summary() -> String {
    match crate::stats::snapshot().await {
        Ok(snapshot) => {
            let images = snapshot
                .database
                .as_ref()
                .map(|database| crate::stats::format_count(database.image_count))
                .unwrap_or_else(|| "?".to_owned());
            let vectors = snapshot
                .database
                .as_ref()
                .and_then(|database| database.vector_count)
                .map(crate::stats::format_count)
                .unwrap_or_else(|| "?".to_owned());
            let latest = snapshot
                .database
                .as_ref()
                .and_then(|database| database.crawl.as_ref())
                .map(|crawl| crate::stats::format_count(crawl.catchup_saved))
                .unwrap_or_else(|| "?".to_owned());
            let backfill = snapshot
                .database
                .as_ref()
                .and_then(|database| database.crawl.as_ref())
                .map(|crawl| crate::stats::format_count(crawl.backfill_saved))
                .unwrap_or_else(|| "?".to_owned());
            format!(
                "저장 이미지 {images}장 | 벡터 {vectors}개 | 최신 {latest}장 | 과거 {backfill}장 | DB {}",
                format_bytes_compact(snapshot.total_disk_size())
            )
        }
        Err(_) => "저장 확인 실패".to_owned(),
    }
}

fn format_bytes_compact(bytes: u64) -> String {
    const UNITS: [&str; 5] = ["B", "KiB", "MiB", "GiB", "TiB"];
    let mut value = bytes as f64;
    let mut unit = 0usize;
    while value >= 1024.0 && unit < UNITS.len() - 1 {
        value /= 1024.0;
        unit += 1;
    }
    if unit == 0 {
        format!("{bytes} B")
    } else {
        format!("{value:.1} {}", UNITS[unit])
    }
}

fn env_bool(key: &str, default: bool) -> bool {
    env::var(key)
        .ok()
        .map(|value| {
            matches!(
                value.trim().to_ascii_lowercase().as_str(),
                "1" | "true" | "yes" | "on"
            )
        })
        .unwrap_or(default)
}

async fn wait_any_child(
    children: &mut [ManagedChild],
) -> anyhow::Result<(usize, String, ExitStatus)> {
    loop {
        for (index, child) in children.iter_mut().enumerate() {
            if let Some(status) = child.child.try_wait()? {
                return Ok((index, child.name.clone(), status));
            }
        }
        sleep(Duration::from_secs(1)).await;
    }
}

pub async fn stop_children(children: &mut [ManagedChild]) {
    for child in children.iter_mut().rev() {
        let _ = child.child.kill().await;
    }
}

#[cfg(unix)]
async fn terminate() {
    match signal::unix::signal(signal::unix::SignalKind::terminate()) {
        Ok(mut stream) => {
            stream.recv().await;
        }
        Err(_) => std::future::pending::<()>().await,
    }
}

#[cfg(not(unix))]
async fn terminate() {
    std::future::pending::<()>().await
}
