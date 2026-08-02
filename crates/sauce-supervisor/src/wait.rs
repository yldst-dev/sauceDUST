use std::time::Duration;

use tokio::time::{sleep, Instant};

pub async fn http(url: &str, timeout: Duration) -> anyhow::Result<()> {
    let client = reqwest::Client::builder()
        .timeout(Duration::from_secs(5))
        .build()?;
    let deadline = Instant::now() + timeout;
    while Instant::now() < deadline {
        if let Ok(response) = client.get(url).send().await {
            if response.status().is_success() {
                return Ok(());
            }
        }
        sleep(Duration::from_secs(2)).await;
    }
    anyhow::bail!("timed out waiting for {url}")
}
