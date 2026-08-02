package embedworker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"saucedust/internal/domain"
)

// encodeVectors는 워커가 보내는 형식 그대로 만듭니다.
// float32 원본 바이트를 base64로 싣습니다.
func encodeVector(values []float32) string {
	return base64.StdEncoding.EncodeToString(domain.EncodeVector(values))
}

func newClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	client, err := New(Options{BaseURL: server.URL, Timeout: 2 * time.Second, Retries: 3})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestNewRejectsEmptyURL(t *testing.T) {
	for _, url := range []string{"", "   "} {
		if _, err := New(Options{BaseURL: url}); err == nil {
			t.Errorf("%q를 받아들였습니다", url)
		}
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	client, err := New(Options{BaseURL: "http://x/"})
	if err != nil {
		t.Fatal(err)
	}
	if client.base != "http://x" {
		t.Errorf("주소가 %q입니다. 뒤 빗금을 떼야 합니다", client.base)
	}
	if client.retries < 1 {
		t.Errorf("재시도가 %d회입니다", client.retries)
	}
}

func TestHealth(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"정상", http.StatusOK, false},
		{"아직 올리는 중", http.StatusServiceUnavailable, true},
		{"오류", http.StatusInternalServerError, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/health" {
					t.Errorf("경로가 %q입니다", r.URL.Path)
				}
				w.WriteHeader(tc.status)
			}))

			err := client.Health(context.Background())
			if (err != nil) != tc.wantErr {
				t.Errorf("%v입니다", err)
			}
		})
	}
}

func TestDescribe(t *testing.T) {
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			t.Errorf("경로가 %q입니다", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device": "cuda",
			"models": []map[string]any{
				{"id": "dinov2-base", "kind": "copy", "backend": "cuda",
					"checkpoint": "facebook/dinov2-base", "vector_size": 768, "input_size": 224},
			},
		})
	}))

	info, err := client.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Device != domain.DeviceCUDA {
		t.Errorf("장치가 %q입니다", info.Device)
	}
	model, ok := info.Model("dinov2-base")
	if !ok {
		t.Fatal("모델을 찾지 못했습니다")
	}
	if model.Kind != domain.ModelCopy || model.VectorSize != 768 {
		t.Errorf("모델이 %+v입니다", model)
	}
}

// 모델이 없다는 응답을 그냥 넘기면, 아무것도 못 하는 노드가 정상인 척 등록됩니다.
func TestDescribeRejectsEmptyModelList(t *testing.T) {
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"device": "cpu", "models": []any{}})
	}))

	if _, err := client.Describe(context.Background()); err == nil {
		t.Error("모델이 없는 워커를 받아들였습니다")
	}
}

func TestDescribeRejectsBadJSON(t *testing.T) {
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("모델 목록입니다"))
	}))

	if _, err := client.Describe(context.Background()); err == nil {
		t.Error("JSON이 아닌 응답을 받아들였습니다")
	}
}

func TestEmbed(t *testing.T) {
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embed" {
			t.Errorf("경로가 %q입니다", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("방식이 %q입니다", r.Method)
		}
		if !strings.HasPrefix(r.Header.Get("content-type"), "multipart/form-data") {
			t.Errorf("형식이 %q입니다", r.Header.Get("content-type"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"vectors": []map[string]any{
				{"model_id": "a", "size": 4, "data": encodeVector([]float32{3, 4, 0, 0})},
			},
			"phash":        "f0f0f0f0f0f0f0f0",
			"dhash":        "0f0f0f0f0f0f0f0f",
			"width":        1920,
			"height":       1080,
			"thumb_base64": base64.StdEncoding.EncodeToString([]byte("jpeg")),
		})
	}))

	result, err := client.Embed(context.Background(), []byte("image"))
	if err != nil {
		t.Fatal(err)
	}

	if result.Hashes.PHash != "f0f0f0f0f0f0f0f0" || result.Hashes.Width != 1920 {
		t.Errorf("해시와 크기가 %+v입니다", result.Hashes)
	}
	if string(result.Thumb) != "jpeg" {
		t.Errorf("축소본이 %q입니다", result.Thumb)
	}

	// 장치별 미세 오차를 없애려고 받는 쪽에서 한 번 더 정규화합니다.
	values, ok := result.Vector("a")
	if !ok {
		t.Fatal("벡터를 찾지 못했습니다")
	}
	var sum float64
	for _, v := range values {
		sum += float64(v) * float64(v)
	}
	if math.Abs(sum-1) > 1e-6 {
		t.Errorf("길이의 제곱이 %v입니다. 정규화하지 않았습니다", sum)
	}
}

