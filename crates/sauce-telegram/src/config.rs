use std::env;
use std::time::Duration;

pub struct TelegramConfig {
    pub bot_token: String,
    pub sauce_api_url: String,
    pub poll_timeout: Duration,
    pub request_timeout: Duration,
}

impl TelegramConfig {
    pub fn from_env() -> anyhow::Result<Self> {
        let bot_token = env::var("TELEGRAM_BOT_TOKEN")
            .ok()
            .map(|value| value.trim().to_owned())
            .filter(|value| !value.is_empty())
            .ok_or_else(|| anyhow::anyhow!("TELEGRAM_BOT_TOKEN is required"))?;
        let sauce_api_url = env::var("SAUCE_API_URL")
            .unwrap_or_else(|_| "http://localhost:8000".to_owned())
            .trim_end_matches('/')
            .to_owned();
        let poll_timeout = Duration::from_secs(parse_env("TELEGRAM_POLL_TIMEOUT_SECS", 30u64)?);
        let request_timeout = Duration::from_secs(parse_env("REQUEST_TIMEOUT_SECS", 30u64)?);

        Ok(Self {
            bot_token,
            sauce_api_url,
            poll_timeout,
            request_timeout,
        })
    }
}

fn parse_env<T>(key: &str, default: T) -> anyhow::Result<T>
where
    T: std::str::FromStr + Copy,
    T::Err: std::fmt::Display,
{
    match env::var(key) {
        Ok(value) if !value.trim().is_empty() => {
            value.parse().map_err(|err| anyhow::anyhow!("{key}: {err}"))
        }
        _ => Ok(default),
    }
}
