use std::time::Duration;

use reqwest::header::HOST;
use reqwest::StatusCode;
use serde::Deserialize;
use tokio::time::sleep;
use tracing::{debug, info};

use sauce_core::errors::{SauceError, SauceResult};

#[derive(Debug, Clone, Deserialize)]
pub struct DanbooruPost {
    pub id: i64,
    pub source: Option<String>,
    pub file_url: Option<String>,
    pub large_file_url: Option<String>,
    pub preview_file_url: Option<String>,
    pub md5: Option<String>,
    pub image_width: Option<i32>,
    pub image_height: Option<i32>,
    pub rating: Option<String>,
    pub score: Option<i32>,
    pub tag_string: Option<String>,
    pub tag_string_general: Option<String>,
    pub tag_string_character: Option<String>,
    pub tag_string_copyright: Option<String>,
    pub tag_string_artist: Option<String>,
    pub tag_string_meta: Option<String>,
}

impl DanbooruPost {
    pub fn canonical_url(&self, base_url: &str) -> String {
        format!("{}/posts/{}", base_url.trim_end_matches('/'), self.id)
    }

    pub fn primary_file_url(&self) -> Option<String> {
        clean_optional(self.large_file_url.clone())
            .or_else(|| clean_optional(self.file_url.clone()))
            .or_else(|| clean_optional(self.preview_file_url.clone()))
    }

    pub fn supported_image_url(&self) -> Option<String> {
        self.primary_file_url()
            .filter(|url| is_supported_image_url(url))
    }

    pub fn tags(&self) -> Vec<String> {
        if let Some(tags) = self.tag_string.as_deref() {
            return tags.split_whitespace().map(str::to_owned).collect();
        }

        [
            &self.tag_string_general,
            &self.tag_string_character,
            &self.tag_string_copyright,
            &self.tag_string_artist,
            &self.tag_string_meta,
        ]
        .into_iter()
        .filter_map(|tags| tags.as_deref())
        .flat_map(|tags| tags.split_whitespace())
        .map(str::to_owned)
        .collect()
    }

    pub fn artist_tags(&self) -> Vec<String> {
        self.tag_string_artist
            .as_deref()
            .map(|tags| tags.split_whitespace().map(str::to_owned).collect())
            .unwrap_or_default()
    }
}

pub fn is_supported_image_url(url: &str) -> bool {
    let path = url.split(['?', '#']).next().unwrap_or(url).to_lowercase();
    [".jpg", ".jpeg", ".png", ".webp"]
        .iter()
        .any(|extension| path.ends_with(extension))
}

fn clean_optional(value: Option<String>) -> Option<String> {
    value.and_then(|value| {
        let value = value.trim();
        if value.is_empty() {
            None
        } else {
            Some(value.to_owned())
        }
    })
}

#[derive(Clone)]
pub struct DanbooruClient {
    client: reqwest::Client,
    direct_client: reqwest::Client,
    base_url: String,
    host_header: Option<String>,
}

impl DanbooruClient {
    pub fn new(
        base_url: impl Into<String>,
        host_header: Option<String>,
        user_agent: impl Into<String>,
        timeout: Duration,
    ) -> SauceResult<Self> {
        let user_agent = user_agent.into();
        let mut builder = crate::http_client::builder(Some(user_agent.clone()), timeout)?;
        if host_header.is_some() {
            builder = builder.http1_only();
        }
        let client = builder.build()?;
        let mut direct_builder =
            crate::http_client::builder_without_proxy(Some(user_agent), timeout)?;
        if host_header.is_some() {
            direct_builder = direct_builder.http1_only();
        }
        let direct_client = direct_builder.build()?;

        Ok(Self {
            client,
            direct_client,
            base_url: base_url.into().trim_end_matches('/').to_owned(),
            host_header,
        })
    }

    pub async fn fetch_posts(&self, tags: &str, limit: u32) -> SauceResult<Vec<DanbooruPost>> {
        self.fetch_posts_between(tags, limit, None, None).await
    }

    pub async fn fetch_posts_before_id(
        &self,
        tags: &str,
        limit: u32,
        before_id: i64,
    ) -> SauceResult<Vec<DanbooruPost>> {
        self.fetch_posts_between(tags, limit, None, Some(before_id))
            .await
    }

    pub async fn fetch_posts_between(
        &self,
        tags: &str,
        limit: u32,
        after_id: Option<i64>,
        before_id: Option<i64>,
    ) -> SauceResult<Vec<DanbooruPost>> {
        let mut posts = Vec::new();
        let mut cursor_before_id = before_id;

        while posts.len() < limit as usize {
            let remaining = limit as usize - posts.len();
            let page_limit = remaining.min(200);
            let mut page_posts = self
                .fetch_page_by_id_window(tags, page_limit, after_id, cursor_before_id)
                .await?;

            if page_posts.is_empty() {
                break;
            }

            page_posts.sort_by(|left, right| right.id.cmp(&left.id));
            let next_before_id = page_posts.iter().map(|post| post.id).min();
            let max_id = page_posts.iter().map(|post| post.id).max();

            if posts.is_empty() {
                if let Some(max_id) = max_id {
                    info!("danbooru backfill upper_bound_id={max_id}");
                }
            }
            if let Some(next_before_id) = next_before_id {
                info!(
                    "danbooru backfill page fetched={} next_before_id={next_before_id}",
                    page_posts.len()
                );
            }

            posts.extend(page_posts);

            match next_before_id {
                Some(next_before_id) if Some(next_before_id) != cursor_before_id => {
                    cursor_before_id = Some(next_before_id);
                }
                _ => break,
            }
        }

        posts.truncate(limit as usize);
        Ok(posts)
    }

