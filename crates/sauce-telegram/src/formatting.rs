use sauce_core::models::{SearchResponse, SearchResult};

use crate::telegram::{InlineKeyboardButton, InlineKeyboardMarkup};

pub struct FormattedSearchResponse {
    pub text: String,
    pub reply_markup: Option<InlineKeyboardMarkup>,
}

pub fn format_search_response(response: &SearchResponse) -> FormattedSearchResponse {
    let Some(result) = response
        .results
        .iter()
        .max_by(|left, right| left.score.total_cmp(&right.score))
    else {
        return FormattedSearchResponse {
            text: "일치하는 결과를 찾지 못했습니다.".to_owned(),
            reply_markup: None,
        };
    };

    let text = format_result(result);
    let inline_keyboard = result_buttons(result).map(|row| InlineKeyboardMarkup {
        inline_keyboard: vec![row],
    });

    FormattedSearchResponse {
        text: truncate_message(text),
        reply_markup: inline_keyboard,
    }
}

fn format_result(result: &SearchResult) -> String {
    let mut lines = Vec::new();
    lines.push(format!("정확도 {}%", score_percent(result.score)));
    lines.push(format!("아티스트: {}", artist_text(result)));
    lines.push(format!("등급: {}", rating_text(result.rating.as_deref())));
    lines.push(String::new());
    if !result.tags.is_empty() {
        lines.push(format!("태그: {}", result.tags.join(", ")));
    } else {
        lines.push("태그: -".to_owned());
    }
    lines.join("\n")
}

fn score_percent(score: f32) -> u32 {
    (score.clamp(0.0, 1.0) * 100.0).round() as u32
}

fn artist_text(result: &SearchResult) -> String {
    if result.artist_tags.is_empty() {
        "-".to_owned()
    } else {
        result.artist_tags.join(", ")
    }
}

fn rating_text(rating: Option<&str>) -> String {
    let Some(rating) = rating else {
        return "-".to_owned();
    };
    match rating.trim().to_ascii_lowercase().as_str() {
        "g" | "general" | "safe" => "일반".to_owned(),
        "s" | "sensitive" => "민감".to_owned(),
        "q" | "questionable" => "수위 높음".to_owned(),
        "e" | "explicit" | "r18" => "성인".to_owned(),
        "r18g" => "고어/성인".to_owned(),
        "" => "-".to_owned(),
        value => value.to_owned(),
    }
}

fn result_buttons(result: &SearchResult) -> Option<Vec<InlineKeyboardButton>> {
    let mut row = Vec::new();
    push_button(&mut row, "Danbooru", &result.canonical_url);
    push_button(&mut row, "원본", &result.source_url);
    push_button(&mut row, "미리보기", &result.preview_url);
    (!row.is_empty()).then_some(row)
}

fn push_button(row: &mut Vec<InlineKeyboardButton>, text: &str, url: &Option<String>) {
    if let Some(url) = url.as_deref().filter(|url| is_http_url(url)) {
        row.push(InlineKeyboardButton {
            text: text.to_owned(),
            url: url.to_owned(),
        });
    }
}

fn is_http_url(url: &str) -> bool {
    url.starts_with("http://") || url.starts_with("https://")
}

fn truncate_message(mut message: String) -> String {
    const LIMIT: usize = 3900;
    if message.len() <= LIMIT {
        return message;
    }
    message.truncate(LIMIT);
    message.push_str("\n...");
    message
}

#[cfg(test)]
mod tests {
    use sauce_core::models::{SearchResponse, SearchResult};

    use super::format_search_response;

    #[test]
    fn formats_only_highest_score_result() {
        let response = SearchResponse {
            results: vec![
                result(0.42, "old_artist", "https://danbooru.donmai.us/posts/1"),
                result(0.981, "torokko", "https://danbooru.donmai.us/posts/2"),
            ],
        };

        let formatted = format_search_response(&response);

        assert!(formatted.text.contains("정확도 98%"));
        assert!(formatted.text.contains("아티스트: torokko"));
        assert!(formatted.text.contains("등급: 일반"));
        assert!(formatted.text.contains("태그: 1girl, acoustic_guitar"));
        assert!(!formatted.text.contains("old_artist"));
        assert_eq!(
            formatted
                .reply_markup
                .as_ref()
                .map(|markup| markup.inline_keyboard[0].len()),
            Some(3)
        );
    }

    #[test]
    fn localizes_rating_values() {
        for (raw, expected) in [
            ("g", "일반"),
            ("safe", "일반"),
            ("s", "민감"),
            ("sensitive", "민감"),
            ("q", "수위 높음"),
            ("e", "성인"),
            ("r18", "성인"),
            ("r18g", "고어/성인"),
        ] {
            let response = SearchResponse {
                results: vec![SearchResult {
                    rating: Some(raw.to_owned()),
                    ..result(0.9, "torokko", "https://danbooru.donmai.us/posts/2")
                }],
            };

            let formatted = format_search_response(&response);

            assert!(formatted.text.contains(&format!("등급: {expected}")));
        }
    }

    fn result(score: f32, artist: &str, canonical_url: &str) -> SearchResult {
        SearchResult {
            score,
            source_site: "danbooru".to_owned(),
            source_post_id: "1".to_owned(),
            source_url: Some("https://www.pixiv.net/artworks/1".to_owned()),
            canonical_url: Some(canonical_url.to_owned()),
            preview_url: Some("https://example.com/preview.jpg".to_owned()),
            rating: Some("g".to_owned()),
            tags: vec!["1girl".to_owned(), "acoustic_guitar".to_owned()],
            artist_tags: vec![artist.to_owned()],
        }
    }
}
