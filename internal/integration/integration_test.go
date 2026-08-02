package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"saucedust/internal/adapter/danbooru"
	"saucedust/internal/adapter/filestore"
	"saucedust/internal/adapter/httpapi"
	"saucedust/internal/adapter/postgres"
	"saucedust/internal/adapter/qdrant"
	"saucedust/internal/app"
	"saucedust/internal/domain"
)

// 이 파일은 실제 PostgreSQL에 가짜 Danbooru, 가짜 Qdrant, 가짜 임베딩 워커를
// 붙여 데이터가 끝까지 흐르는지 확인합니다.

var models = []domain.EmbeddingModel{
	{ID: "copy-model", Kind: domain.ModelCopy, VectorSize: 8,
		Distance: "cosine", Collection: "copy", InputSize: 224, Active: true},
	{ID: "semantic-model", Kind: domain.ModelSemantic, VectorSize: 8,
		Distance: "cosine", Collection: "semantic", InputSize: 224, Active: true},
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func openStore(t testing.TB) *postgres.Store {
	t.Helper()

	url := os.Getenv("SAUCEDUST_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SAUCEDUST_TEST_DATABASE_URL이 없어 건너뜁니다")
	}

	ctx := context.Background()
	store, err := postgres.Open(ctx, postgres.Options{URL: url, MaxConns: 16, Schema: "test_integration"})
	if err != nil {
		t.Fatalf("연결 실패: %v", err)
	}
	t.Cleanup(store.Close)

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("마이그레이션 실패: %v", err)
	}
	if _, err := store.Pool().Exec(ctx, `
TRUNCATE image_vectors, images, crawl_ranges, crawl_post_retries, crawl_states,
         node_models, node_metrics, net_probes, query_cache, nodes,
         embedding_models RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("초기화 실패: %v", err)
	}
	for _, m := range models {
		if err := store.UpsertModel(ctx, m); err != nil {
			t.Fatalf("모델 등록 실패: %v", err)
		}
	}
	return store
}

// 가짜 Danbooru -----------------------------------------------------------

func danbooruServer(t testing.TB, total int64) *httptest.Server {
	t.Helper()

	// 실제 Danbooru는 절대 URL을 주므로 시험 서버도 그렇게 맞춥니다.
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/posts.json", func(w http.ResponseWriter, r *http.Request) {
		tags := r.URL.Query().Get("tags")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 200
		}

		lower, upper := int64(1), total
		for _, field := range strings.Fields(tags) {
			switch {
			case strings.HasPrefix(field, "id:>="):
				lower = mustInt(field[5:])
			case strings.HasPrefix(field, "id:<"):
				upper = mustInt(field[4:]) - 1
			case strings.HasPrefix(field, "id:>"):
				lower = mustInt(field[4:]) + 1
			}
		}
		if upper > total {
			upper = total
		}

		var out []map[string]any
		if strings.Contains(tags, "order:id_asc") {
			for id := lower; id <= upper && len(out) < limit; id++ {
				out = append(out, post(base, id))
			}
		} else {
			for id := upper; id >= lower && len(out) < limit; id-- {
				out = append(out, post(base, id))
			}
		}
		json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake-image-bytes-" + filepath.Base(r.URL.Path)))
	})

	server := httptest.NewServer(mux)
	base = server.URL
	t.Cleanup(server.Close)
	return server
}

func mustInt(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func post(base string, id int64) map[string]any {
	return map[string]any{
		"id":                id,
		"md5":               fmt.Sprintf("%032x", id),
		"large_file_url":    fmt.Sprintf("%s/img/%d.jpg", base, id),
		"file_url":          fmt.Sprintf("%s/img/%d.jpg", base, id),
		"preview_file_url":  fmt.Sprintf("%s/img/p%d.jpg", base, id),
		"source":            "https://www.pixiv.net/artworks/1",
		"rating":            "g",
		"score":             int(id % 50),
		"image_width":       1000,
		"image_height":      1400,
		"file_size":         2048,
		"file_ext":          "jpg",
		"tag_string":        "1girl solo",
		"tag_string_artist": "artist_name",
	}
}

// 가짜 Qdrant -------------------------------------------------------------

type qdrantSpy struct {
	mu          sync.Mutex
	collections map[string]bool
	points      map[string]int
}

func qdrantServer(t testing.TB) (*qdrant.Client, *qdrantSpy) {
	t.Helper()
	spy := &qdrantSpy{collections: map[string]bool{}, points: map[string]int{}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		spy.mu.Lock()
		defer spy.mu.Unlock()
		w.Header().Set("content-type", "application/json")

		path := r.URL.Path
		name := strings.TrimPrefix(path, "/collections/")

		switch {
		case strings.HasSuffix(path, "/exists"):
			target := strings.TrimSuffix(name, "/exists")
			fmt.Fprintf(w, `{"result":{"exists":%t}}`, spy.collections[target])

		case strings.HasSuffix(path, "/points") && r.Method == http.MethodPut:
			var body struct {
				Points []json.RawMessage `json:"points"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			spy.points[strings.TrimSuffix(name, "/points")] += len(body.Points)
			fmt.Fprint(w, `{"status":"ok"}`)

		case r.Method == http.MethodPut:
			spy.collections[name] = true
			fmt.Fprint(w, `{"status":"ok"}`)

		default:
			fmt.Fprint(w, `{"result":{}}`)
		}
	}))
	t.Cleanup(server.Close)

	client, err := qdrant.New(qdrant.Options{BaseURL: server.URL, Quantize: true})
	if err != nil {
		t.Fatalf("Qdrant 클라이언트 생성 실패: %v", err)
	}
	return client, spy
}

