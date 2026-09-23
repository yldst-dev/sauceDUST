package httpapi

import (
	"encoding/base64"
	"fmt"

	"saucedust/internal/domain"
)

// 전송 형식은 어댑터의 관심사이므로 도메인 타입과 분리해 둡니다.
// 벡터는 JSON 숫자 배열 대신 float32 원본 바이트를 base64로 실어 보냅니다.
// 768차원 기준 숫자 배열은 약 10KB, 이 방식은 4KB입니다.

type IngestRequest struct {
	NodeID string       `json:"node_id"`
	Items  []IngestItem `json:"items"`
}

type IngestItem struct {
	SourceSite   string       `json:"source_site"`
	SourcePostID int64        `json:"source_post_id"`
	SourceURL    string       `json:"source_url,omitempty"`
	CanonicalURL string       `json:"canonical_url,omitempty"`
	FileURL      string       `json:"file_url,omitempty"`
	PreviewURL   string       `json:"preview_url,omitempty"`
	MD5          string       `json:"md5,omitempty"`
	PHash        string       `json:"phash,omitempty"`
	DHash        string       `json:"dhash,omitempty"`
	Width        int          `json:"width,omitempty"`
	Height       int          `json:"height,omitempty"`
	FileSize     int64        `json:"file_size,omitempty"`
	Rating       string       `json:"rating,omitempty"`
	Score        int          `json:"score,omitempty"`
	Tags         []string     `json:"tags,omitempty"`
	ArtistTags   []string     `json:"artist_tags,omitempty"`
	IndexedBy    string       `json:"indexed_by,omitempty"`
	Vectors      []WireVector `json:"vectors"`
	Thumb        string       `json:"thumb_base64,omitempty"`
}

type WireVector struct {
	ModelID string `json:"model_id"`
	Size    int    `json:"size"`
	Data    string `json:"data"`
}

type IngestResponse struct {
	Accepted int `json:"accepted"`
}

func EncodeItem(item domain.IndexedImage) IngestItem {
	out := IngestItem{
		SourceSite:   item.Image.SourceSite,
		SourcePostID: item.Image.SourcePostID,
		SourceURL:    item.Image.SourceURL,
		CanonicalURL: item.Image.CanonicalURL,
		FileURL:      item.Image.FileURL,
		PreviewURL:   item.Image.PreviewURL,
		MD5:          item.Image.MD5,
		PHash:        item.Image.PHash,
		DHash:        item.Image.DHash,
		Width:        item.Image.Width,
		Height:       item.Image.Height,
		FileSize:     item.Image.FileSize,
		Rating:       item.Image.Rating,
		Score:        item.Image.Score,
		Tags:         item.Image.Tags,
		ArtistTags:   item.Image.ArtistTags,
		IndexedBy:    item.Image.IndexedBy,
	}
	for _, v := range item.Vectors {
		out.Vectors = append(out.Vectors, WireVector{
			ModelID: v.ModelID,
			Size:    len(v.Values),
			Data:    base64.StdEncoding.EncodeToString(domain.EncodeVector(v.Values)),
		})
	}
	if len(item.Thumb) > 0 {
		out.Thumb = base64.StdEncoding.EncodeToString(item.Thumb)
	}
	return out
}

func (i IngestItem) Decode() (domain.IndexedImage, error) {
	out := domain.IndexedImage{
		Image: domain.Image{
			SourceSite:   i.SourceSite,
			SourcePostID: i.SourcePostID,
			SourceURL:    i.SourceURL,
			CanonicalURL: i.CanonicalURL,
			FileURL:      i.FileURL,
			PreviewURL:   i.PreviewURL,
			MD5:          i.MD5,
			PHash:        i.PHash,
			DHash:        i.DHash,
			Width:        i.Width,
			Height:       i.Height,
			FileSize:     i.FileSize,
			Rating:       i.Rating,
			Score:        i.Score,
			Tags:         i.Tags,
			ArtistTags:   i.ArtistTags,
			IndexedBy:    i.IndexedBy,
		},
	}
	if i.SourceSite == "" || i.SourcePostID <= 0 {
		return out, fmt.Errorf("출처 정보가 없습니다")
	}

	for _, v := range i.Vectors {
		raw, err := base64.StdEncoding.DecodeString(v.Data)
		if err != nil {
			return out, fmt.Errorf("모델 %s 벡터를 해석하지 못했습니다: %w", v.ModelID, err)
		}
		values, err := domain.DecodeVector(raw)
		if err != nil {
			return out, fmt.Errorf("모델 %s 벡터를 해석하지 못했습니다: %w", v.ModelID, err)
		}
		if v.Size > 0 && len(values) != v.Size {
			return out, fmt.Errorf("모델 %s 벡터 길이가 %d인데 %d라고 했습니다",
				v.ModelID, len(values), v.Size)
		}
		out.Vectors = append(out.Vectors, domain.Vector{ModelID: v.ModelID, Values: values})
	}
	if len(out.Vectors) == 0 {
		return out, fmt.Errorf("게시물 %d에 벡터가 없습니다", i.SourcePostID)
	}

	if i.Thumb != "" {
		thumb, err := base64.StdEncoding.DecodeString(i.Thumb)
		if err != nil {
			return out, fmt.Errorf("축소본을 해석하지 못했습니다: %w", err)
		}
		out.Thumb = thumb
	}
	return out, nil
}

