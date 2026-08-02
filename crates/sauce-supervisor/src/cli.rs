use clap::{Parser, Subcommand};

#[derive(Parser)]
#[command(name = "saucedust", version)]
pub struct Cli {
    #[arg(short = 'd', long = "daemon")]
    pub daemon: bool,
    #[command(subcommand)]
    pub command: Option<Commands>,
}

#[derive(Subcommand)]
pub enum Commands {
    Bootstrap {
        #[arg(long)]
        skip_python: bool,
    },
    Menu,
    Start {
        #[arg(long)]
        no_indexer: bool,
        #[arg(long)]
        no_telegram: bool,
        #[arg(long, default_value_t = 200)]
        index_limit: u32,
        #[arg(long, default_value_t = 60)]
        poll_secs: u64,
        #[arg(long)]
        backfill_workers: Option<usize>,
        #[arg(long)]
        backfill_range_size: Option<i64>,
    },
    Stop,
    Status,
    Service {
        #[command(subcommand)]
        command: ServiceCommand,
    },
    Serve {
        #[arg(long)]
        no_indexer: bool,
        #[arg(long)]
        no_telegram: bool,
        #[arg(long, default_value_t = 200)]
        index_limit: u32,
        #[arg(long, default_value_t = 60)]
        poll_secs: u64,
        #[arg(long)]
        backfill_workers: Option<usize>,
        #[arg(long)]
        backfill_range_size: Option<i64>,
    },
    DbUp,
    DbDown,
    Env {
        #[command(subcommand)]
        command: EnvCommand,
    },
    Index {
        #[command(subcommand)]
        command: IndexCommand,
    },
    Normalize {
        #[command(subcommand)]
        command: NormalizeCommand,
    },
    Vpn {
        #[command(subcommand)]
        command: VpnCommand,
    },
    Verify,
    Stats,
    ResetDev,
    Doctor,
    #[command(hide = true)]
    InternalApi,
    #[command(hide = true)]
    InternalTelegram,
    #[command(hide = true)]
    InternalIndexer {
        #[command(subcommand)]
        command: IndexCommand,
    },
}

#[derive(Subcommand)]
pub enum EnvCommand {
    Setup {
        #[arg(long)]
        advanced: bool,
    },
    Set {
        key: String,
        value: String,
    },
    Show {
        #[arg(long)]
        show_secrets: bool,
    },
}

#[derive(Subcommand)]
pub enum ServiceCommand {
    Install {
        #[arg(long)]
        no_indexer: bool,
        #[arg(long)]
        no_telegram: bool,
        #[arg(long, default_value_t = 200)]
        index_limit: u32,
        #[arg(long, default_value_t = 60)]
        poll_secs: u64,
        #[arg(long)]
        backfill_workers: Option<usize>,
        #[arg(long)]
        backfill_range_size: Option<i64>,
    },
    Start,
    Stop,
    Status,
    Uninstall,
}

#[derive(Subcommand)]
pub enum IndexCommand {
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
    CheckPost {
        #[arg(long, default_value = "danbooru")]
        source_site: String,
        #[arg(long)]
        post_id: String,
    },
}

#[derive(Subcommand)]
pub enum NormalizeCommand {
    Danbooru {
        #[arg(long)]
        tags: String,
        #[arg(long)]
        limit: u32,
        #[arg(long)]
        out: String,
    },
}

#[derive(Subcommand)]
pub enum VpnCommand {
    Setup,
    Start,
    Rotate,
    Stop,
    Status,
    Test,
}

#[cfg(test)]
mod tests {
    use super::*;
    use clap::Parser;

    #[test]
    fn parses_daemon_flag() {
        let cli = Cli::parse_from(["saucedust", "-d"]);
        assert!(cli.daemon);
        assert!(cli.command.is_none());
    }
}
