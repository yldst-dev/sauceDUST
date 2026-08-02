// Package danbooru는 Danbooru 수집 어댑터입니다.
// 속도 제한과 인증도 여기서 다룹니다.
package danbooru

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"saucedust/internal/domain"
)

const (
	Site     = "danbooru"
	pageSize = 200
)

// Doer는 실제 요청을 보내는 쪽입니다. netpath.Chain이 이 모양을 만족합니다.
// 이렇게 두면 어댑터가 우회 경로 선택 방식을 전혀 몰라도 됩니다.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

type Options struct {
	BaseURL   string
	UserAgent string
	Retries   int
	// RatePerSecond는 이 클라이언트가 내는 초당 요청 수 상한입니다.
	// API 조회와 이미지 내려받기를 합쳐서 셉니다. 상대가 IP 단위로 제한하므로
	// 나눠 세면 의미가 없습니다. 0이면 제한하지 않습니다.
	RatePerSecond float64
	Burst         int
	// Login과 APIKey를 넣으면 인증된 요청을 보냅니다.
	// Danbooru는 익명 요청보다 인증 요청에 더 높은 한도를 줍니다.
	Login  string
	APIKey string
}

type Client struct {
	base      string
	userAgent string
	retries   int
	http      Doer
	limiter   *rateLimiter
	// auth는 Basic 인증 헤더 값입니다. 비어 있으면 익명으로 요청합니다.
	auth string
}

// Authenticated는 인증된 요청을 보내는지 알려줍니다.
func (c *Client) Authenticated() bool { return c.auth != "" }

func New(doer Doer, opts Options) (*Client, error) {
	if doer == nil {
		return nil, errors.New("HTTP 실행기가 없습니다")
	}
	if opts.BaseURL == "" {
		opts.BaseURL = "https://danbooru.donmai.us"
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "saucedust/0.2"
	}
	if opts.Retries <= 0 {
		opts.Retries = 3
	}
	if opts.RatePerSecond < 0 {
		return nil, errors.New("초당 요청 수는 0 이상이어야 합니다")
	}
	client := &Client{
		base:      strings.TrimRight(opts.BaseURL, "/"),
		userAgent: opts.UserAgent,
		retries:   opts.Retries,
		http:      doer,
		limiter:   newRateLimiter(opts.RatePerSecond, opts.Burst),
	}

	// 둘 중 하나만 있으면 인증이 되지 않으니 조용히 넘어가지 말고 알립니다.
	hasLogin := strings.TrimSpace(opts.Login) != ""
	hasKey := strings.TrimSpace(opts.APIKey) != ""
	switch {
	case hasLogin != hasKey:
		return nil, errors.New("Danbooru 인증에는 계정 이름과 API 키가 모두 필요합니다")
	case hasLogin && hasKey:
		client.auth = "Basic " + base64.StdEncoding.EncodeToString(
			[]byte(opts.Login+":"+opts.APIKey))
	}
	return client, nil
}

// setHeaders는 모든 요청에 공통으로 붙는 것을 답니다.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("user-agent", c.userAgent)
	if c.auth != "" {
		req.Header.Set("authorization", c.auth)
	}
}

func (c *Client) Site() string { return Site }

func (c *Client) LatestPostID(ctx context.Context) (int64, error) {
	posts, err := c.fetch(ctx, "order:id_desc", 1)
	if err != nil {
		return 0, err
	}
	if len(posts) == 0 {
		return 0, errors.New("최신 게시물을 찾지 못했습니다")
	}
	return posts[0].PostID, nil
}

// PostsInRange는 구간을 최신순으로 훑습니다. 페이지 번호 대신 직전 묶음의 가장
// 작은 id보다 작은 조건을 붙이므로, 수집 중 새 글이 올라와도 밀리지 않습니다.
func (c *Client) PostsInRange(ctx context.Context, lowerID, upperID int64, tags string) ([]domain.SourcePost, error) {
	if lowerID > upperID {
		return nil, fmt.Errorf("구간이 뒤집혔습니다: %d~%d", lowerID, upperID)
	}

	var (
		out    []domain.SourcePost
		cursor = upperID + 1
	)
	for cursor > lowerID {
		query := joinTags(tags,
			"order:id_desc",
			fmt.Sprintf("id:>=%d", lowerID),
			fmt.Sprintf("id:<%d", cursor))

		page, err := c.fetch(ctx, query, pageSize)
		if err != nil {
			return out, err
		}
		if len(page) == 0 {
			break
		}

		lowest := page[0].PostID
		for _, post := range page {
			if post.PostID < lowest {
				lowest = post.PostID
			}
		}
		out = append(out, page...)

		if lowest <= lowerID || len(page) < pageSize {
			break
		}
		cursor = lowest
	}
	return out, nil
}

