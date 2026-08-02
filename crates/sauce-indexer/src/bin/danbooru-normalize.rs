use std::path::PathBuf;

use clap::Parser;
use sauce_indexer::runner::{self, NormalizeOptions};
use tracing_subscriber::EnvFilter;

#[derive(Parser)]
#[command(name = "danbooru-normalize")]
struct Cli {
    #[arg(long, default_value = "mari_(blue_archive) rating:safe")]
    tags: String,
    #[arg(long, default_value_t = 200, value_parser = clap::value_parser!(u32).range(1..=200))]
    limit: u32,
    #[arg(long)]
    out: PathBuf,
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::from_default_env())
        .init();

    let cli = Cli::parse();
    runner::normalize_danbooru(NormalizeOptions {
        tags: cli.tags,
        limit: cli.limit,
        out: cli.out,
    })
    .await
}