// 검색 질의는 보관하지 않으므로 축소본을 만들지 않습니다. 그만큼 빠릅니다.
func TestEmbedQuerySkipsThumb(t *testing.T) {
	var gotQuery atomic.Value
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery.Store(r.URL.RawQuery)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"vectors": []map[string]any{
				{"model_id": "a", "size": 2, "data": encodeVector([]float32{1, 0})},
			},
		})
	}))

	if _, err := client.EmbedQuery(context.Background(), []byte("image")); err != nil {
		t.Fatal(err)
	}
	if got := gotQuery.Load(); got != "thumb=0" {
		t.Errorf("질의 문자열이 %q입니다. thumb=0을 기대했습니다", got)
	}
}

func TestEmbedRejectsEmptyImage(t *testing.T) {
	client := newClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("빈 이미지로 요청을 보냈습니다")
	}))

	if _, err := client.Embed(context.Background(), nil); err == nil {
		t.Error("빈 이미지를 받아들였습니다")
	}
}

// 워커가 말한 길이와 실제 길이가 다르면 어느 한쪽이 잘린 것입니다.
// 그대로 저장하면 차원이 어긋난 점이 Qdrant에 들어갑니다.
func TestEmbedRejectsSizeMismatch(t *testing.T) {
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"vectors": []map[string]any{
				{"model_id": "a", "size": 768, "data": encodeVector([]float32{1, 0})},
			},
		})
	}))

	if _, err := client.Embed(context.Background(), []byte("image")); err == nil {
		t.Error("길이가 어긋난 벡터를 받아들였습니다")
	}
}

func TestEmbedRejectsBadPayload(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
	}{
		{"base64가 아님", map[string]any{
			"vectors": []map[string]any{{"model_id": "a", "size": 2, "data": "!!!!"}}}},
		{"바이트 길이가 4의 배수가 아님", map[string]any{
			"vectors": []map[string]any{{"model_id": "a", "size": 0,
				"data": base64.StdEncoding.EncodeToString([]byte{1, 2, 3})}}}},
		{"벡터가 없음", map[string]any{"vectors": []any{}, "phash": "f0f0f0f0f0f0f0f0"}},
		{"축소본이 base64가 아님", map[string]any{
			"vectors":      []map[string]any{{"model_id": "a", "size": 2, "data": encodeVector([]float32{1, 0})}},
			"thumb_base64": "!!!!"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(tc.body)
			}))
			if _, err := client.Embed(context.Background(), []byte("image")); err == nil {
				t.Error("잘못된 응답을 받아들였습니다")
			}
		})
	}
}

// 5xx는 워커가 잠깐 밀린 것일 수 있으므로 다시 시도합니다.
func TestEmbedRetriesServerError(t *testing.T) {
	var calls atomic.Int32
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"vectors": []map[string]any{
				{"model_id": "a", "size": 2, "data": encodeVector([]float32{1, 0})},
			},
		})
	}))

	if _, err := client.Embed(context.Background(), []byte("image")); err != nil {
		t.Fatalf("재시도로 살아나야 합니다: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("%d번 불렀습니다. 2번을 기대했습니다", got)
	}
}

// 4xx는 이 이미지 자체가 문제입니다. 다시 보내도 같은 답이 옵니다.
func TestEmbedDoesNotRetryClientError(t *testing.T) {
	var calls atomic.Int32
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("이미지를 열지 못했습니다"))
	}))

	_, err := client.Embed(context.Background(), []byte("image"))
	if err == nil {
		t.Fatal("오류가 나야 합니다")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("%d번 불렀습니다. 한 번만 보내야 합니다", got)
	}
	// 무엇이 왜 거절됐는지 알려야 로그만 보고도 원인을 찾습니다.
	if !strings.Contains(err.Error(), "400") ||
		!strings.Contains(err.Error(), "이미지를 열지 못했습니다") {
		t.Errorf("응답 내용을 알려주지 않습니다: %v", err)
	}
}

func TestEmbedGivesUpAfterRetries(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	client, err := New(Options{BaseURL: server.URL, Timeout: time.Second, Retries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Embed(context.Background(), []byte("image")); err == nil {
		t.Fatal("계속 실패하는데 성공했다고 합니다")
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("%d번 불렀습니다. 2번을 기대했습니다", got)
	}
}

// 재시도 사이의 대기 중에도 종료 신호를 바로 따라야 합니다.
func TestEmbedStopsOnCanceledContext(t *testing.T) {
	client := newClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := client.Embed(ctx, []byte("image")); err == nil {
		t.Fatal("오류가 나야 합니다")
	}
	// 재시도 대기는 1초, 2초입니다. 그대로 기다렸다면 훨씬 오래 걸립니다.
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("%v나 기다렸습니다. 취소를 따르지 않았습니다", elapsed)
	}
}

func TestBackoffIsCapped(t *testing.T) {
	for attempt := 0; attempt < 20; attempt++ {
		if got := backoff(attempt); got > 10*time.Second || got <= 0 {
			t.Errorf("%d번째 대기가 %v입니다", attempt, got)
		}
	}
}