func (s *qdrantSpy) count(collection string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.points[collection]
}

// 가짜 임베딩 워커 ---------------------------------------------------------

type stubEmbedder struct {
	calls int64
	mu    sync.Mutex
}

func (e *stubEmbedder) Describe(context.Context) (domain.EmbedderInfo, error) {
	return domain.EmbedderInfo{Device: domain.DeviceCPU, Models: models}, nil
}

func (e *stubEmbedder) Health(context.Context) error { return nil }

func (e *stubEmbedder) EmbedQuery(ctx context.Context, image []byte) (domain.EmbedResult, error) {
	return e.Embed(ctx, image)
}

func (e *stubEmbedder) Embed(_ context.Context, image []byte) (domain.EmbedResult, error) {
	e.mu.Lock()
	e.calls++
	e.mu.Unlock()

	// 이미지 바이트에서 결정적으로 벡터와 해시를 만듭니다.
	var seed byte
	for _, b := range image {
		seed ^= b
	}
	values := make([]float32, 8)
	for i := range values {
		values[i] = float32((int(seed)+i)%17) / 17
	}
	domain.Normalize(values)

	return domain.EmbedResult{
		Vectors: []domain.Vector{
			{ModelID: "copy-model", Values: values},
			{ModelID: "semantic-model", Values: append([]float32(nil), values...)},
		},
		Hashes: domain.Hashes{
			PHash:  fmt.Sprintf("%016x", uint64(seed)*0x9e3779b97f4a7c15),
			DHash:  fmt.Sprintf("%016x", uint64(seed)*0x517cc1b727220a95),
			Width:  1000,
			Height: 1400,
		},
		Thumb: []byte("thumb-" + string(image[:min(8, len(image))])),
	}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 조립 -------------------------------------------------------------------

type stack struct {
	store    *postgres.Store
	ingest   *app.Ingest
	indexer  *app.Indexer
	source   *danbooru.Client
	embedder *stubEmbedder
	spy      *qdrantSpy
	thumbs   *filestore.ThumbStore
	thumbDir string
}

func newStack(t testing.TB, totalPosts int64) *stack {
	t.Helper()
	ctx := context.Background()

	store := openStore(t)
	index, spy := qdrantServer(t)
	for _, m := range models {
		if err := index.EnsureCollection(ctx, m); err != nil {
			t.Fatalf("컬렉션 준비 실패: %v", err)
		}
	}

	thumbDir := t.TempDir()
	thumbs, err := filestore.NewThumbStore(thumbDir)
	if err != nil {
		t.Fatalf("축소본 저장소 생성 실패: %v", err)
	}

	ingest, err := app.NewIngest(app.IngestDeps{
		Images: store, Vector: store, Index: index,
		Thumbs: thumbs, Models: models, Log: quiet(),
	})
	if err != nil {
		t.Fatalf("적재기 생성 실패: %v", err)
	}

	api := danbooruServer(t, totalPosts)
	source, err := danbooru.New(api.Client(), danbooru.Options{BaseURL: api.URL})
	if err != nil {
		t.Fatalf("수집 클라이언트 생성 실패: %v", err)
	}

	embedder := &stubEmbedder{}
	limiter := app.NewLimiter(app.LimiterConfig{
		Start: 4, Min: 1, Max: 8, Interval: time.Hour,
	}, app.SystemClock)

	indexer, err := app.NewIndexer(app.IndexerConfig{
		NodeID: "it-node", SourceSite: "danbooru", ScopeKey: "default", SubmitBatch: 8,
	}, app.IndexerDeps{
		Source: source, Embedder: embedder, Sink: ingest, Images: store,
		Limiter: limiter, Models: models, Log: quiet(),
	})
	if err != nil {
		t.Fatalf("인덱서 생성 실패: %v", err)
	}

	return &stack{
		store: store, ingest: ingest, indexer: indexer, source: source,
		embedder: embedder, spy: spy, thumbs: thumbs, thumbDir: thumbDir,
	}
}

// 검증 -------------------------------------------------------------------

// 수집한 게시물이 메타데이터, 벡터 백업, 축소본, 색인까지 모두 남아야 합니다.
func TestFullPipelinePersistsEverything(t *testing.T) {
	ctx := context.Background()
	s := newStack(t, 500)

	posts, err := s.source.PostsInRange(ctx, 1, 20, "")
	if err != nil {
		t.Fatalf("수집 실패: %v", err)
	}

	report, err := s.indexer.IndexBatch(ctx, posts)
	if err != nil {
		t.Fatalf("처리 실패: %v", err)
	}
	if report.Saved != 20 {
		t.Fatalf("저장이 %d건입니다. 20건을 기대했습니다 (실패 %v)", report.Saved, report.Failed)
	}

	count, err := s.store.CountImages(ctx)
	if err != nil {
		t.Fatalf("개수 조회 실패: %v", err)
	}
	if count != 20 {
		t.Fatalf("PostgreSQL에 %d건입니다", count)
	}

	// 벡터가 두 모델 모두 백업되어야 Qdrant를 잃어도 복구됩니다.
	summary, err := s.store.CrawlSummary(ctx, "danbooru", "default")
	if err != nil {
		t.Fatalf("요약 조회 실패: %v", err)
	}
	for _, m := range models {
		if summary.VectorsByModel[m.ID] != 20 {
			t.Fatalf("모델 %s 벡터가 %d건입니다", m.ID, summary.VectorsByModel[m.ID])
		}
	}

	if got := s.spy.count("copy"); got != 20 {
		t.Fatalf("Qdrant copy 컬렉션에 %d점입니다", got)
	}
	if got := s.spy.count("semantic"); got != 20 {
		t.Fatalf("Qdrant semantic 컬렉션에 %d점입니다", got)
	}

	// 축소본이 실제 파일로 남아야 모델 교체 시 다시 안 받습니다.
	var thumbs int
	filepath.Walk(s.thumbDir, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			thumbs++
		}
		return nil
	})
	if thumbs != 20 {
		t.Fatalf("축소본이 %d개입니다. 20개를 기대했습니다", thumbs)
	}

	img, err := s.store.ImageBySource(ctx, "danbooru", 7)
	if err != nil {
		t.Fatalf("이미지 조회 실패: %v", err)
	}
	switch {
	case img.PHash == "":
		t.Fatal("해시가 저장되지 않았습니다")
	case img.ThumbPath == "":
		t.Fatal("축소본 경로가 기록되지 않았습니다")
	case img.IndexedBy != "it-node":
		t.Fatalf("처리 노드가 %q입니다", img.IndexedBy)
	case len(img.Tags) == 0:
		t.Fatal("태그가 저장되지 않았습니다")
	}
}

