use sauce_indexer::danbooru::{
    is_supported_image_url, tags_with_id_cursor, tags_with_id_window, DanbooruPost,
};
use sauce_indexer::normalizer::normalize_danbooru_post;

fn fixture_posts() -> Vec<DanbooruPost> {
    serde_json::from_str(include_str!("../fixtures/danbooru_posts.json")).unwrap()
}

fn fixture_post(id: i64) -> DanbooruPost {
    fixture_posts()
        .into_iter()
        .find(|post| post.id == id)
        .unwrap()
}

#[test]
fn splits_tag_string_into_tags() {
    let post = fixture_post(101);
    let normalized = normalize_danbooru_post(&post, "https://danbooru.donmai.us").unwrap();

    assert_eq!(
        normalized.tags,
        vec!["red_eyes", "long_hair", "sample_character", "sample_series"]
    );
}

#[test]
fn falls_back_from_large_file_url_to_file_url() {
    let post = fixture_post(102);
    let normalized = normalize_danbooru_post(&post, "https://danbooru.donmai.us").unwrap();

    assert_eq!(
        normalized.file_url,
        "https://cdn.example.invalid/original-102.jpg"
    );
}

#[test]
fn falls_back_from_blank_large_file_url_to_file_url() {
    let post = fixture_post(104);
    let normalized = normalize_danbooru_post(&post, "https://danbooru.donmai.us").unwrap();

    assert_eq!(
        normalized.file_url,
        "https://cdn.example.invalid/original-104.png"
    );
}

#[test]
fn falls_back_from_file_url_to_preview_file_url() {
    let post = fixture_post(103);
    let normalized = normalize_danbooru_post(&post, "https://danbooru.donmai.us").unwrap();

    assert_eq!(
        normalized.file_url,
        "https://cdn.example.invalid/preview-103.jpg"
    );
}

#[test]
fn prefers_large_file_url_when_available() {
    let post = fixture_post(101);
    let normalized = normalize_danbooru_post(&post, "https://danbooru.donmai.us").unwrap();

    assert_eq!(
        normalized.file_url,
        "https://cdn.example.invalid/large-101.jpg"
    );
}

#[test]
fn builds_canonical_url_from_post_id() {
    let post = fixture_post(101);
    let normalized = normalize_danbooru_post(&post, "https://danbooru.donmai.us/").unwrap();

    assert_eq!(
        normalized.canonical_url,
        "https://danbooru.donmai.us/posts/101"
    );
    assert_eq!(normalized.source_site, "danbooru");
    assert_eq!(normalized.source_post_id, "101");
}

#[test]
fn filters_supported_image_urls() {
    assert!(is_supported_image_url(
        "https://cdn.example.invalid/image.JPG?download=1"
    ));
    assert!(is_supported_image_url(
        "https://cdn.example.invalid/image.webp"
    ));
    assert!(!is_supported_image_url(
        "https://cdn.example.invalid/video.mp4"
    ));
    assert!(!is_supported_image_url(
        "https://cdn.example.invalid/video.webm"
    ));
}

#[test]
fn appends_id_cursor_to_tags() {
    assert_eq!(
        tags_with_id_cursor("rating:g source:* score:>20", Some(11309767)),
        "rating:g source:* score:>20 order:id_desc id:<11309767"
    );
}

#[test]
fn replaces_existing_id_cursor_tag() {
    assert_eq!(
        tags_with_id_cursor("rating:g id:<100 id:>50 score:>20", Some(90)),
        "rating:g score:>20 order:id_desc id:<90"
    );
}

#[test]
fn replaces_existing_order_tag() {
    assert_eq!(
        tags_with_id_cursor("rating:g order:score source:*", Some(90)),
        "rating:g source:* order:id_desc id:<90"
    );
}

#[test]
fn leaves_tags_without_cursor_unchanged() {
    assert_eq!(
        tags_with_id_cursor("rating:g source:*", None),
        "rating:g source:* order:id_desc"
    );
}

#[test]
fn builds_bounded_id_window_tags() {
    assert_eq!(
        tags_with_id_window("rating:g source:*", Some(100), Some(200)),
        "rating:g source:* order:id_desc id:>100 id:<200"
    );
}

#[test]
fn builds_unfiltered_id_window_tags() {
    assert_eq!(
        tags_with_id_window("", Some(100), Some(200)),
        "order:id_desc id:>100 id:<200"
    );
}

#[test]
fn keeps_exact_id_tag_for_retry_lookup() {
    assert_eq!(
        tags_with_id_window("id:123", None, None),
        "id:123 order:id_desc"
    );
}
