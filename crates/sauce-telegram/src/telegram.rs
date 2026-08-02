use std::time::Duration;

use serde::{Deserialize, Serialize};

#[derive(Clone)]
pub struct TelegramClient {
    client: reqwest::Client,
    api_base: String,
    file_base: String,
    poll_timeout: Duration,
}

#[derive(Debug, Deserialize)]
pub struct TelegramResponse<T> {
    ok: bool,
    result: T,
    description: Option<String>,
}

#[derive(Debug, Deserialize)]
pub struct Update {
    pub update_id: i64,
    pub message: Option<Message>,
}

#[derive(Debug, Deserialize)]
pub struct Message {
    pub chat: Chat,
    pub text: Option<String>,
    pub photo: Option<Vec<PhotoSize>>,
    pub document: Option<Document>,
}

#[derive(Debug, Deserialize)]
pub struct Chat {
    pub id: i64,
}

#[derive(Debug, Deserialize)]
pub struct PhotoSize {
    pub file_id: String,
    pub width: i32,
    pub height: i32,
    pub file_size: Option<i64>,
}

#[derive(Debug, Deserialize)]
pub struct Document {
    pub file_id: String,
    pub mime_type: Option<String>,
}

#[derive(Debug, Deserialize)]
struct FileInfo {
    file_path: String,
}

#[derive(Debug, Serialize)]
struct SendMessageRequest<'a> {
    chat_id: i64,
    text: &'a str,
    disable_web_page_preview: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    reply_markup: Option<InlineKeyboardMarkup>,
}

#[derive(Debug, Clone, Serialize)]
pub struct InlineKeyboardMarkup {
    pub inline_keyboard: Vec<Vec<InlineKeyboardButton>>,
}

#[derive(Debug, Clone, Serialize)]
pub struct InlineKeyboardButton {
    pub text: String,
    pub url: String,
}

impl TelegramClient {
    pub fn new(token: &str, poll_timeout: Duration) -> anyhow::Result<Self> {
        let builder = reqwest::Client::builder().timeout(poll_timeout + Duration::from_secs(10));
        let client = sauce_core::outbound_proxy::apply_env_proxy(builder)?.build()?;
        Ok(Self {
            client,
            api_base: format!("https://api.telegram.org/bot{token}"),
            file_base: format!("https://api.telegram.org/file/bot{token}"),
            poll_timeout,
        })
    }

    pub async fn get_updates(&self, offset: Option<i64>) -> anyhow::Result<Vec<Update>> {
        let mut request = self
            .client
            .get(format!("{}/getUpdates", self.api_base))
            .query(&[
                ("timeout", self.poll_timeout.as_secs().to_string()),
                ("allowed_updates", r#"["message"]"#.to_owned()),
            ]);
        if let Some(offset) = offset {
            request = request.query(&[("offset", offset.to_string())]);
        }
        self.decode(request.send().await?).await
    }

    pub async fn download_file(&self, file_id: &str) -> anyhow::Result<Vec<u8>> {
        let file = self
            .decode::<FileInfo>(
                self.client
                    .get(format!("{}/getFile", self.api_base))
                    .query(&[("file_id", file_id)])
                    .send()
                    .await?,
            )
            .await?;
        let bytes = self
            .client
            .get(format!("{}/{}", self.file_base, file.file_path))
            .send()
            .await?
            .error_for_status()?
            .bytes()
            .await?;
        Ok(bytes.to_vec())
    }

    pub async fn send_message(&self, chat_id: i64, text: &str) -> anyhow::Result<()> {
        self.send_message_with_markup(chat_id, text, None).await
    }

    pub async fn send_message_with_markup(
        &self,
        chat_id: i64,
        text: &str,
        reply_markup: Option<InlineKeyboardMarkup>,
    ) -> anyhow::Result<()> {
        let body = SendMessageRequest {
            chat_id,
            text,
            disable_web_page_preview: false,
            reply_markup,
        };
        self.decode::<serde_json::Value>(
            self.client
                .post(format!("{}/sendMessage", self.api_base))
                .json(&body)
                .send()
                .await?,
        )
        .await?;
        Ok(())
    }

    async fn decode<T: for<'de> Deserialize<'de>>(
        &self,
        response: reqwest::Response,
    ) -> anyhow::Result<T> {
        let response = response.error_for_status()?;
        let body = response.json::<TelegramResponse<T>>().await?;
        if body.ok {
            Ok(body.result)
        } else {
            Err(anyhow::anyhow!(
                "{}",
                body.description
                    .unwrap_or_else(|| "telegram api error".to_owned())
            ))
        }
    }
}

impl Message {
    pub fn image_file_id(&self) -> Option<&str> {
        if let Some(photo) = &self.photo {
            return photo
                .iter()
                .max_by_key(|item| {
                    item.file_size
                        .unwrap_or_else(|| i64::from(item.width) * i64::from(item.height))
                })
                .map(|item| item.file_id.as_str());
        }
        self.document
            .as_ref()
            .filter(|document| {
                document
                    .mime_type
                    .as_deref()
                    .is_some_and(|mime| mime.starts_with("image/"))
            })
            .map(|document| document.file_id.as_str())
    }
}
