pub mod embedding_client;
pub mod routes;
pub mod state;

use std::net::SocketAddr;

use anyhow::Context;
use embedding_client::EmbeddingClient;
use sauce_core::config::AppConfig;
use sauce_db::postgres::PostgresRepo;
use sauce_db::qdrant::QdrantRepo;
use state::AppState;
use tokio::net::TcpListener;
use tower_http::cors::CorsLayer;
use tower_http::trace::TraceLayer;

pub async fn run() -> anyhow::Result<()> {
    let config = AppConfig::from_env()?;
    let postgres = PostgresRepo::connect(&config.database_url).await?;
    postgres.apply_migrations().await?;
    let qdrant = QdrantRepo::new(&config.qdrant_url, "images", config.vector_size)?;
    qdrant.ensure_collection().await?;
    let embedding = EmbeddingClient::new(&config.embedding_worker_url, config.request_timeout)?;
    let state = AppState {
        postgres,
        qdrant,
        embedding,
    };
    let app = routes::router(state)
        .layer(CorsLayer::permissive())
        .layer(TraceLayer::new_for_http());
    let bind: SocketAddr = config
        .api_bind
        .parse()
        .with_context(|| format!("invalid SAUCE_API_BIND: {}", config.api_bind))?;
    let listener = TcpListener::bind(bind).await?;

    tracing::info!("sauce-api listening on {}", bind);
    axum::serve(listener, app).await?;

    Ok(())
}
