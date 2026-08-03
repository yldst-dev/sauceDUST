// Package qdrant는 벡터 검색 색인 어댑터입니다.
package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"saucedust/internal/domain"
)

// Client는 Qdrant REST API를 씁니다. 필요한 동작이 네 가지뿐이라
// gRPC 의존성을 들이지 않고 표준 라이브러리만으로 붙입니다.
type Client struct {
	base   string
	apiKey string
	http   *http.Client
	// quantize가 켜지면 컬렉션을 int8로 압축해서 저장합니다.
	// 정확도 손실은 미미하고 용량은 4분의 1이 됩니다.
	quantize bool
	log      *slog.Logger
}

type Options struct {
	BaseURL  string
	APIKey   string
	Timeout  time.Duration
	Quantize bool
	Log      *slog.Logger
}

func New(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, errors.New("Qdrant 주소가 비어 있습니다")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	return &Client{
		base:     strings.TrimRight(opts.BaseURL, "/"),
		apiKey:   opts.APIKey,
		http:     &http.Client{Timeout: opts.Timeout},
		quantize: opts.Quantize,
		log:      opts.Log,
	}, nil
}

func (c *Client) Ping(ctx context.Context) error {
	return c.call(ctx, http.MethodGet, "/collections", nil, nil)
}

func (c *Client) EnsureCollection(ctx context.Context, m domain.EmbeddingModel) error {
	if m.Collection == "" {
		return errors.New("컬렉션 이름이 비어 있습니다")
	}
	if m.VectorSize <= 0 {
		return fmt.Errorf("모델 %s의 벡터 차원이 잘못되었습니다: %d", m.ID, m.VectorSize)
	}

	exists, err := c.hasCollection(ctx, m.Collection)
	if err != nil {
		return err
	}
	if exists {
		return c.verifyCollection(ctx, m)
	}

	body := map[string]any{
		"vectors":     vectorParams(m),
		"hnsw_config": hnswParams(),
	}
	if c.quantize {
		body["quantization_config"] = quantParams()
	}

	if err := c.call(ctx, http.MethodPut, "/collections/"+m.Collection, body, nil); err != nil {
		return fmt.Errorf("컬렉션 %s를 만들지 못했습니다: %w", m.Collection, err)
	}
	return nil
}

// verifyCollection은 이미 있는 컬렉션의 차원이 모델과 맞는지 봅니다.
// 다르면 벡터가 섞여 검색이 조용히 망가지므로 시작을 막습니다.
func (c *Client) verifyCollection(ctx context.Context, m domain.EmbeddingModel) error {
	var payload struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors struct {
						Size     int    `json:"size"`
						Distance string `json:"distance"`
						OnDisk   *bool  `json:"on_disk"`
					} `json:"vectors"`
				} `json:"params"`
				HNSW struct {
					OnDisk *bool `json:"on_disk"`
				} `json:"hnsw_config"`
				Quantization *struct {
					Binary *struct{} `json:"binary"`
				} `json:"quantization_config"`
			} `json:"config"`
		} `json:"result"`
	}
	if err := c.call(ctx, http.MethodGet, "/collections/"+m.Collection, nil, &payload); err != nil {
		return err
	}

	vectors := payload.Result.Config.Params.Vectors
	if vectors.Size != 0 && vectors.Size != m.VectorSize {
		return fmt.Errorf("%w: 컬렉션 %s의 차원이 %d인데 모델 %s는 %d입니다",
			domain.ErrModelMismatch, m.Collection, vectors.Size, m.ID, m.VectorSize)
	}
	// 거리 함수가 다르면 오류 없이 검색 결과만 조용히 이상해집니다.
	if want := distanceName(m.Distance); vectors.Distance != "" && vectors.Distance != want {
		return fmt.Errorf("%w: 컬렉션 %s의 거리가 %s인데 모델 %s는 %s입니다",
			domain.ErrModelMismatch, m.Collection, vectors.Distance, m.ID, want)
	}
	// 셋 중 하나라도 어긋나면 옮깁니다. 담을 장수 계산이 이 셋 위에
	// 서 있어서, 하나만 달라도 실제 메모리가 계산보다 몇 배가 됩니다.
	cfg := payload.Result.Config
	needsMove := vectors.OnDisk == nil || !*vectors.OnDisk ||
		cfg.HNSW.OnDisk == nil || !*cfg.HNSW.OnDisk
	if c.quantize && (cfg.Quantization == nil || cfg.Quantization.Binary == nil) {
		needsMove = true
	}
	if needsMove {
		return c.moveVectorsToDisk(ctx, m.Collection)
	}
	return nil
}

