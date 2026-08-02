pub mod bot;
pub mod config;
pub mod formatting;
pub mod sauce_api;
pub mod telegram;

pub async fn run() -> anyhow::Result<()> {
    dotenvy::dotenv().ok();
    let config = config::TelegramConfig::from_env()?;
    let telegram = telegram::TelegramClient::new(&config.bot_token, config.poll_timeout)?;
    let sauce = sauce_api::SauceApiClient::new(&config.sauce_api_url, config.request_timeout)?;
    bot::run(telegram, sauce).await
}