// PostsAfter는 워터마크보다 새로 올라온 글을 오래된 순으로 돌려줍니다.
func (c *Client) PostsAfter(ctx context.Context, afterID int64, tags string, limit int) ([]domain.SourcePost, error) {
	if limit <= 0 || limit > pageSize {
		limit = pageSize
	}
	query := joinTags(tags, "order:id_asc", fmt.Sprintf("id:>%d", afterID))
	return c.fetch(ctx, query, limit)
}

func (c *Client) PostByID(ctx context.Context, postID int64) (*domain.SourcePost, error) {
	endpoint := fmt.Sprintf("%s/posts/%d.json", c.base, postID)

	body, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var raw apiPost
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("게시물 %d 응답을 해석하지 못했습니다: %w", postID, err)
	}
	post := raw.toDomain(c.base)
	return &post, nil
}

func (c *Client) Download(ctx context.Context, target string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = 32 << 20
	}

	if err := c.limiter.wait(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("이미지를 받지 못했습니다: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.observe(resp)
		return nil, &StatusError{Code: resp.StatusCode, URL: target}
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("이미지가 너무 큽니다: %d바이트", resp.ContentLength)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("이미지를 읽지 못했습니다: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("이미지가 %d바이트 제한을 넘었습니다", maxBytes)
	}
	return data, nil
}

func (c *Client) fetch(ctx context.Context, tags string, limit int) ([]domain.SourcePost, error) {
	params := url.Values{"limit": {strconv.Itoa(limit)}}
	if tags != "" {
		params.Set("tags", tags)
	}
	endpoint := c.base + "/posts.json?" + params.Encode()

	body, err := c.get(ctx, endpoint)
	if err != nil {
		return nil, err
	}

	var raw []apiPost
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("목록 응답을 해석하지 못했습니다: %w", err)
	}

	out := make([]domain.SourcePost, 0, len(raw))
	for _, item := range raw {
		if item.ID == 0 {
			continue
		}
		out = append(out, item.toDomain(c.base))
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, endpoint string) ([]byte, error) {
	var lastErr error

	for attempt := 0; attempt < c.retries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, backoff(attempt)); err != nil {
				return nil, err
			}
		}
		if err := c.limiter.wait(ctx); err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)
		req.Header.Set("accept", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		c.observe(resp)
		_ = resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusOK:
			if readErr != nil {
				lastErr = readErr
				continue
			}
			return body, nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = &StatusError{Code: resp.StatusCode, URL: endpoint}
			continue
		default:
			return nil, &StatusError{Code: resp.StatusCode, URL: endpoint}
		}
	}
	return nil, fmt.Errorf("요청이 %d번 모두 실패했습니다: %w", c.retries, lastErr)
}

// observe는 상대가 속도를 줄이라고 알려줬는지 보고 즉시 반영합니다.
// Retry-After가 있으면 그 값을, 없으면 기본 대기를 씁니다.
func (c *Client) observe(resp *http.Response) {
	if resp.StatusCode != http.StatusTooManyRequests &&
		resp.StatusCode != http.StatusServiceUnavailable {
		return
	}

	pause := defaultThrottlePause
	if raw := resp.Header.Get("retry-after"); raw != "" {
		if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			pause = time.Duration(secs) * time.Second
		}
	}
	c.limiter.penalize(pause)
}

// defaultThrottlePause는 Retry-After가 없을 때 쉬는 시간입니다.
const defaultThrottlePause = 10 * time.Second

type StatusError struct {
	Code int
	URL  string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("응답 상태가 %d입니다 (%s)", e.Code, e.URL)
}

// Retryable은 잠시 뒤 다시 시도할 가치가 있는 상태인지 알려줍니다.
func (e *StatusError) Retryable() bool {
	return e.Code == http.StatusTooManyRequests || e.Code >= 500
}

// Throttled는 상대가 속도를 줄이라고 알려준 경우입니다.
// 동시성 조절기가 이 신호를 보고 한도를 절반으로 낮춥니다.
func (e *StatusError) Throttled() bool {
	return e.Code == http.StatusTooManyRequests || e.Code == http.StatusServiceUnavailable
}

func joinTags(base string, extra ...string) string {
	parts := make([]string, 0, len(extra)+1)
	if trimmed := strings.TrimSpace(base); trimmed != "" {
		parts = append(parts, trimmed)
	}
	parts = append(parts, extra...)
	return strings.Join(parts, " ")
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
