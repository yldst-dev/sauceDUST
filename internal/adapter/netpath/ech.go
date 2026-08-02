package netpath

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const defaultDoH = "https://cloudflare-dns.com/dns-query"

// echResolver는 DNS HTTPS 레코드에서 ECH 설정을 가져옵니다.
// ECH를 쓰면 TLS 인사말의 도메인 이름 자체가 암호화되므로 검사 장비가 볼 수 없습니다.
// 상대 서버가 지원하지 않으면 설정이 없고, 그때는 조각내기로 넘어갑니다.
type echResolver struct {
	endpoint string
	client   *http.Client
	ttl      time.Duration

	mu    sync.RWMutex
	cache map[string]echEntry
}

type echEntry struct {
	config  []byte
	expires time.Time
}

func newECHResolver(endpoint string, ttl time.Duration) *echResolver {
	if endpoint == "" {
		endpoint = defaultDoH
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	return &echResolver{
		endpoint: endpoint,
		client:   &http.Client{Timeout: 10 * time.Second},
		ttl:      ttl,
		cache:    map[string]echEntry{},
	}
}

func (r *echResolver) ConfigFor(ctx context.Context, host string) ([]byte, error) {
	r.mu.RLock()
	entry, ok := r.cache[host]
	r.mu.RUnlock()
	if ok && time.Now().Before(entry.expires) {
		if len(entry.config) == 0 {
			return nil, fmt.Errorf("%s는 ECH를 제공하지 않습니다", host)
		}
		return entry.config, nil
	}

	config, err := r.lookup(ctx, host)

	r.mu.Lock()
	r.cache[host] = echEntry{config: config, expires: time.Now().Add(r.ttl)}
	r.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return config, nil
}

func (r *echResolver) lookup(ctx context.Context, host string) ([]byte, error) {
	endpoint := r.endpoint + "?" + url.Values{
		"name": {host},
		"type": {"HTTPS"},
	}.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("accept", "application/dns-json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTPS 레코드 조회에 실패했습니다: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTPS 레코드 조회 응답이 %d입니다", resp.StatusCode)
	}

	var payload struct {
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("HTTPS 레코드 응답을 해석하지 못했습니다: %w", err)
	}

	for _, answer := range payload.Answer {
		if answer.Type != 65 {
			continue
		}
		if raw, ok := parseECHParam(answer.Data); ok {
			config, err := base64.StdEncoding.DecodeString(raw)
			if err != nil {
				return nil, fmt.Errorf("ECH 설정을 해석하지 못했습니다: %w", err)
			}
			return config, nil
		}
	}
	return nil, fmt.Errorf("%s의 HTTPS 레코드에 ECH 설정이 없습니다", host)
}

// parseECHParam은 SVCB 표현형 문자열에서 ech 값만 뽑습니다.
// 예: `1 . alpn="h3,h2" ech="AEX+DQBB..." ipv4hint=...`
func parseECHParam(data string) (string, bool) {
	idx := strings.Index(data, "ech=")
	if idx < 0 {
		return "", false
	}
	rest := data[idx+len("ech="):]
	if rest == "" {
		return "", false
	}
	if rest[0] == '"' {
		end := strings.IndexByte(rest[1:], '"')
		if end < 0 {
			return "", false
		}
		return rest[1 : 1+end], true
	}
	if end := strings.IndexAny(rest, " \t"); end >= 0 {
		return rest[:end], true
	}
	return rest, true
}
