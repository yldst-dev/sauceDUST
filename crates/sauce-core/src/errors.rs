use thiserror::Error;

#[derive(Debug, Error)]
pub enum SauceError {
    #[error("configuration error: {0}")]
    Config(String),
    #[error("http error: {0}")]
    Http(#[from] reqwest::Error),
    #[error("image error: {0}")]
    Image(#[from] image::ImageError),
    #[error("embedding worker error: {0}")]
    Embedding(String),
    #[error("database error: {0}")]
    Database(String),
    #[error("vector database error: {0}")]
    Vector(String),
    #[error("invalid input: {0}")]
    InvalidInput(String),
}

pub type SauceResult<T> = Result<T, SauceError>;
