package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"saucedust/internal/domain"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func sampleBatch() []domain.IndexedImage {
	return []domain.IndexedImage{{
		Image: domain.Image{
			SourceSite: "danbooru", SourcePostID: 12345,
			CanonicalURL: "https://danbooru.example/posts/12345",
			PHash:        "f0f0f0f0f0f0f0f0", Rating: "g",
			Tags: []string{"1girl", "solo"}, Width: 800, Height: 1200,
		},
		Vectors: []domain.Vector{
			{ModelID: "copy-model", Values: []float32{0.1, -0.25, 0.5, 0.75}},
		},
		Thumb: []byte("thumbnail-bytes"),
	}}
}

// 전송 형식을 거쳐도 값이 그대로 돌아와야 합니다.
func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := sampleBatch()[0]

	encoded := EncodeItem(original)
	payload, err := json.Marshal(encoded)
	if err != nil {
		t.Fatalf("직렬화 실패: %v", err)
	}

	var wire IngestItem
	if err := json.Unmarshal(payload, &wire); err != nil {
		t.Fatalf("역직렬화 실패: %v", err)
	}
	decoded, err := wire.Decode()
	if err != nil {
		t.Fatalf("복원 실패: %v", err)
	}

	switch {
	case decoded.Image.SourcePostID != original.Image.SourcePostID:
		t.Fatalf("게시물 번호가 %d입니다", decoded.Image.SourcePostID)
	case decoded.Image.PHash != original.Image.PHash:
		t.Fatalf("해시가 %q입니다", decoded.Image.PHash)
	case len(decoded.Vectors) != 1:
		t.Fatalf("벡터가 %d개입니다", len(decoded.Vectors))
	case string(decoded.Thumb) != "thumbnail-bytes":
		t.Fatalf("축소본이 %q입니다", decoded.Thumb)
	}

	for i, want := range original.Vectors[0].Values {
		if got := decoded.Vectors[0].Values[i]; got != want {
			t.Fatalf("벡터 %d번째가 %v입니다. %v를 기대했습니다", i, got, want)
		}
	}
}

func TestDecodeRejectsBadInput(t *testing.T) {
	cases := map[string]IngestItem{
		"출처 없음": {Vectors: []WireVector{{ModelID: "m", Data: "AAAAAA=="}}},
		"벡터 없음": {SourceSite: "danbooru", SourcePostID: 1},
		"깨진 base64": {SourceSite: "danbooru", SourcePostID: 1,
			Vectors: []WireVector{{ModelID: "m", Data: "!!!not-base64!!!"}}},
		"길이 불일치": {SourceSite: "danbooru", SourcePostID: 1,
			Vectors: []WireVector{{ModelID: "m", Size: 99, Data: "AAAAAA=="}}},
	}
	for name, item := range cases {
		if _, err := item.Decode(); err == nil {
			t.Errorf("%s는 거부해야 합니다", name)
		}
	}
}

type recordingIngest struct {
	mu       sync.Mutex
	received []domain.IndexedImage
	fail     error
}

func (r *recordingIngest) Submit(_ context.Context, batch []domain.IndexedImage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.received = append(r.received, batch...)
	return nil
}

func (r *recordingIngest) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.received)
}

// 서버 쪽 ingest 처리기를 감싸는 최소 핸들러입니다.
// 실제 Server는 *app.Ingest를 요구하므로 여기서는 경로 계약만 확인합니다.
func ingestServer(t *testing.T, sink *recordingIngest, token string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ingest", func(w http.ResponseWriter, r *http.Request) {
		if token != "" && r.Header.Get("authorization") != "Bearer "+token {
			writeError(w, http.StatusUnauthorized, errors.New("인증이 필요합니다"))
			return
		}

		var req IngestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		batch := make([]domain.IndexedImage, 0, len(req.Items))
		for _, item := range req.Items {
			decoded, err := item.Decode()
			if err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
			batch = append(batch, decoded)
		}
		if err := sink.Submit(r.Context(), batch); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, domain.ErrModelMismatch) {
				status = http.StatusConflict
			}
			writeError(w, status, err)
			return
		}
		writeJSON(w, http.StatusOK, IngestResponse{Accepted: len(batch)})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestClientSubmitsBatch(t *testing.T) {
	sink := &recordingIngest{}
	server := ingestServer(t, sink, "")

	client, err := NewClient(ClientOptions{BaseURL: server.URL, NodeID: "worker-1"})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}
	if err := client.Submit(context.Background(), sampleBatch()); err != nil {
		t.Fatalf("전송 실패: %v", err)
	}
	if sink.count() != 1 {
		t.Fatalf("받은 건수가 %d입니다", sink.count())
	}
	if sink.received[0].Image.SourcePostID != 12345 {
		t.Fatalf("게시물 번호가 %d입니다", sink.received[0].Image.SourcePostID)
	}
}