type NodeView struct {
	ID          string  `json:"id"`
	Role        string  `json:"role"`
	Status      string  `json:"status"`
	Hostname    string  `json:"hostname"`
	Platform    string  `json:"platform"`
	Device      string  `json:"device"`
	NetMode     string  `json:"net_mode"`
	Concurrency int     `json:"concurrency"`
	Version     string  `json:"version"`
	LastSeenSec float64 `json:"last_seen_sec"`
	Downloaded  int64   `json:"downloaded"`
	Embedded    int64   `json:"embedded"`
	Saved       int64   `json:"saved"`
	Failed      int64   `json:"failed"`
	PerSecond   float64 `json:"per_second"`
	CPUPct      float32 `json:"cpu_pct"`
	MemMB       float32 `json:"mem_mb"`
}

type StatsView struct {
	Images       int64            `json:"images"`
	Vectors      map[string]int64 `json:"vectors"`
	OnlineNodes  int              `json:"online_nodes"`
	TotalNodes   int              `json:"total_nodes"`
	RangesDone   int64            `json:"ranges_done"`
	RangesActive int64            `json:"ranges_active"`
	RangesFailed int64            `json:"ranges_failed"`
	RetryQueue   int64            `json:"retry_queue"`
	SavedPerSec  float64          `json:"saved_per_sec"`
	// HighWatermark는 지금까지 확인한 가장 최신 게시물 번호입니다.
	// BackfillBefore는 과거로 내려가는 수집 경계입니다. 둘 사이가 이미 훑은 구간입니다.
	HighWatermark   int64  `json:"high_watermark"`
	BackfillBefore  int64  `json:"backfill_before"`
	SourceSite      string `json:"source_site"`
	ScopeKey        string `json:"scope_key"`
	RangesTotal     int64  `json:"ranges_total"`
	RangesEmpty     int64  `json:"ranges_empty"`
	RangesExhausted int64  `json:"ranges_exhausted"`
}

type SearchHitView struct {
	SourceSite   string   `json:"source_site"`
	SourcePostID int64    `json:"source_post_id"`
	CanonicalURL string   `json:"canonical_url"`
	SourceURL    string   `json:"source_url,omitempty"`
	PreviewURL   string   `json:"preview_url,omitempty"`
	Rating       string   `json:"rating,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	ArtistTags   []string `json:"artist_tags,omitempty"`
	Score        float32  `json:"score"`
	HashDistance int      `json:"hash_distance"`
	Exact        bool     `json:"exact"`
}

type SearchResponse struct {
	ExactMatch bool            `json:"exact_match"`
	Hits       []SearchHitView `json:"hits"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// ReadyView는 각 부품이 살아 있는지 알려줍니다.
// 배치 도구와 감시 도구가 읽습니다.
type ReadyView struct {
	Ready  bool              `json:"ready"`
	Checks map[string]string `json:"checks"`
}

// ImageView는 이미지 한 건입니다.
// Indexed가 false면 아직 수집되지 않은 것입니다. 검색 결과가 예상과 다를 때
// 그 게시물이 애초에 들어와 있는지 확인하는 데 씁니다.
type ImageView struct {
	Indexed      bool     `json:"indexed"`
	ID           int64    `json:"id,omitempty"`
	SourceSite   string   `json:"source_site,omitempty"`
	SourcePostID int64    `json:"source_post_id,omitempty"`
	CanonicalURL string   `json:"canonical_url,omitempty"`
	SourceURL    string   `json:"source_url,omitempty"`
	PreviewURL   string   `json:"preview_url,omitempty"`
	MD5          string   `json:"md5,omitempty"`
	PHash        string   `json:"phash,omitempty"`
	DHash        string   `json:"dhash,omitempty"`
	Width        int      `json:"width,omitempty"`
	Height       int      `json:"height,omitempty"`
	Rating       string   `json:"rating,omitempty"`
	Score        int      `json:"score,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	ArtistTags   []string `json:"artist_tags,omitempty"`
	IndexedBy    string   `json:"indexed_by,omitempty"`
	HasThumb     bool     `json:"has_thumb"`
}

func newImageView(img domain.Image) ImageView {
	return ImageView{
		Indexed:      true,
		ID:           img.ID,
		SourceSite:   img.SourceSite,
		SourcePostID: img.SourcePostID,
		CanonicalURL: img.CanonicalURL,
		SourceURL:    img.SourceURL,
		PreviewURL:   img.PreviewURL,
		MD5:          img.MD5,
		PHash:        img.PHash,
		DHash:        img.DHash,
		Width:        img.Width,
		Height:       img.Height,
		Rating:       img.Rating,
		Score:        img.Score,
		Tags:         img.Tags,
		ArtistTags:   img.ArtistTags,
		IndexedBy:    img.IndexedBy,
		HasThumb:     img.ThumbPath != "",
	}
}