// 같은 묶음을 다시 처리하면 내려받지도 임베딩하지도 않아야 합니다.
func TestSecondPassSkipsEverything(t *testing.T) {
	ctx := context.Background()
	s := newStack(t, 500)

	posts, _ := s.source.PostsInRange(ctx, 1, 15, "")
	if _, err := s.indexer.IndexBatch(ctx, posts); err != nil {
		t.Fatalf("첫 처리 실패: %v", err)
	}
	first := s.embedder.calls

	report, err := s.indexer.IndexBatch(ctx, posts)
	if err != nil {
		t.Fatalf("두 번째 처리 실패: %v", err)
	}
	if report.Existing != 15 {
		t.Fatalf("기존 건수가 %d입니다", report.Existing)
	}
	if s.embedder.calls != first {
		t.Fatalf("임베딩이 %d번 더 불렸습니다", s.embedder.calls-first)
	}
}

// Qdrant를 통째로 잃어도 PostgreSQL만으로 색인을 되살릴 수 있어야 합니다.
func TestRebuildIndexFromPostgres(t *testing.T) {
	ctx := context.Background()
	s := newStack(t, 500)

	posts, _ := s.source.PostsInRange(ctx, 1, 12, "")
	if _, err := s.indexer.IndexBatch(ctx, posts); err != nil {
		t.Fatalf("처리 실패: %v", err)
	}

	// 색인 완료 표시를 지워 Qdrant가 비어 있는 상황을 만듭니다.
	if _, err := s.store.Pool().Exec(ctx, `UPDATE image_vectors SET indexed_at = NULL`); err != nil {
		t.Fatalf("초기화 실패: %v", err)
	}

	restored, err := s.ingest.RebuildIndex(ctx, 5)
	if err != nil {
		t.Fatalf("재구축 실패: %v", err)
	}
	if restored != 24 {
		t.Fatalf("되살린 벡터가 %d개입니다. 12건 곱하기 모델 2개인 24를 기대했습니다", restored)
	}

	pending, err := s.store.PendingVectors(ctx, "copy-model", 100)
	if err != nil {
		t.Fatalf("남은 벡터 조회 실패: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("아직 %d건이 색인되지 않았습니다", len(pending))
	}
}

// 작업 노드가 HTTP로 보낸 결과가 중앙 저장소까지 도달해야 합니다.
func TestWorkerToControlOverHTTP(t *testing.T) {
	ctx := context.Background()
	s := newStack(t, 500)

	server, err := httpapi.New(httpapi.Config{
		Bind: "127.0.0.1:0", Token: "test-token",
		SourceSite: "danbooru", ScopeKey: "default",
	}, httpapi.Deps{Ingest: s.ingest, Stats: s.store, Images: s.store, Log: quiet()})
	if err != nil {
		t.Fatalf("서버 생성 실패: %v", err)
	}

	central := httptest.NewServer(serverHandler(t, server))
	defer central.Close()

	client, err := httpapi.NewClient(httpapi.ClientOptions{
		BaseURL: central.URL, Token: "test-token", NodeID: "worker-a",
	})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}

	// 작업 노드처럼 인덱서 결과를 HTTP 싱크로 보냅니다.
	limiter := app.NewLimiter(app.LimiterConfig{Start: 2, Min: 1, Max: 4,
		Interval: time.Hour}, app.SystemClock)
	remote, err := app.NewIndexer(app.IndexerConfig{
		NodeID: "worker-a", SourceSite: "danbooru", ScopeKey: "default",
	}, app.IndexerDeps{
		Source: s.source, Embedder: s.embedder, Sink: client, Images: s.store,
		Limiter: limiter, Models: models, Log: quiet(),
	})
	if err != nil {
		t.Fatalf("원격 인덱서 생성 실패: %v", err)
	}

	posts, _ := s.source.PostsInRange(ctx, 100, 109, "")
	report, err := remote.IndexBatch(ctx, posts)
	if err != nil {
		t.Fatalf("전송 실패: %v", err)
	}
	if report.Saved != 10 {
		t.Fatalf("저장이 %d건입니다 (실패 %v)", report.Saved, report.Failed)
	}

	img, err := s.store.ImageBySource(ctx, "danbooru", 105)
	if err != nil {
		t.Fatalf("중앙에 도달하지 못했습니다: %v", err)
	}
	if img.IndexedBy != "worker-a" {
		t.Fatalf("처리 노드가 %q입니다", img.IndexedBy)
	}
	if s.spy.count("copy") != 10 {
		t.Fatalf("Qdrant에 %d점입니다", s.spy.count("copy"))
	}
}