func TestClientSendsToken(t *testing.T) {
	sink := &recordingIngest{}
	server := ingestServer(t, sink, "secret")

	client, _ := NewClient(ClientOptions{BaseURL: server.URL, Token: "secret"})
	if err := client.Submit(context.Background(), sampleBatch()); err != nil {
		t.Fatalf("전송 실패: %v", err)
	}
}

func TestClientReportsAuthFailure(t *testing.T) {
	server := ingestServer(t, &recordingIngest{}, "secret")

	client, _ := NewClient(ClientOptions{BaseURL: server.URL, Token: "wrong", Retries: 3})
	err := client.Submit(context.Background(), sampleBatch())
	if err == nil {
		t.Fatal("인증 실패는 오류여야 합니다")
	}
	if !strings.Contains(err.Error(), "인증") {
		t.Fatalf("오류 내용이 %q입니다", err)
	}
}

// 모델 구성이 다르면 재시도해도 소용없으므로 바로 멈춰야 합니다.
func TestClientDoesNotRetryModelMismatch(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		writeError(w, http.StatusConflict, domain.ErrModelMismatch)
	}))
	defer server.Close()

	client, _ := NewClient(ClientOptions{BaseURL: server.URL, Retries: 3})
	err := client.Submit(context.Background(), sampleBatch())
	if !errors.Is(err, domain.ErrModelMismatch) {
		t.Fatalf("모델 불일치를 알려야 하는데 %v입니다", err)
	}
	if hits != 1 {
		t.Fatalf("요청이 %d회입니다. 재시도하면 안 됩니다", hits)
	}
}

func TestClientRetriesServerError(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, IngestResponse{Accepted: 1})
	}))
	defer server.Close()

	client, _ := NewClient(ClientOptions{BaseURL: server.URL, Retries: 3})
	if err := client.Submit(context.Background(), sampleBatch()); err != nil {
		t.Fatalf("재시도 후에도 실패했습니다: %v", err)
	}
	if hits != 2 {
		t.Fatalf("요청이 %d회입니다", hits)
	}
}

func TestClientEmptyBatchIsNoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("빈 묶음에 요청을 보내면 안 됩니다")
	}))
	defer server.Close()

	client, _ := NewClient(ClientOptions{BaseURL: server.URL})
	if err := client.Submit(context.Background(), nil); err != nil {
		t.Fatalf("빈 묶음은 그냥 넘어가야 합니다: %v", err)
	}
}

type stubStats struct{}

func (stubStats) Ping(context.Context) error { return nil }

func (stubStats) CountImages(context.Context) (int64, error) { return 4242, nil }

func (stubStats) ListNodes(context.Context, time.Duration) ([]domain.Node, error) {
	return []domain.Node{
		{ID: "control-1", Role: domain.RoleControl, Status: "online",
			Device: domain.DeviceMPS, NetMode: domain.NetDirect,
			Concurrency: 4, HeartbeatAt: time.Now()},
		{ID: "worker-2", Role: domain.RoleWorker, Status: "offline",
			Device: domain.DeviceCUDA, NetMode: domain.NetFragment,
			Concurrency: 16, HeartbeatAt: time.Now().Add(-10 * time.Minute)},
	}, nil
}

func (stubStats) FleetSummary(context.Context, time.Duration) (domain.FleetSummary, error) {
	return domain.FleetSummary{
		Nodes: map[string]domain.NodeThroughput{
			"control-1": {NodeID: "control-1", Saved: 900, PerSecond: 3.5},
		},
		TotalPerSecond: 3.5,
	}, nil
}

