package danbooru

import (
	"fmt"
	"strings"

	"saucedust/internal/domain"
)

// apiPost는 Danbooru posts.json 응답 한 건입니다. 필요한 필드만 받습니다.
type apiPost struct {
	ID              int64  `json:"id"`
	MD5             string `json:"md5"`
	FileURL         string `json:"file_url"`
	LargeFileURL    string `json:"large_file_url"`
	PreviewFileURL  string `json:"preview_file_url"`
	Source          string `json:"source"`
	Rating          string `json:"rating"`
	Score           int    `json:"score"`
	ImageWidth      int    `json:"image_width"`
	ImageHeight     int    `json:"image_height"`
	FileSize        int64  `json:"file_size"`
	FileExt         string `json:"file_ext"`
	IsDeleted       bool   `json:"is_deleted"`
	IsBanned        bool   `json:"is_banned"`
	TagString       string `json:"tag_string"`
	TagStringArtist string `json:"tag_string_artist"`

	TagStringGeneral   string `json:"tag_string_general"`
	TagStringCharacter string `json:"tag_string_character"`
	TagStringCopyright string `json:"tag_string_copyright"`
	TagStringMeta      string `json:"tag_string_meta"`
}

func (p apiPost) toDomain(base string) domain.SourcePost {
	return domain.SourcePost{
		Site:         Site,
		PostID:       p.ID,
		MD5:          p.MD5,
		Rating:       p.Rating,
		Score:        p.Score,
		Width:        p.ImageWidth,
		Height:       p.ImageHeight,
		FileSize:     p.FileSize,
		FileURL:      p.FileURL,
		LargeURL:     p.LargeFileURL,
		PreviewURL:   p.PreviewFileURL,
		SourceURL:    strings.TrimSpace(p.Source),
		Tags:         p.tags(),
		ArtistTags:   splitTags(p.TagStringArtist),
		FileExt:      p.FileExt,
		IsDeleted:    p.IsDeleted,
		IsBanned:     p.IsBanned,
		CanonicalURL: fmt.Sprintf("%s/posts/%d", base, p.ID),
	}
}

// tags는 통합 태그 문자열을 우선 쓰고, 비어 있으면 분류별 필드를 합칩니다.
func (p apiPost) tags() []string {
	if merged := splitTags(p.TagString); len(merged) > 0 {
		return merged
	}

	seen := map[string]bool{}
	var out []string
	for _, group := range []string{
		p.TagStringArtist, p.TagStringCharacter,
		p.TagStringCopyright, p.TagStringGeneral, p.TagStringMeta,
	} {
		for _, tag := range splitTags(group) {
			if seen[tag] {
				continue
			}
			seen[tag] = true
			out = append(out, tag)
		}
	}
	return out
}

func splitTags(raw string) []string {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil
	}
	return fields
}