// 다른 모델을 쓰는 노드가 보낸 벡터는 거부되어야 합니다.
func TestControlRejectsForeignModel(t *testing.T) {
	ctx := context.Background()
	s := newStack(t, 100)

	bad := []domain.IndexedImage{{
		Image: domain.Image{SourceSite: "danbooru", SourcePostID: 999},
		Vectors: []domain.Vector{
			{ModelID: "copy-model", Values: make([]float32, 512)},
			{ModelID: "semantic-model", Values: make([]float32, 8)},
		},
	}}

	err := s.ingest.Submit(ctx, bad)
	if err == nil {
		t.Fatal("차원이 다른 벡터는 거부해야 합니다")
	}
	if !strings.Contains(err.Error(), "차원") {
		t.Fatalf("오류 내용이 %q입니다", err)
	}

	if count, _ := s.store.CountImages(ctx); count != 0 {
		t.Fatalf("거부했는데 %d건이 저장되었습니다", count)
	}
}

// 여러 노드가 동시에 돌아도 같은 게시물을 두 번 저장하지 않아야 합니다.
func TestConcurrentNodesDoNotDuplicate(t *testing.T) {
	ctx := context.Background()
	s := newStack(t, 200)

	posts, _ := s.source.PostsInRange(ctx, 1, 30, "")

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.indexer.IndexBatch(ctx, posts)
		}()
	}
	wg.Wait()

	count, err := s.store.CountImages(ctx)
	if err != nil {
		t.Fatalf("개수 조회 실패: %v", err)
	}
	if count != 30 {
		t.Fatalf("이미지가 %d건입니다. 30건이어야 합니다", count)
	}
}

func serverHandler(t *testing.T, server *httpapi.Server) http.Handler {
	t.Helper()
	return server.Handler()
}
