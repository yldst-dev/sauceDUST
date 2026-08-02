package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"saucedust/internal/domain"
)

// Client는 작업 노드가 중앙 서버로 결과를 보낼 때 씁니다.
// app.VectorSink를 만족하므로 인덱서는 결과가 어디로 가는지 모릅니다.
type Client struct {
	base    string
	token   string
	nodeID  string
	http    *http.Client
	retries int
}

type ClientOptions struct {
	BaseURL string
	Token   string
	NodeID  string
	Timeout time.Duration
	Retries int
}

func NewClient(opts ClientOptions) (*Client, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, errors.New("중앙 서버 주소가 비어 있습니다")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 120 * time.Second
	}
	if opts.Retries <= 0 {
		opts.Retries = 3
	}
	return &Client{
		base:    strings.TrimRight(opts.BaseURL, "/"),
		token:   opts.Token,
		nodeID:  opts.NodeID,
		http:    &http.Client{Timeout: opts.Timeout},
		retries: opts.Retries,
	}, nil
}

func (c *Client) Submit(ctx context.Context, batch []domain.IndexedImage) error {
	if len(batch) == 0 {
		return nil
	}

	req := IngestRequest{NodeID: c.nodeID, Items: make([]IngestItem, 0, len(batch))}
	for _, item := range batch {
		req.Items = append(req.Items, EncodeItem(item))
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 0; attempt < c.retries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, backoff(attempt)); err != nil {
				return err
			}
		}

		retryable, err := c.post(ctx, payload)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable {
			return err
		}
	}
	return fmt.Errorf("결과 전송이 %d번 모두 실패했습니다: %w", c.retries, lastErr)
}

func (c *Client) post(ctx context.Context, payload []byte) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/v1/ingest", bytes.NewReader(payload))
	if err != nil {
		return false, err
	}
	req.Header.Set("content-type", "application/json")
	if c.token != "" {
		req.Header.Set("authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return true, fmt.Errorf("중앙 서버에 닿지 못했습니다: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		// 본문을 비워야 연결이 재사용됩니다. 내용은 쓰지 않습니다.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return false, nil
	}

	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	message := strings.TrimSpace(string(detail))

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return false, errors.New("중앙 서버 인증에 실패했습니다. SAUCEDUST_CONTROL_TOKEN을 확인하십시오")
	case resp.StatusCode == http.StatusConflict:
		// 모델 구성이 다르면 재시도해도 소용없습니다. 바로 멈춥니다.
		return false, fmt.Errorf("%w: %s", domain.ErrModelMismatch, message)
	case resp.StatusCode >= 500:
		return true, fmt.Errorf("중앙 서버 응답이 %d입니다: %s", resp.StatusCode, message)
	default:
		return false, fmt.Errorf("중앙 서버 응답이 %d입니다: %s", resp.StatusCode, message)
	}
}

func backoff(attempt int) time.Duration {
	d := time.Duration(1<<attempt) * time.Second
	if d > 30*time.Second {
		return 30 * time.Second
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
