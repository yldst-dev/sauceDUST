use std::time::Duration;

use reqwest::multipart::{Form, Part};
use tokio::time::sleep;

use crate::errors::{SauceError, SauceResult};
use crate::models::EmbedResponse;

#[derive(Debug, Clone)]
pub struct EmbeddingClient {
    client: reqwest::Client,
    base_url: String,
}

impl EmbeddingClient {
    pub fn new(base_url: impl Into<String>, timeout: Duration) -> SauceResult<Self> {
        let client = reqwest::Client::builder().timeout(timeout).build()?;

        Ok(Self {
            client,
            base_url: base_url.into().trim_end_matches('/').to_owned(),
        })
    }

    pub async fn health(&self) -> bool {
        self.client
            .get(format!("{}/health", self.base_url))
            .send()
            .await
            .map(|response| response.status().is_success())
            .unwrap_or(false)
    }

    pub async fn embed_image(&self, bytes: Vec<u8>, filename: &str) -> SauceResult<EmbedResponse> {
        let mut delay = Duration::from_secs(1);
        let mut last_error = None;
        for attempt in 1..=3 {
            match self.embed_image_once(bytes.clone(), filename).await {
                Ok(response) => return Ok(response),
                Err(error) if attempt < 3 && is_transient_embedding_error(&error) => {
                    last_error = Some(error);
                    sleep(delay).await;
                    delay = delay.saturating_mul(2).min(Duration::from_secs(10));
                }
                Err(error) => return Err(error),
            }
        }
        Err(last_error
            .unwrap_or_else(|| SauceError::Embedding("embedding retry failed".to_owned())))
    }

    async fn embed_image_once(&self, bytes: Vec<u8>, filename: &str) -> SauceResult<EmbedResponse> {
        let part = Part::bytes(bytes)
            .file_name(filename.to_owned())
            .mime_str("application/octet-stream")
            .map_err(|err| SauceError::Embedding(err.to_string()))?;
        let form = Form::new().part("file", part);
        let response = self
            .client
            .post(format!("{}/embed", self.base_url))
            .multipart(form)
            .send()
            .await?;
        let status = response.status();

        if !status.is_success() {
            let body = response.text().await.unwrap_or_default();
            return Err(SauceError::Embedding(format!("{status}: {body}")));
        }

        Ok(response.json().await?)
    }
}

fn is_transient_embedding_error(error: &SauceError) -> bool {
    let message = error.to_string();
    message.contains("error sending request")
        || message.contains("error decoding response body")
        || message.contains("connection")
        || message.contains("timeout")
        || message.contains("502")
        || message.contains("503")
        || message.contains("504")
}