    pub async fn fetch_latest_post_id(&self, tags: &str) -> SauceResult<Option<i64>> {
        let posts = self.fetch_page_by_id_window(tags, 1, None, None).await?;
        Ok(posts.into_iter().map(|post| post.id).max())
    }

    pub async fn fetch_post_by_id(
        &self,
        tags: &str,
        post_id: i64,
    ) -> SauceResult<Option<DanbooruPost>> {
        let tags = if tags.trim().is_empty() {
            format!("id:{post_id}")
        } else {
            format!("{} id:{post_id}", tags.trim())
        };
        let posts = self.fetch_posts_between(&tags, 1, None, None).await?;
        Ok(posts.into_iter().find(|post| post.id == post_id))
    }

    async fn fetch_page_by_id_window(
        &self,
        tags: &str,
        limit: usize,
        after_id: Option<i64>,
        before_id: Option<i64>,
    ) -> SauceResult<Vec<DanbooruPost>> {
        let url = format!("{}/posts.json", self.base_url);
        let mut last_error = None;
        let tags = tags_with_id_window(tags, after_id, before_id);

        for attempt in 1..=3 {
            let query = [("tags", tags.clone()), ("limit", limit.to_string())];
            debug!("danbooru request url={url}");
            let response = self.request(&url, &query).send().await;

            match response {
                Ok(response) if response.status().is_success() => {
                    log_rate_limit_headers(response.headers());
                    return response.json().await.map_err(SauceError::Http);
                }
                Ok(response)
                    if response.status() == StatusCode::TOO_MANY_REQUESTS
                        || response.status().is_server_error() =>
                {
                    log_rate_limit_headers(response.headers());
                    last_error = Some(format!("http {}", response.status()));
                }
                Ok(response) => {
                    return Err(SauceError::Http(response.error_for_status().unwrap_err()));
                }
                Err(err) => {
                    if sauce_core::outbound_proxy::direct_fallback_enabled()
                        && sauce_core::outbound_proxy::proxy_configured()
                    {
                        println!(
                            "직접망 fallback 전환: Danbooru 목록 요청 프록시 실패로 현재 네트워크 재시도"
                        );
                        sauce_core::outbound_proxy::activate_direct_fallback();
                        let direct_response = self.request_direct(&url, &query).send().await;
                        match direct_response {
                            Ok(response) if response.status().is_success() => {
                                log_rate_limit_headers(response.headers());
                                return response.json().await.map_err(SauceError::Http);
                            }
                            Ok(response)
                                if response.status() == StatusCode::TOO_MANY_REQUESTS
                                    || response.status().is_server_error() =>
                            {
                                log_rate_limit_headers(response.headers());
                                last_error = Some(format!("http {}", response.status()));
                            }
                            Ok(response) => {
                                return Err(SauceError::Http(
                                    response.error_for_status().unwrap_err(),
                                ));
                            }
                            Err(direct_err) => {
                                last_error = Some(format!("proxy: {err}; direct: {direct_err}"));
                            }
                        }
                    } else {
                        last_error = Some(err.to_string());
                    }
                }
            }

            sleep(Duration::from_millis(500 * 2u64.pow(attempt - 1))).await;
        }

        Err(SauceError::InvalidInput(
            last_error.unwrap_or_else(|| "danbooru request failed".to_owned()),
        ))
    }

    fn request(&self, url: &str, query: &[(&str, String)]) -> reqwest::RequestBuilder {
        let mut request = self.client.get(url).query(query);
        if let Some(host_header) = &self.host_header {
            request = request.header(HOST, host_header);
        }
        request
    }

    fn request_direct(&self, url: &str, query: &[(&str, String)]) -> reqwest::RequestBuilder {
        let mut request = self.direct_client.get(url).query(query);
        if let Some(host_header) = &self.host_header {
            request = request.header(HOST, host_header);
        }
        request
    }
}

pub fn tags_with_id_cursor(tags: &str, before_id: Option<i64>) -> String {
    tags_with_id_window(tags, None, before_id)
}

pub fn tags_with_id_window(tags: &str, after_id: Option<i64>, before_id: Option<i64>) -> String {
    let mut parts = tags
        .split_whitespace()
        .filter(|tag| {
            !tag.starts_with("id:<") && !tag.starts_with("id:>") && !tag.starts_with("order:")
        })
        .map(str::to_owned)
        .collect::<Vec<_>>();

    parts.push("order:id_desc".to_owned());

    if let Some(after_id) = after_id {
        parts.push(format!("id:>{after_id}"));
    }

    if let Some(before_id) = before_id {
        parts.push(format!("id:<{before_id}"));
    }

    parts.join(" ")
}

fn log_rate_limit_headers(headers: &reqwest::header::HeaderMap) {
    for key in [
        "x-rate-limit",
        "x-ratelimit-limit",
        "x-ratelimit-remaining",
        "x-ratelimit-reset",
    ] {
        if let Some(value) = headers.get(key).and_then(|value| value.to_str().ok()) {
            debug!("{key}={value}");
        }
    }
}
