use std::time::Duration;

use reqwest::multipart::{Form, Part};
use sauce_core::models::SearchResponse;

#[derive(Clone)]
pub struct SauceApiClient {
    client: reqwest::Client,
    base_url: String,
}

impl SauceApiClient {
    pub fn new(base_url: &str, timeout: Duration) -> anyhow::Result<Self> {
        let client = reqwest::Client::builder().timeout(timeout).build()?;
        Ok(Self {
            client,
            base_url: base_url.trim_end_matches('/').to_owned(),
        })
    }

    pub async fn search_image(&self, bytes: Vec<u8>) -> anyhow::Result<SearchResponse> {
        let part = Part::bytes(bytes).file_name("telegram-image.jpg");
        let form = Form::new().part("file", part);
        let response = self
            .client
            .post(format!("{}/search", self.base_url))
            .multipart(form)
            .send()
            .await?
            .error_for_status()?;
        Ok(response.json().await?)
    }
}