func (stubStats) CrawlSummary(context.Context, string, string) (domain.CrawlSummary, error) {
	return domain.CrawlSummary{
		RangesCompleted: 120, RangesRunning: 3, RangesFailed: 1,
		RetryPending: 7, HighWatermark: 10_000_000, BackfillBefore: 6_000_000,
		VectorsByModel: map[string]int64{"copy-model": 4242},
	}, nil
}

func newTestServer(t *testing.T, token string) http.Handler {
	t.Helper()
	server, err := New(Config{
		Bind: "127.0.0.1:0", Token: token,
		SourceSite: "danbooru", ScopeKey: "default",
	}, Deps{Stats: stubStats{}, Images: stubImages{}, Log: quiet()})
	if err != nil {
		t.Fatalf("서버 생성 실패: %v", err)
	}
	return server.routes()
}

type stubImages struct{}

func (stubImages) UpsertImage(context.Context, *domain.Image) (int64, error) { return 0, nil }

func (stubImages) UpsertImages(context.Context, []*domain.Image) ([]int64, error) {
	return nil, nil
}

func (stubImages) ExistingPostIDs(context.Context, string, []int64) (map[int64]int64, error) {
	return nil, nil
}

func (stubImages) ImageByID(_ context.Context, id int64) (*domain.Image, error) {
	if id != 42 {
		return nil, domain.ErrNotFound
	}
	return &domain.Image{
		ID: 42, SourceSite: "danbooru", SourcePostID: 12345,
		CanonicalURL: "https://danbooru.example/posts/12345",
		PHash:        "f0f0f0f0f0f0f0f0", Rating: "g", ThumbPath: "a/b/42.jpg",
	}, nil
}

func (stubImages) ImageBySource(_ context.Context, site string, postID int64) (*domain.Image, error) {
	if site != "danbooru" || postID != 12345 {
		return nil, domain.ErrNotFound
	}
	return &domain.Image{ID: 42, SourceSite: site, SourcePostID: postID}, nil
}

func (stubImages) ImagesByIDs(context.Context, []int64) (map[int64]domain.Image, error) {
	return nil, nil
}

func (stubImages) CountImages(context.Context) (int64, error) { return 0, nil }

func TestStatsEndpoint(t *testing.T) {
	handler := newTestServer(t, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/stats", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("응답이 %d입니다: %s", rec.Code, rec.Body)
	}

	var stats StatsView
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("응답 해석 실패: %v", err)
	}
	switch {
	case stats.Images != 4242:
		t.Fatalf("이미지 수가 %d입니다", stats.Images)
	case stats.OnlineNodes != 1 || stats.TotalNodes != 2:
		t.Fatalf("노드 수가 %d/%d입니다", stats.OnlineNodes, stats.TotalNodes)
	case stats.HighWatermark != 10_000_000:
		t.Fatalf("진행점이 %d입니다", stats.HighWatermark)
	case stats.RetryQueue != 7:
		t.Fatalf("재시도 대기가 %d입니다", stats.RetryQueue)
	}
}

func TestNodesEndpoint(t *testing.T) {
	handler := newTestServer(t, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/nodes", nil))

	var nodes []NodeView
	if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
		t.Fatalf("응답 해석 실패: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("노드가 %d개입니다", len(nodes))
	}
	if nodes[0].Saved != 900 || nodes[0].PerSecond != 3.5 {
		t.Fatalf("처리량이 %+v입니다", nodes[0])
	}
	if nodes[1].NetMode != "frag" {
		t.Fatalf("경로가 %q입니다", nodes[1].NetMode)
	}
}

func TestAuthBlocksWithoutToken(t *testing.T) {
	handler := newTestServer(t, "secret")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/stats", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("토큰 없이 %d가 나왔습니다. 401을 기대했습니다", rec.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
	req.Header.Set("authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("올바른 토큰으로 %d가 나왔습니다", rec.Code)
	}
}

func TestHealthNeedsNoToken(t *testing.T) {
	handler := newTestServer(t, "secret")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("상태 확인이 %d입니다", rec.Code)
	}
}