// vectorParams는 벡터 저장 방식입니다.
//
// on_disk를 명시합니다. 지금 판의 기본값도 원본을 memmap으로 두지만
// 문서에 적히지 않은 기본값이라, 판이 올라가며 바뀌면 메모리 요건이
// 조용히 몇 배로 뜁니다. 담을 장수 계산이 그 전제 위에 있습니다.
func vectorParams(m domain.EmbeddingModel) map[string]any {
	return map[string]any{
		"size":     m.VectorSize,
		"distance": distanceName(m.Distance),
		"on_disk":  true,
	}
}

// hnswParams는 그래프도 디스크에 둡니다.
//
// 그래프가 장당 상주 비용의 대부분이었습니다. 30만 점 768차원에서 잰
// 값입니다. int8에 그래프를 램에 두면 하한이 450MB인데, 이진으로 줄이고
// 그래프까지 내리면 250MB입니다. 장당 1,046바이트가 334바이트가 됩니다.
// 이 차이가 8GB에서 410만 장과 1,190만 장을 가릅니다.
func hnswParams() map[string]any {
	return map[string]any{"on_disk": true}
}

// quantParams는 이진 양자화입니다.
//
// int8은 차원마다 1바이트, 이진은 1비트라 8분의 1입니다. 정확도 손실은
// 재점수로 되찾습니다. 실제 SigLIP 임베딩 2,000장에 망가뜨린 질의
// 2,000건을 넣어 재니 1등 정답률이 int8 96.5퍼센트, 이진에 재점수
// 4배가 96.1퍼센트였습니다. 재점수 없이는 89.7퍼센트로 떨어지므로
// 검색할 때 반드시 켜야 합니다.
func quantParams() map[string]any {
	return map[string]any{
		"binary": map[string]any{"always_ram": true},
	}
}

// searchQuant는 검색할 때 붙이는 재점수 설정입니다.
//
// 이진으로 후보를 넓게 뽑고 디스크의 원본으로 다시 점수를 매깁니다.
// 4배로 뽑을 때 int8과 사실상 같아졌고, 2배면 95.1퍼센트였습니다.
// 원본은 어차피 디스크에 있으므로 읽는 비용이 이미 값에 들어 있습니다.
func searchQuant() map[string]any {
	return map[string]any{
		"quantization": map[string]any{
			"rescore":      true,
			"oversampling": 4.0,
		},
	}
}

// moveVectorsToDisk는 예전에 만든 컬렉션을 지금 방식으로 옮깁니다.
//
// 담을 장수 계산이 "원본은 디스크, 줄인 것만 메모리"를 전제로 합니다.
// 만들 때 이 값을 넣기 전에 생긴 컬렉션은 그 전제 밖에 있어서, 같은
// 계산을 쓰면 실제보다 훨씬 넉넉하게 나옵니다.
//
// 지금 판은 값을 안 넣어도 원본을 memmap으로 두는 것으로 재서 확인했지만,
// 문서에 적힌 기본값이 아니라 판이 올라가며 바뀔 수 있습니다. 명시해 둡니다.
func (c *Client) moveVectorsToDisk(ctx context.Context, name string) error {
	// 이름 없는 기본 벡터라 빈 문자열을 키로 씁니다.
	// 이름 없는 기본 벡터라 빈 문자열을 키로 씁니다.
	body := map[string]any{
		"vectors":     map[string]any{"": map[string]any{"on_disk": true}},
		"hnsw_config": hnswParams(),
	}
	if c.quantize {
		body["quantization_config"] = quantParams()
	}
	if err := c.call(ctx, http.MethodPatch, "/collections/"+name, body, nil); err != nil {
		return fmt.Errorf("컬렉션 %s를 옮기지 못했습니다: %w", name, err)
	}
	c.log.Info("예전 컬렉션을 지금 방식으로 옮겼습니다. 재정리가 끝날 때까지 잠시 느릴 수 있습니다",
		slog.String("collection", name))
	return nil
}

