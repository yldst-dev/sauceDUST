use std::io::{self, Write};

const RESET: &str = "\x1b[0m";
const BOLD: &str = "\x1b[1m";
const DIM: &str = "\x1b[2m";
const GREEN: &str = "\x1b[32m";
const YELLOW: &str = "\x1b[33m";
const BLUE: &str = "\x1b[34m";
const CYAN: &str = "\x1b[36m";
const RED: &str = "\x1b[31m";
const CLEAR_LINE: &str = "\r\x1b[2K";

pub fn title(text: &str) {
    println!();
    println!("{BOLD}{CYAN}{text}{RESET}");
    println!("{DIM}{}{}", "─".repeat(visible_len(text).max(12)), RESET);
}

pub fn step(current: usize, total: usize, text: &str) {
    println!();
    println!(
        "{} {} {BOLD}{text}{RESET}",
        progress(current, total),
        format!("[{current}/{total}]").as_str()
    );
}

pub fn ok(text: &str) {
    println!("{GREEN}✓{RESET} {text}");
}

pub fn info(text: &str) {
    println!("{BLUE}•{RESET} {text}");
}

pub fn warn(text: &str) {
    println!("{YELLOW}!{RESET} {text}");
}

pub fn command(text: &str) {
    println!("  {DIM}${RESET} {BOLD}{text}{RESET}");
}

pub fn task_ok(text: &str, seconds: u64) {
    println!(
        "{} {GREEN}완료{RESET} {text} {DIM}{seconds}s{RESET}",
        progress(1, 1)
    );
}

pub fn task_fail(text: &str, seconds: u64) {
    println!(
        "{} {RED}실패{RESET} {text} {DIM}{seconds}s{RESET}",
        progress(1, 1)
    );
}

pub fn log_line(stream: &str, line: &str) {
    let color = if stream == "stderr" { YELLOW } else { DIM };
    for (index, chunk) in wrap_line(line, terminal_width().saturating_sub(18))
        .into_iter()
        .enumerate()
    {
        if index == 0 {
            println!("  {color}│{RESET} {DIM}{stream}{RESET} {chunk}");
        } else {
            println!(
                "  {color}│{RESET} {DIM}{}{RESET} {chunk}",
                " ".repeat(stream.len())
            );
        }
    }
}

pub fn live_line(text: &str, tick: usize, seconds: u64) -> String {
    format!("{} {DIM}{text} 진행 중... {seconds}s{RESET}", moving(tick))
}

pub fn moving_bar(tick: usize) -> String {
    moving(tick)
}

pub fn clear_line() {
    print!("{CLEAR_LINE}");
    flush();
}

pub fn clear_lines(count: usize) {
    for index in 0..count {
        if index > 0 {
            print!("\x1b[1A");
        }
        print!("{CLEAR_LINE}");
    }
    flush();
}

pub fn inline(text: &str) {
    print!("{CLEAR_LINE}{text}");
    flush();
}

pub fn important_line(text: &str) -> String {
    format!("{YELLOW}{BOLD}! 중요{RESET} {text}")
}

pub fn failure_box(label: &str, lines: &[String]) {
    println!("{RED}╭─ 오류 로그: {label}{RESET}");
    if lines.is_empty() {
        println!("{RED}│{RESET} 로그가 없습니다.");
    } else {
        for line in lines {
            for chunk in wrap_line(line, terminal_width().saturating_sub(4)) {
                println!("{RED}│{RESET} {chunk}");
            }
        }
    }
    println!("{RED}╰────────────────────────────{RESET}");
}

pub fn flush() {
    let _ = io::stdout().flush();
}

fn progress(current: usize, total: usize) -> String {
    let width = 18;
    let filled = if total == 0 {
        0
    } else {
        width * current.min(total) / total
    };
    format!(
        "{CYAN}[{}{}]{RESET}",
        "█".repeat(filled),
        "░".repeat(width - filled)
    )
}

fn moving(tick: usize) -> String {
    let width = 18;
    let pos = tick % width;
    let mut chars = vec!["░"; width];
    chars[pos] = "█";
    if pos + 1 < width {
        chars[pos + 1] = "▓";
    }
    format!("{CYAN}[{}]{RESET}", chars.join(""))
}

fn wrap_line(line: &str, width: usize) -> Vec<String> {
    let width = width.max(20);
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
    std::env::var("COLUMNS")
        .ok()
        .and_then(|value| value.parse::<usize>().ok())
        .filter(|value| *value >= 40)
        .unwrap_or(100)
}

fn visible_len(text: &str) -> usize {
    text.chars().map(char_width).sum()
}

fn char_width(ch: char) -> usize {
    if ch.is_ascii() {
        1
    } else {
        2
    }
}
