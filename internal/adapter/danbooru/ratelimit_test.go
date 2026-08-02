package danbooru

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 실측에서 동시 32로 올렸을 때 429가 쏟아졌습니다.
// 동시 수와 별개로 초당 요청 수 자체를 지켜야 합니다.
func TestRateLimiterCapsRequestsPerSecond(t *testing.T) {
	limiter := newRateLimiter(20, 1)
	ctx := context.Background()

	started := time.Now()
	for i := 0; i < 10; i++ {
		if err := limiter.wait(ctx); err != nil {
			t.Fatalf("대기 실패: %v", err)
		}
	}
	elapsed := time.Since(started)

	// 초당 20건이면 10건에 최소 450ms는 걸려야 합니다.
	if elapsed < 400*time.Millisecond {
		t.Fatalf("10건에 %v밖에 안 걸렸습니다. 제한이 걸리지 않았습니다", elapsed)
	}
}

// 몰아서 보내는 것은 허용해야 합니다. 잠깐 쉬었다가 재개하는 흐름이 흔합니다.
func TestRateLimiterAllowsBurst(t *testing.T) {
	limiter := newRateLimiter(10, 5)
	ctx := context.Background()

	started := time.Now()
	for i := 0; i < 5; i++ {
		limiter.wait(ctx)
	}
	if elapsed := time.Since(started); elapsed > 100*time.Millisecond {
		t.Fatalf("몰아서 보내는데 %v가 걸렸습니다", elapsed)
	}
}

// 동시에 여러 고루틴이 써도 전체 속도는 지켜져야 합니다.
func TestRateLimiterHoldsUnderConcurrency(t *testing.T) {
	limiter := newRateLimiter(50, 1)
	ctx := context.Background()

	var wg sync.WaitGroup
	started := time.Now()
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			limiter.wait(ctx)
		}()
	}
	wg.Wait()

	// 초당 50건이면 25건에 최소 460ms입니다.
	if elapsed := time.Since(started); elapsed < 400*time.Millisecond {
		t.Fatalf("동시 요청 25건이 %v만에 통과했습니다", elapsed)
	}
}

func TestRateLimiterRespectsContext(t *testing.T) {
	limiter := newRateLimiter(1, 1)
	limiter.wait(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if err := limiter.wait(ctx); err == nil {
		t.Fatal("컨텍스트가 끝나면 기다림을 멈춰야 합니다")
	}
}

func TestNilLimiterIsNoop(t *testing.T) {
	var limiter *rateLimiter
	if err := limiter.wait(context.Background()); err != nil {
		t.Fatalf("제한을 끄면 그냥 통과해야 합니다: %v", err)
	}
	limiter.penalize(time.Second)
}

// 429를 받으면 즉시 속도를 늦춰야 합니다. 계속 밀어붙이면 차단당합니다.
func TestPenalizeDelaysNextRequest(t *testing.T) {
	limiter := newRateLimiter(1000, 1)
	limiter.penalize(150 * time.Millisecond)

	started := time.Now()
	limiter.wait(context.Background())

	if elapsed := time.Since(started); elapsed < 100*time.Millisecond {
		t.Fatalf("제재 후 %v만에 다시 보냈습니다", elapsed)
	}
}

// 서버가 Retry-After로 알려주면 그 값을 따라야 합니다.
func TestClientHonorsRetryAfter(t *testing.T) {
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("retry-after", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client, err := New(server.Client(), Options{
		BaseURL: server.URL, Retries: 2, RatePerSecond: 1000, Burst: 100,
	})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}

	started := time.Now()
	if _, err := client.PostsAfter(context.Background(), 0, "", 1); err != nil {
		t.Fatalf("재시도 후에도 실패했습니다: %v", err)
	}

	// Retry-After 1초를 지켜야 합니다. 재시도 백오프 자체는 1초 미만입니다.
	if elapsed := time.Since(started); elapsed < 900*time.Millisecond {
		t.Fatalf("Retry-After를 무시하고 %v만에 다시 보냈습니다", elapsed)
	}
}

// 내려받기에도 같은 상한이 걸려야 합니다. 요청 수의 대부분이 이미지입니다.
func TestDownloadIsRateLimited(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("image"))
	}))
	defer server.Close()

	client, _ := New(server.Client(), Options{
		BaseURL: server.URL, RatePerSecond: 20, Burst: 1,
	})

	started := time.Now()
	for i := 0; i < 8; i++ {
		if _, err := client.Download(context.Background(), server.URL+"/a.jpg", 1024); err != nil {
			t.Fatalf("내려받기 실패: %v", err)
		}
	}

	if elapsed := time.Since(started); elapsed < 300*time.Millisecond {
		t.Fatalf("8건이 %v만에 나갔습니다. 내려받기에 제한이 없습니다", elapsed)
	}
}

func TestNewRejectsNegativeRate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	if _, err := New(server.Client(), Options{BaseURL: server.URL, RatePerSecond: -1}); err == nil {
		t.Fatal("음수 속도는 거부해야 합니다")
	}
}

// 계정을 넣으면 인증 헤더를 붙여야 합니다. 익명보다 높은 한도를 받습니다.
func TestAuthenticatedRequestsCarryHeader(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("authorization")
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client, err := New(server.Client(), Options{
		BaseURL: server.URL, Login: "someone", APIKey: "secret-key",
	})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}
	if !client.Authenticated() {
		t.Fatal("인증 상태로 표시해야 합니다")
	}
	if _, err := client.PostsAfter(context.Background(), 0, "", 1); err != nil {
		t.Fatalf("조회 실패: %v", err)
	}

	// Basic c29tZW9uZTpzZWNyZXQta2V5 == someone:secret-key
	if !strings.HasPrefix(seen, "Basic ") {
		t.Fatalf("인증 헤더가 %q입니다", seen)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(seen, "Basic "))
	if err != nil {
		t.Fatalf("헤더를 해석하지 못했습니다: %v", err)
	}
	if string(decoded) != "someone:secret-key" {
		t.Fatalf("자격 증명이 %q입니다", decoded)
	}
}

// 이미지 내려받기에도 인증이 붙어야 합니다. 요청의 대부분이 이미지입니다.
func TestDownloadCarriesAuthHeader(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("authorization")
		w.Write([]byte("image"))
	}))
	defer server.Close()

	client, _ := New(server.Client(), Options{
		BaseURL: server.URL, Login: "a", APIKey: "b",
	})
	if _, err := client.Download(context.Background(), server.URL+"/x.jpg", 1024); err != nil {
		t.Fatalf("내려받기 실패: %v", err)
	}
	if !strings.HasPrefix(seen, "Basic ") {
		t.Fatalf("인증 헤더가 %q입니다", seen)
	}
}

// 한쪽만 넣으면 인증이 되지 않으니 조용히 넘어가면 안 됩니다.
func TestPartialCredentialsAreRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	cases := map[string]Options{
		"계정만": {BaseURL: server.URL, Login: "someone"},
		"키만":  {BaseURL: server.URL, APIKey: "secret"},
	}
	for name, opts := range cases {
		if _, err := New(server.Client(), opts); err == nil {
			t.Errorf("%s인 경우를 거부해야 합니다", name)
		}
	}
}

func TestAnonymousClientSendsNoAuth(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("authorization")
		w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client, _ := New(server.Client(), Options{BaseURL: server.URL})
	if client.Authenticated() {
		t.Fatal("자격 증명 없이 인증 상태로 표시하면 안 됩니다")
	}
	client.PostsAfter(context.Background(), 0, "", 1)
	if seen != "" {
		t.Fatalf("인증 헤더가 %q로 붙었습니다", seen)
	}
}
