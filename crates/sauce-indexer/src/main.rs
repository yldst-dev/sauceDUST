use clap::{Parser, Subcommand};
use sauce_indexer::runner::{self, DanbooruOptions};
use tracing_subscriber::EnvFilter;

#[derive(Parser)]
#[command(name = "sauce-indexer")]
struct Cli {
    #[command(subcommand)]
    command: Commands,
}

#[derive(Subcommand)]
enum Commands {
    Danbooru {
        #[arg(long)]
        limit: Option<u32>,
        #[arg(long)]
        dry_run: bool,
        #[arg(long)]
        continuous: bool,
        #[arg(long, default_value_t = 60)]
        poll_secs: u64,
        #[arg(long)]
        backfill_workers: Option<usize>,
        #[arg(long)]
        backfill_range_size: Option<i64>,
    },
    Verify,
    ResetDev,
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(EnvFilter::from_default_env())
        .init();

    let cli = Cli::parse();

    match cli.command {
        Commands::Danbooru {
            limit,
            dry_run,
            continuous,
            poll_secs,
            backfill_workers,
            backfill_range_size,
        } => {
            runner::run_danbooru(DanbooruOptions {
                limit,
                dry_run,
                continuous,
                poll_secs,
                backfill_workers,
                backfill_range_size,
            })
            .await?
        }
        Commands::Verify => runner::verify().await?,
        Commands::ResetDev => runner::reset_dev().await?,
    }

    Ok(())
}
