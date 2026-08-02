package domain

import "testing"

func TestDownloadURLPrefersSample(t *testing.T) {
	// Danbooru의 large_file_url은 원본이 아니라 850픽셀 안팎 견본입니다.
	// 어차피 512로 줄이므로 견본이 있으면 그쪽을 씁니다.
	post := SourcePost{
		LargeURL:   "https://cdn.example/sample.jpg",
		FileURL:    "https://cdn.example/original.png",
		PreviewURL: "https://cdn.example/preview.jpg",
	}
	if got := post.DownloadURL(); got != "https://cdn.example/sample.jpg" {
		t.Errorf("%q를 골랐습니다. 견본을 기대했습니다", got)
	}
}

func TestDownloadURLFallsBack(t *testing.T) {
	tests := []struct {
		name string
		post SourcePost
		want string
	}{
		{
			"견본이 없으면 원본",
			SourcePost{FileURL: "https://cdn.example/a.png", PreviewURL: "https://cdn.example/b.jpg"},
			"https://cdn.example/a.png",
		},
		{
			"원본도 없으면 미리보기",
			SourcePost{PreviewURL: "https://cdn.example/b.jpg"},
			"https://cdn.example/b.jpg",
		},
		{
			"쓸 수 있는 것이 없으면 빈 문자열",
			SourcePost{},
			"",
		},
		{
			"견본 확장자를 못 쓰면 다음 후보로",
			SourcePost{LargeURL: "https://cdn.example/a.mp4", FileURL: "https://cdn.example/a.jpg"},
			"https://cdn.example/a.jpg",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.post.DownloadURL(); got != tc.want {
				t.Errorf("%q입니다. %q를 기대했습니다", got, tc.want)
			}
		})
	}
}

func TestDownloadURLExtensionAllowlist(t *testing.T) {
	// 동영상과 움직이는 그림은 지금 파이프라인이 다루지 못합니다.
	// 받아 놓고 워커에서 실패하면 재시도 대기열만 채웁니다.
	allowed := []string{"a.jpg", "a.jpeg", "a.png", "a.webp", "a.JPG", "a.PNG"}
	blocked := []string{"a.gif", "a.mp4", "a.webm", "a.zip", "a.swf", "a", "a.jpg.exe"}

	for _, name := range allowed {
		post := SourcePost{LargeURL: "https://cdn.example/" + name}
		if post.DownloadURL() == "" {
			t.Errorf("%s를 걸렀습니다", name)
		}
	}
	for _, name := range blocked {
		post := SourcePost{LargeURL: "https://cdn.example/" + name}
		if got := post.DownloadURL(); got != "" {
			t.Errorf("%s를 받아들였습니다: %q", name, got)
		}
	}
}

func TestDownloadURLIgnoresQueryAndFragment(t *testing.T) {
	// 서명이 붙은 주소는 확장자 뒤에 물음표가 옵니다.
	tests := []string{
		"https://cdn.example/a.jpg?expires=123&sig=abc",
		"https://cdn.example/a.jpg#anchor",
		"https://cdn.example/a.png?v=2#x",
	}
	for _, url := range tests {
		post := SourcePost{LargeURL: url}
		if post.DownloadURL() != url {
			t.Errorf("%q를 걸렀습니다", url)
		}
	}
}

func TestSkipReason(t *testing.T) {
	usable := SourcePost{LargeURL: "https://cdn.example/a.jpg"}

	tests := []struct {
		name string
		post SourcePost
		want string
	}{
		{"정상", usable, ""},
		{"삭제됨", SourcePost{IsDeleted: true, LargeURL: usable.LargeURL}, "삭제된 게시물"},
		{"차단됨", SourcePost{IsBanned: true, LargeURL: usable.LargeURL}, "차단된 게시물"},
		{"쓸 수 있는 주소 없음", SourcePost{}, "지원하는 이미지 URL 없음"},
		{"동영상", SourcePost{LargeURL: "https://cdn.example/a.mp4"}, "지원하는 이미지 URL 없음"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.post.SkipReason(); got != tc.want {
				t.Errorf("%q입니다. %q를 기대했습니다", got, tc.want)
			}
		})
	}
}

func TestSkipReasonChecksDeletedFirst(t *testing.T) {
	// 삭제와 주소 없음이 겹치면 삭제가 더 정확한 설명입니다.
	post := SourcePost{IsDeleted: true}
	if got := post.SkipReason(); got != "삭제된 게시물" {
		t.Errorf("%q입니다. 삭제를 먼저 알려야 합니다", got)
	}
}
