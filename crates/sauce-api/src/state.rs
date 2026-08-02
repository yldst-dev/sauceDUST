use sauce_core::embedding::EmbeddingClient;
use sauce_db::postgres::PostgresRepo;
use sauce_db::qdrant::QdrantRepo;

#[derive(Clone)]
pub struct AppState {
    pub postgres: PostgresRepo,
    pub qdrant: QdrantRepo,
    pub embedding: EmbeddingClient,
}
