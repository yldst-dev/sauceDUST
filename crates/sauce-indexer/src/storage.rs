use sauce_core::models::NewImageMetadata;

use crate::danbooru::DanbooruPost;

pub fn danbooru_metadata(
    post: &DanbooruPost,
    base_url: &str,
    phash: String,
    dhash: String,
    width: u32,
    height: u32,
) -> NewImageMetadata {
    NewImageMetadata {
        source_site: "danbooru".to_owned(),
        source_post_id: post.id.to_string(),
        source_url: post.source.clone(),
        canonical_url: Some(post.canonical_url(base_url)),
        file_url: post.primary_file_url(),
        preview_url: post.preview_file_url.clone(),
        md5: post.md5.clone(),
        phash: Some(phash),
        dhash: Some(dhash),
        width: Some(post.image_width.unwrap_or(width as i32)),
        height: Some(post.image_height.unwrap_or(height as i32)),
        rating: post.rating.clone(),
        score: post.score,
        tags: post.tags(),
        artist_tags: post.artist_tags(),
    }
}