// 대시보드가 바이너리에 실제로 들어갔는지 확인합니다.
func TestDashboardIsEmbedded(t *testing.T) {
	handler := newTestServer(t, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("대시보드 응답이 %d입니다", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"saucedust", "수집 구간", "/v1/stats", "/v1/nodes"} {
		if !strings.Contains(body, want) {
			t.Errorf("대시보드에 %q가 없습니다", want)
		}
	}
	if rec.Header().Get("cache-control") != "no-store" {
		t.Error("대시보드는 캐시하지 않아야 합니다")
	}
}

// 준비 상태는 인증 없이 볼 수 있어야 합니다. 감시 도구가 씁니다.
func TestReadyNeedsNoToken(t *testing.T) {
	handler := newTestServer(t, "secret")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("준비 확인이 %d입니다: %s", rec.Code, rec.Body)
	}

	var view ReadyView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("응답 해석 실패: %v", err)
	}
	if !view.Ready {
		t.Fatalf("준비되지 않았다고 나옵니다: %+v", view.Checks)
	}
	if view.Checks["postgres"] != "ok" {
		t.Fatalf("저장소 상태가 %q입니다", view.Checks["postgres"])
	}
}

// 부품이 죽어 있으면 503을 내야 배치 도구가 알아챕니다.
func TestReadyReportsFailure(t *testing.T) {
	server, err := New(Config{Bind: "127.0.0.1:0", SourceSite: "danbooru", ScopeKey: "default"},
		Deps{Stats: brokenStats{}, Images: stubImages{}, Log: quiet()})
	if err != nil {
		t.Fatalf("서버 생성 실패: %v", err)
	}

	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("응답이 %d입니다. 503을 기대했습니다", rec.Code)
	}
	var view ReadyView
	json.Unmarshal(rec.Body.Bytes(), &view)
	if view.Ready {
		t.Fatal("죽어 있는데 준비됐다고 합니다")
	}
}

type brokenStats struct{ stubStats }

func (brokenStats) Ping(context.Context) error { return errors.New("연결이 끊겼습니다") }

func TestImageByID(t *testing.T) {
	handler := newTestServer(t, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/images/42", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("응답이 %d입니다: %s", rec.Code, rec.Body)
	}

	var view ImageView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("응답 해석 실패: %v", err)
	}
	switch {
	case !view.Indexed:
		t.Fatal("수집된 것으로 표시해야 합니다")
	case view.SourcePostID != 12345:
		t.Fatalf("게시물 번호가 %d입니다", view.SourcePostID)
	case !view.HasThumb:
		t.Fatal("축소본이 있다고 표시해야 합니다")
	}
}

// 찾지 못한 것은 오류가 아니라 404이며, 아직 수집되지 않았다고 알려야 합니다.
// 검색 결과가 이상할 때 이것으로 원인을 가립니다.
func TestImageNotFoundSaysNotIndexed(t *testing.T) {
	handler := newTestServer(t, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/images/999", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("응답이 %d입니다", rec.Code)
	}

	var view ImageView
	json.Unmarshal(rec.Body.Bytes(), &view)
	if view.Indexed {
		t.Fatal("없는 이미지를 수집됐다고 합니다")
	}
}

func TestImageBySource(t *testing.T) {
	handler := newTestServer(t, "")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/v1/images/source/danbooru/12345", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("응답이 %d입니다: %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec,
		httptest.NewRequest(http.MethodGet, "/v1/images/source/danbooru/99999", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("없는 게시물에 %d가 나왔습니다", rec.Code)
	}
}

func TestImageRejectsNonNumericID(t *testing.T) {
	handler := newTestServer(t, "")

	for _, path := range []string{"/v1/images/abc", "/v1/images/source/danbooru/xyz"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s에서 %d가 나왔습니다. 400을 기대했습니다", path, rec.Code)
		}
	}
}

func TestImageEndpointNeedsToken(t *testing.T) {
	handler := newTestServer(t, "secret")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/images/42", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("토큰 없이 %d가 나왔습니다", rec.Code)
	}
}