func (c *Client) hasCollection(ctx context.Context, name string) (bool, error) {
	var payload struct {
		Result struct {
			Exists bool `json:"exists"`
		} `json:"result"`
	}
	if err := c.call(ctx, http.MethodGet, "/collections/"+name+"/exists", nil, &payload); err != nil {
		return false, err
	}
	return payload.Result.Exists, nil
}

func (c *Client) Upsert(ctx context.Context, collection string, points []domain.VectorPoint) error {
	if len(points) == 0 {
		return nil
	}

	items := make([]map[string]any, 0, len(points))
	for _, p := range points {
		if len(p.Vector) == 0 {
			return fmt.Errorf("image %d의 벡터가 비어 있습니다", p.ImageID)
		}
		items = append(items, map[string]any{
			"id":      p.ImageID,
			"vector":  p.Vector,
			"payload": p.Payload,
		})
	}

	body := map[string]any{"points": items}
	path := "/collections/" + collection + "/points?wait=true"
	if err := c.call(ctx, http.MethodPut, path, body, nil); err != nil {
		return fmt.Errorf("벡터 %d건 저장에 실패했습니다: %w", len(points), err)
	}
	return nil
}

func (c *Client) Search(ctx context.Context, collection string, vector []float32, limit int) ([]domain.VectorMatch, error) {
	if len(vector) == 0 {
		return nil, errors.New("검색 벡터가 비어 있습니다")
	}
	if limit <= 0 {
		limit = 5
	}

	body := map[string]any{
		"query":        vector,
		"limit":        limit,
		"with_payload": false,
		"params":       searchQuant(),
	}

	var payload struct {
		Result struct {
			Points []struct {
				ID    int64   `json:"id"`
				Score float32 `json:"score"`
			} `json:"points"`
		} `json:"result"`
	}
	path := "/collections/" + collection + "/points/query"
	if err := c.call(ctx, http.MethodPost, path, body, &payload); err != nil {
		return nil, fmt.Errorf("벡터 검색에 실패했습니다: %w", err)
	}

	out := make([]domain.VectorMatch, 0, len(payload.Result.Points))
	for _, p := range payload.Result.Points {
		out = append(out, domain.VectorMatch{ImageID: p.ID, Score: p.Score})
	}
	return out, nil
}

func (c *Client) Count(ctx context.Context, collection string) (int64, error) {
	var payload struct {
		Result struct {
			Count int64 `json:"count"`
		} `json:"result"`
	}
	path := "/collections/" + collection + "/points/count"
	if err := c.call(ctx, http.MethodPost, path, map[string]any{"exact": true}, &payload); err != nil {
		return 0, err
	}
	return payload.Result.Count, nil
}

func (c *Client) DeleteCollection(ctx context.Context, collection string) error {
	return c.call(ctx, http.MethodDelete, "/collections/"+collection, nil, nil)
}

func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("api-key", c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("Qdrant에 닿지 못했습니다: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("Qdrant 응답이 %d입니다: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("Qdrant 응답을 해석하지 못했습니다: %w", err)
	}
	return nil
}

func distanceName(raw string) string {
	switch strings.ToLower(raw) {
	case "", "cosine":
		return "Cosine"
	case "dot":
		return "Dot"
	case "euclid", "euclidean", "l2":
		return "Euclid"
	default:
		return "Cosine"
	}
}

// PayloadFor는 검색 결과 조립에 필요한 최소 정보를 벡터 옆에 함께 둡니다.
// PostgreSQL이 잠깐 느려도 대략의 결과를 낼 수 있게 하기 위한 사본입니다.
func PayloadFor(img domain.Image) map[string]any {
	return map[string]any{
		"image_id":       img.ID,
		"source_site":    img.SourceSite,
		"source_post_id": img.SourcePostID,
		"canonical_url":  img.CanonicalURL,
		"rating":         img.Rating,
		"phash":          img.PHash,
		"dhash":          img.DHash,
	}
}
