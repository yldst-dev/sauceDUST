use tokio::time::{sleep, Duration};

use crate::formatting::format_search_response;
use crate::sauce_api::SauceApiClient;
use crate::telegram::{Message, TelegramClient};

pub async fn run(telegram: TelegramClient, sauce: SauceApiClient) -> anyhow::Result<()> {
    let mut offset = None;
    tracing::info!("sauce-telegram started");

    loop {
        match telegram.get_updates(offset).await {
            Ok(updates) => {
                for update in updates {
                    offset = Some(update.update_id + 1);
                    if let Some(message) = update.message {
                        handle_message(&telegram, &sauce, message).await;
                    }
                }
            }
            Err(err) => {
                tracing::warn!("telegram getUpdates failed: {err}");
                sleep(Duration::from_secs(5)).await;
            }
        }
    }
}

async fn handle_message(telegram: &TelegramClient, sauce: &SauceApiClient, message: Message) {
    let chat_id = message.chat.id;
    if message.text.as_deref().is_some_and(|text| text == "/start") {
        let _ = telegram
            .send_message(
                chat_id,
                "이미지를 보내면 인덱싱된 이미지와 비교해서 출처를 찾아드립니다.",
            )
            .await;
        return;
    }

    let Some(file_id) = message.image_file_id() else {
        let _ = telegram
            .send_message(
                chat_id,
                "검색할 이미지를 사진 또는 이미지 파일로 보내주십시오.",
            )
            .await;
        return;
    };

    let result = async {
        let bytes = telegram.download_file(file_id).await?;
        let response = sauce.search_image(bytes).await?;
        Ok::<_, anyhow::Error>(format_search_response(&response))
    }
    .await;

    let text = match result {
        Ok(formatted) => {
            let _ = telegram
                .send_message_with_markup(chat_id, &formatted.text, formatted.reply_markup)
                .await;
            return;
        }
        Err(err) => {
            tracing::warn!("telegram image search failed: {err}");
            format!("검색 처리 중 오류가 발생했습니다: {err}")
        }
    };
    let _ = telegram.send_message(chat_id, &text).await;
}
