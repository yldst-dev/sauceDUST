package domain

import (
	"path"
	"strings"
)

// SourcePost는 수집 대상 사이트에서 받은 게시물 한 건입니다.
// 사이트별 응답 형태는 어댑터에서 이 타입으로 변환합니다.
type SourcePost struct {
	Site         string
	PostID       int64
	MD5          string
	Rating       string
	Score        int
	Width        int
	Height       int
	FileSize     int64
	FileURL      string
	LargeURL     string
	PreviewURL   string
	SourceURL    string
	Tags         []string
	ArtistTags   []string
	FileExt      string
	IsDeleted    bool
	IsBanned     bool
	CanonicalURL string
}

var allowedExt = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
}

// DownloadURL은 내려받을 주소를 고릅니다.
//
// LargeURL을 먼저 봅니다. Danbooru에서 이 값은 원본이 아니라 긴 변 850픽셀
// 안팎의 견본입니다. 어차피 512로 줄여서 계산하므로 원본을 받으면 대역폭만
// 몇 배로 쓰고 결과는 같습니다. 견본이 없을 때만 원본(FileURL)으로 갑니다.
//
// 확장자를 걸러 내는 이유는 동영상과 애니메이션 GIF 때문입니다.
func (p SourcePost) DownloadURL() string {
	for _, candidate := range []string{p.LargeURL, p.FileURL, p.PreviewURL} {
		if candidate != "" && allowedExt[strings.ToLower(path.Ext(stripQuery(candidate)))] {
			return candidate
		}
	}
	return ""
}

// SkipReason은 내려받을 필요조차 없는 게시물의 사유를 돌려줍니다.
// 빈 문자열이면 처리 대상입니다.
func (p SourcePost) SkipReason() string {
	switch {
	case p.IsDeleted:
		return "삭제된 게시물"
	case p.IsBanned:
		return "차단된 게시물"
	case p.DownloadURL() == "":
		return "지원하는 이미지 URL 없음"
	default:
		return ""
	}
}

func stripQuery(u string) string {
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		return u[:i]
	}
	return u
}
