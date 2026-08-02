package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBotMessageHasImage(t *testing.T) {
	if (BotMessage{}).HasImage() {
		t.Error("빈 메시지에 이미지가 있다고 합니다")
	}
	if !(BotMessage{FileID: "abc"}).HasImage() {
		t.Error("이미지를 못 알아봤습니다")
	}
}

func TestBotMessageIsCommand(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"/start", true},
		{"/START", true},
		{"  /start  ", true},
		{"/start hello", true},
		{"/start@saucedust_bot", true},
		{"/started", false},
		{"/stop", false},
		{"start", false},
		{"", false},
		{"이거 /start 맞나요", false},
	}

	for _, tc := range tests {
		t.Run(tc.text, func(t *testing.T) {
			got := BotMessage{Text: tc.text}.IsCommand("/start")
			if got != tc.want {
				t.Errorf("%v입니다. %v를 기대했습니다", got, tc.want)
			}
		})
	}
}

func TestTrimmedLeavesShortTextAlone(t *testing.T) {
	reply := BotReply{ChatID: 1, Text: "짧은 답"}
	if got := reply.Trimmed(); got.Text != reply.Text {
		t.Errorf("%q로 바뀌었습니다", got.Text)
	}
}

func TestTrimmedKeepsWithinLimit(t *testing.T) {
	// 한글은 한 글자에 3바이트입니다. 바이트로 자르면서 글자 경계를 놓치면
	// 텔레그램이 통째로 거부합니다.
	long := strings.Repeat("한", 4000)
	got := BotReply{Text: long}.Trimmed()

	if len(got.Text) > botTextLimit {
		t.Errorf("%d바이트입니다. %d 이하여야 합니다", len(got.Text), botTextLimit)
	}
	if !utf8.ValidString(got.Text) {
		t.Error("글자 중간에서 잘렸습니다")
	}
	if !strings.HasSuffix(got.Text, "…") {
		t.Error("잘렸다는 표시가 없습니다")
	}
	if strings.ContainsRune(got.Text, '�') {
		t.Error("깨진 글자가 들어 있습니다")
	}
}

func TestTrimmedHandlesMixedWidth(t *testing.T) {
	// 이모지는 4바이트입니다. 경계 계산이 3바이트만 가정하면 여기서 깨집니다.
	for _, filler := range []string{"a", "한", "🎨", "a한🎨"} {
		text := strings.Repeat(filler, 5000)
		got := BotReply{Text: text}.Trimmed()

		if len(got.Text) > botTextLimit {
			t.Errorf("%q 반복이 %d바이트가 되었습니다", filler, len(got.Text))
		}
		if !utf8.ValidString(got.Text) {
			t.Errorf("%q 반복이 글자 중간에서 잘렸습니다", filler)
		}
	}
}

func TestTrimmedAtBoundary(t *testing.T) {
	// 상한 바로 위아래에서 동작이 뒤집히지 않는지 봅니다.
	for _, size := range []int{botTextLimit - 1, botTextLimit, botTextLimit + 1} {
		got := BotReply{Text: strings.Repeat("a", size)}.Trimmed()

		if size <= botTextLimit && got.Text != strings.Repeat("a", size) {
			t.Errorf("%d바이트를 건드렸습니다", size)
		}
		if len(got.Text) > botTextLimit {
			t.Errorf("%d바이트가 %d바이트가 되었습니다", size, len(got.Text))
		}
	}
}

func TestTrimmedKeepsOtherFields(t *testing.T) {
	reply := BotReply{
		ChatID:         42,
		Text:           strings.Repeat("한", 4000),
		Buttons:        []BotButton{{Label: "원본", URL: "https://example.test"}},
		DisablePreview: true,
	}
	got := reply.Trimmed()

	if got.ChatID != 42 || !got.DisablePreview || len(got.Buttons) != 1 {
		t.Errorf("본문 말고 다른 것이 바뀌었습니다: %+v", got)
	}
}

func TestRatingLabel(t *testing.T) {
	tests := map[string]string{
		"g":            "전체 이용가",
		"general":      "전체 이용가",
		"G":            "전체 이용가",
		"  s  ":        "다소 민감",
		"sensitive":    "다소 민감",
		"q":            "선정적",
		"questionable": "선정적",
		"e":            "노골적",
		"explicit":     "노골적",
		"":             "등급 없음",
		"알수없음":         "알수없음",
	}

	for input, want := range tests {
		if got := RatingLabel(input); got != want {
			t.Errorf("%q가 %q입니다. %q를 기대했습니다", input, got, want)
		}
	}
}

func TestPostLabel(t *testing.T) {
	tests := []struct {
		name string
		img  Image
		want string
	}{
		{
			"작가 없음",
			Image{SourceSite: "danbooru", SourcePostID: 42},
			"danbooru #42",
		},
		{
			"작가 하나",
			Image{SourceSite: "danbooru", SourcePostID: 42, ArtistTags: []string{"someone"}},
			"danbooru #42 · someone",
		},
		{
			"작가 여럿",
			Image{SourceSite: "danbooru", SourcePostID: 7, ArtistTags: []string{"a", "b"}},
			"danbooru #7 · a, b",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PostLabel(tc.img); got != tc.want {
				t.Errorf("%q입니다. %q를 기대했습니다", got, tc.want)
			}
		})
	}
}
