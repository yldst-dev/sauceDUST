mod postgres;
mod qdrant;

use crate::process::ManagedChild;

pub async fn up() -> anyhow::Result<ManagedChild> {
    postgres::start().await?;
    ManagedChild::spawn("qdrant", qdrant::command()?)
}

pub async fn down() -> anyhow::Result<()> {
    postgres::stop().await
}

pub async fn bootstrap() -> anyhow::Result<()> {
    qdrant::ensure_binary().await?;
    postgres::start().await
}

pub async fn start_postgres() -> anyhow::Result<()> {
    postgres::start().await
}
