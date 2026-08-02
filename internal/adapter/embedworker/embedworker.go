// Package embedworker는 같은 노드에서 도는 Python 임베딩 워커에 붙습니다.
package embedworker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"saucedust/internal/domain"
)

// Client는 같은 노드에서 도는 Python 임베딩 워커에 붙습니다.
// 워커가 이미지를 한 번만 디코드해서 모든 모델의 벡터와 지각 해시, 축소본을
// 한꺼번에 돌려주므로 Go 쪽에서는 이미지 처리를 하지 않습니다.
type Client struct {
	base    string
	http    *http.Client
	retries int
}

type Options struct {
	BaseURL string
	Timeout time.Duration
	Retries int
}

func New(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, errors.New("임베딩 워커 주소가 비어 있습니다")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 120 * time.Second
	}
	if opts.Retries <= 0 {
		opts.Retries = 3
	}
	return &Client{
		base:    strings.TrimRight(opts.BaseURL, "/"),
		http:    &http.Client{Timeout: opts.Timeout},
		retries: opts.Retries,
	}, nil
}

func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("임베딩 워커에 닿지 못했습니다: %w", err)
	}
	defer resp.Body.Close()
	// 본문을 비워야 연결이 재사용됩니다. 내용은 쓰지 않습니다.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("임베딩 워커 상태가 %d입니다", resp.StatusCode)
	}
	return nil
}

// Describe는 워커가 실제로 올린 모델 목록과 장치를 알려줍니다.
// 노드마다 다른 모델이 올라가면 벡터를 비교할 수 없으므로 시작 시 확인합니다.
func (c *Client) Describe(ctx context.Context) (domain.EmbedderInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/info", nil)
	if err != nil {
		return domain.EmbedderInfo{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return domain.EmbedderInfo{}, fmt.Errorf("워커 정보를 받지 못했습니다: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return domain.EmbedderInfo{}, fmt.Errorf("워커 정보 응답이 %d입니다", resp.StatusCode)
	}

	var payload infoResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return domain.EmbedderInfo{}, fmt.Errorf("워커 정보를 해석하지 못했습니다: %w", err)
	}

	info := domain.EmbedderInfo{Device: domain.Device(payload.Device)}
	for _, m := range payload.Models {
		info.Models = append(info.Models, domain.EmbeddingModel{
			ID:         m.ID,
			Kind:       domain.ModelKind(m.Kind),
			Backend:    m.Backend,
			Checkpoint: m.Checkpoint,
			VectorSize: m.VectorSize,
			InputSize:  m.InputSize,
		})
	}
	if len(info.Models) == 0 {
		return info, errors.New("워커가 보고한 모델이 없습니다")
	}
	return info, nil
}

// Embed는 인덱싱용입니다. 축소본까지 함께 받습니다.
func (c *Client) Embed(ctx context.Context, image []byte) (domain.EmbedResult, error) {
	return c.embed(ctx, image, true)
}

// EmbedQuery는 검색 질의용입니다. 축소본을 만들지 않아 그만큼 빠릅니다.
// 질의 이미지는 보관하지 않으므로 축소본이 필요 없습니다.
func (c *Client) EmbedQuery(ctx context.Context, image []byte) (domain.EmbedResult, error) {
	return c.embed(ctx, image, false)
}

func (c *Client) embed(ctx context.Context, image []byte, wantThumb bool) (domain.EmbedResult, error) {
	if len(image) == 0 {
		return domain.EmbedResult{}, errors.New("빈 이미지는 처리할 수 없습니다")
	}

	var lastErr error
	for attempt := 0; attempt < c.retries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, backoff(attempt)); err != nil {
				return domain.EmbedResult{}, err
			}
		}

		result, retryable, err := c.embedOnce(ctx, image, wantThumb)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !retryable {
			return domain.EmbedResult{}, err
		}
	}
	return domain.EmbedResult{}, fmt.Errorf("임베딩이 %d번 모두 실패했습니다: %w", c.retries, lastErr)
}

func (c *Client) embedOnce(ctx context.Context, image []byte, wantThumb bool) (domain.EmbedResult, bool, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", "image")
	if err != nil {
		return domain.EmbedResult{}, false, err
	}
	if _, err := part.Write(image); err != nil {
		return domain.EmbedResult{}, false, err
	}
	if err := writer.Close(); err != nil {
		return domain.EmbedResult{}, false, err
	}

	endpoint := c.base + "/embed"
	if !wantThumb {
		endpoint += "?thumb=0"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return domain.EmbedResult{}, false, err
	}
	req.Header.Set("content-type", writer.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return domain.EmbedResult{}, true, fmt.Errorf("임베딩 요청에 실패했습니다: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		retryable := resp.StatusCode >= 500
		return domain.EmbedResult{}, retryable,
			fmt.Errorf("임베딩 응답이 %d입니다: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	}

	var payload embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return domain.EmbedResult{}, true, fmt.Errorf("임베딩 응답을 해석하지 못했습니다: %w", err)
	}

	result := domain.EmbedResult{
		Hashes: domain.Hashes{
			PHash:  payload.PHash,
			DHash:  payload.DHash,
			Width:  payload.Width,
			Height: payload.Height,
		},
	}
	for _, v := range payload.Vectors {
		raw, err := base64.StdEncoding.DecodeString(v.Data)
		if err != nil {
			return domain.EmbedResult{}, false,
				fmt.Errorf("모델 %s 벡터를 해석하지 못했습니다: %w", v.ModelID, err)
		}
		values, err := domain.DecodeVector(raw)
		if err != nil {
			return domain.EmbedResult{}, false,
				fmt.Errorf("모델 %s 벡터를 해석하지 못했습니다: %w", v.ModelID, err)
		}
		if v.Size > 0 && len(values) != v.Size {
			return domain.EmbedResult{}, false,
				fmt.Errorf("모델 %s 벡터 길이가 %d인데 %d라고 했습니다",
					v.ModelID, len(values), v.Size)
		}
		domain.Normalize(values)
		result.Vectors = append(result.Vectors, domain.Vector{ModelID: v.ModelID, Values: values})
	}
	if payload.Thumb != "" {
		thumb, err := base64.StdEncoding.DecodeString(payload.Thumb)
		if err != nil {
			return domain.EmbedResult{}, false, fmt.Errorf("축소본을 해석하지 못했습니다: %w", err)
		}
		result.Thumb = thumb
	}
	if len(result.Vectors) == 0 {
		return domain.EmbedResult{}, false, errors.New("응답에 벡터가 없습니다")
	}
	return result, false, nil
}

type infoResponse struct {
	Device string `json:"device"`
	Models []struct {
		ID         string `json:"id"`
		Kind       string `json:"kind"`
		Backend    string `json:"backend"`
		Checkpoint string `json:"checkpoint"`
		VectorSize int    `json:"vector_size"`
		InputSize  int    `json:"input_size"`
	} `json:"models"`
}

// 벡터는 float32 원본 바이트를 base64로 받습니다.
// JSON 숫자 배열이면 768차원 기준 약 9KB이고 파싱도 느립니다.
type embedResponse struct {
	Vectors []struct {
		ModelID string `json:"model_id"`
		Size    int    `json:"size"`
		Data    string `json:"data"`
	} `json:"vectors"`
	PHash  string `json:"phash"`
	DHash  string `json:"dhash"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Thumb  string `json:"thumb_base64"`
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<attempt) * time.Second
	if d > 10*time.Second {
		return 10 * time.Second
	}
	return d
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
