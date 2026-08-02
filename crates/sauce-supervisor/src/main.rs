mod app;
mod auto;
mod bootstrap;
mod cli;
mod command;
mod db;
mod doctor;
mod env_config;
mod menu;
mod paths;
mod process;
mod python;
mod runtime_assets;
mod service;
mod stats;
mod system_setup;
mod ui;
mod vpn;
mod wait;

use clap::Parser;
use tracing_subscriber::EnvFilter;

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    paths::augment_runtime_path();
    dotenvy::dotenv().ok();
    std::panic::set_hook(Box::new(|info| {
        crate::process::append_run_log_line("panic", &info.to_string());
    }));
    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::from_default_env())
        .init();

    let result = app::run(cli::Cli::parse()).await;
    if let Err(error) = &result {
        crate::process::append_run_log_line("saucedust", &format!("exited with error: {error:#}"));
    }
    result
}
