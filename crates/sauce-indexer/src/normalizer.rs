use serde::Serialize;

use crate::danbooru::DanbooruPost;

#[derive(Debug, Clone, Serialize, PartialEq, Eq)]
pub struct NormalizedImageRecord {
    pub source_site: String,
    pub source_post_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub source_url: Option<String>,
    pub canonical_url: String,
    pub file_url: String,
    pub preview_url: Option<String>,
    pub rating: Option<String>,
    pub tags: Vec<String>,
    pub md5: Option<String>,
    pub width: Option<i32>,
    pub height: Option<i32>,
    pub score: Option<i32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub artist_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub artist_name: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub page_index: Option<u32>,
}

pub fn normalize_danbooru_post(
    post: &DanbooruPost,
    base_url: &str,
) -> Option<NormalizedImageRecord> {
    let file_url = danbooru_file_url(post)?;

    Some(NormalizedImageRecord {
        source_site: "danbooru".to_owned(),
        source_post_id: post.id.to_string(),
        source_url: clean_optional(post.source.clone()),
        canonical_url: post.canonical_url(base_url),
        file_url,
        preview_url: clean_optional(post.preview_file_url.clone()),
        rating: clean_optional(post.rating.clone()),
        tags: post.tags(),
        md5: clean_optional(post.md5.clone()),
        width: post.image_width,
        height: post.image_height,
        score: post.score,
        artist_id: None,
        artist_name: None,
        page_index: None,
    })
}

pub fn normalize_danbooru_posts(
    posts: &[DanbooruPost],
    base_url: &str,
) -> Vec<NormalizedImageRecord> {
    posts
        .iter()
        .filter_map(|post| normalize_danbooru_post(post, base_url))
        .collect()
}

fn danbooru_file_url(post: &DanbooruPost) -> Option<String> {
    post.primary_file_url()
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
