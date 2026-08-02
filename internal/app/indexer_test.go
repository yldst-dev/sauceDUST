package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"saucedust/internal/domain"
)

var testModels = []domain.EmbeddingModel{
	{ID: "copy-model", Kind: domain.ModelCopy, VectorSize: 4, Collection: "copy"},
	{ID: "semantic-model", Kind: domain.ModelSemantic, VectorSize: 4, Collection: "semantic"},
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeSource struct {
	downloads atomic.Int64
	failURL   map[string]error
	peak      atomic.Int64
	running   atomic.Int64
}

func (f *fakeSource) Site() string { return "danbooru" }

func (f *fakeSource) LatestPostID(context.Context) (int64, error) { return 1000, nil }

func (f *fakeSource) PostsInRange(context.Context, int64, int64, string) ([]domain.SourcePost, error) {
	return nil, nil
}

func (f *fakeSource) PostsAfter(context.Context, int64, string, int) ([]domain.SourcePost, error) {
	return nil, nil
}

func (f *fakeSource) PostByID(_ context.Context, id int64) (*domain.SourcePost, error) {
	post := makeSourcePost(id)
	return &post, nil
}

func (f *fakeSource) Download(ctx context.Context, url string, _ int64) ([]byte, error) {
	now := f.running.Add(1)
	for {
		peak := f.peak.Load()
		if now <= peak || f.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	defer f.running.Add(-1)

	time.Sleep(2 * time.Millisecond)
	f.downloads.Add(1)

	if err, bad := f.failURL[url]; bad {
		return nil, err
	}
	return []byte("image-bytes"), nil
}

type fakeEmbedder struct {
	calls atomic.Int64
	// shortVector가 켜지면 차원이 틀린 벡터를 돌려줍니다.
	shortVector bool
	missModel   bool
}

func (f *fakeEmbedder) Describe(context.Context) (domain.EmbedderInfo, error) {
	return domain.EmbedderInfo{Device: domain.DeviceCPU, Models: testModels}, nil
}

func (f *fakeEmbedder) Health(context.Context) error { return nil }

func (f *fakeEmbedder) EmbedQuery(ctx context.Context, image []byte) (domain.EmbedResult, error) {
	return f.Embed(ctx, image)
}

func (f *fakeEmbedder) Embed(context.Context, []byte) (domain.EmbedResult, error) {
	f.calls.Add(1)

	size := 4
	if f.shortVector {
		size = 2
	}
	values := make([]float32, size)
	for i := range values {
		values[i] = 0.5
	}

	result := domain.EmbedResult{
		Hashes: domain.Hashes{PHash: "abc", DHash: "def", Width: 800, Height: 1200},
		Thumb:  []byte("thumb"),
	}
	result.Vectors = append(result.Vectors, domain.Vector{ModelID: "copy-model", Values: values})
	if !f.missModel {
		other := make([]float32, size)
		copy(other, values)
		result.Vectors = append(result.Vectors, domain.Vector{ModelID: "semantic-model", Values: other})
	}
	return result, nil
}

type fakeSink struct {
	mu       sync.Mutex
	received []domain.IndexedImage
	batches  int
	failNext error
}

func (f *fakeSink) Submit(_ context.Context, batch []domain.IndexedImage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	f.received = append(f.received, batch...)
	f.batches++
	return nil
}

func (f *fakeSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.received)
}

type fakeImages struct {
	existing map[int64]int64
}

func (f *fakeImages) UpsertImage(context.Context, *domain.Image) (int64, error) { return 0, nil }

func (f *fakeImages) UpsertImages(_ context.Context, imgs []*domain.Image) ([]int64, error) {
	ids := make([]int64, len(imgs))
	for i := range imgs {
		ids[i] = int64(i + 1)
	}
	return ids, nil
}

func (f *fakeImages) ExistingPostIDs(_ context.Context, _ string, ids []int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	for _, id := range ids {
		if imageID, ok := f.existing[id]; ok {
			out[id] = imageID
		}
	}
	return out, nil
}

func (f *fakeImages) ImageByID(context.Context, int64) (*domain.Image, error) {
	return nil, domain.ErrNotFound
}

func (f *fakeImages) ImageBySource(context.Context, string, int64) (*domain.Image, error) {
	return nil, domain.ErrNotFound
}

func (f *fakeImages) ImagesByIDs(context.Context, []int64) (map[int64]domain.Image, error) {
	return nil, nil
}

func (f *fakeImages) CountImages(context.Context) (int64, error) { return 0, nil }

func (f *fakeImages) ThumbsMissingVector(context.Context, string, int64, int) ([]domain.ThumbRef, int64, error) {
	return nil, 0, nil
}

func (f *fakeImages) CountThumbsMissingVector(context.Context, string) (int64, error) {
	return 0, nil
}

func makeSourcePost(id int64) domain.SourcePost {
	return domain.SourcePost{
		Site:         "danbooru",
		PostID:       id,
		LargeURL:     fmt.Sprintf("https://cdn.example/%d.jpg", id),
		CanonicalURL: fmt.Sprintf("https://danbooru.example/posts/%d", id),
		Rating:       "g",
		Tags:         []string{"1girl"},
		Width:        100,
		Height:       200,
	}
}

func makePosts(n int) []domain.SourcePost {
	out := make([]domain.SourcePost, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, makeSourcePost(int64(i)))
	}
	return out
}

type indexerHarness struct {
	indexer  *Indexer
	source   *fakeSource
	embedder *fakeEmbedder
	sink     *fakeSink
	images   *fakeImages
	limiter  *Limiter
}

func newHarness(t *testing.T, concurrency int) *indexerHarness {
	t.Helper()

	h := &indexerHarness{
		source:   &fakeSource{failURL: map[string]error{}},
		embedder: &fakeEmbedder{},
		sink:     &fakeSink{},
		images:   &fakeImages{existing: map[int64]int64{}},
	}
	h.limiter = NewLimiter(LimiterConfig{
		Start: concurrency, Min: 1, Max: concurrency,
		Interval: time.Hour, Enabled: false,
	}, newFakeClock())

	indexer, err := NewIndexer(IndexerConfig{
		NodeID: "test-node", SourceSite: "danbooru", ScopeKey: "default",
		SubmitBatch: 8,
	}, IndexerDeps{
		Source: h.source, Embedder: h.embedder, Sink: h.sink,
		Images: h.images, Limiter: h.limiter, Models: testModels,
		Log: quietLogger(),
	})
	if err != nil {
		t.Fatalf("인덱서 생성 실패: %v", err)
	}
	h.indexer = indexer
	return h
}

func TestIndexBatchProcessesEveryPost(t *testing.T) {
	h := newHarness(t, 4)

	report, err := h.indexer.IndexBatch(context.Background(), makePosts(20))
	if err != nil {
		t.Fatalf("처리 실패: %v", err)
	}
	if report.Saved != 20 {
		t.Fatalf("저장이 %d건입니다. 20건을 기대했습니다", report.Saved)
	}
	if h.sink.count() != 20 {
		t.Fatalf("보낸 건수가 %d입니다", h.sink.count())
	}
	if got := h.embedder.calls.Load(); got != 20 {
		t.Fatalf("임베딩 호출이 %d번입니다", got)
	}
}

// 이미 저장된 게시물은 내려받지도, 임베딩하지도 않아야 합니다.
func TestIndexBatchSkipsExisting(t *testing.T) {
	h := newHarness(t, 4)
	for id := int64(1); id <= 15; id++ {
		h.images.existing[id] = id * 10
	}

	report, err := h.indexer.IndexBatch(context.Background(), makePosts(20))
	if err != nil {
		t.Fatalf("처리 실패: %v", err)
	}
	if report.Existing != 15 {
		t.Fatalf("기존 건수가 %d입니다. 15를 기대했습니다", report.Existing)
	}
	if report.Saved != 5 {
		t.Fatalf("저장이 %d건입니다. 5건을 기대했습니다", report.Saved)
	}
	if got := h.source.downloads.Load(); got != 5 {
		t.Fatalf("내려받기가 %d번입니다. 5번을 기대했습니다", got)
	}
}

func TestIndexBatchSkipsUnusable(t *testing.T) {
	h := newHarness(t, 4)

	posts := makePosts(5)
	posts[0].IsDeleted = true
	posts[1].IsBanned = true
	posts[2].LargeURL = "https://cdn.example/x.gif"
	posts[2].FileURL = ""
	posts[2].PreviewURL = ""

	report, err := h.indexer.IndexBatch(context.Background(), posts)
	if err != nil {
		t.Fatalf("처리 실패: %v", err)
	}
	if report.Skipped != 3 {
		t.Fatalf("건너뛴 건수가 %d입니다. 3을 기대했습니다", report.Skipped)
	}
	if report.Saved != 2 {
		t.Fatalf("저장이 %d건입니다. 2건을 기대했습니다", report.Saved)
	}
}

// 한 장이 실패해도 나머지는 그대로 저장되고, 실패한 것만 재시도 목록에 올라야 합니다.
func TestIndexBatchIsolatesFailures(t *testing.T) {
	h := newHarness(t, 4)
	h.source.failURL["https://cdn.example/3.jpg"] = errors.New("연결이 끊겼습니다")
	h.source.failURL["https://cdn.example/7.jpg"] = errors.New("연결이 끊겼습니다")

	report, err := h.indexer.IndexBatch(context.Background(), makePosts(10))
	if err != nil {
		t.Fatalf("묶음 전체가 실패하면 안 됩니다: %v", err)
	}
	if report.Saved != 8 {
		t.Fatalf("저장이 %d건입니다. 8건을 기대했습니다", report.Saved)
	}
	if len(report.Failed) != 2 {
		t.Fatalf("실패가 %d건입니다. 2건을 기대했습니다", len(report.Failed))
	}

	failed := map[int64]bool{}
	for _, id := range report.Failed {
		failed[id] = true
	}
	if !failed[3] || !failed[7] {
		t.Fatalf("실패 목록이 %v입니다. 3과 7을 기대했습니다", report.Failed)
	}
}

// 모델 차원이 어긋난 벡터는 저장하면 안 됩니다. 검색이 조용히 망가집니다.
func TestIndexBatchRejectsWrongVectorSize(t *testing.T) {
	h := newHarness(t, 2)
	h.embedder.shortVector = true

	report, err := h.indexer.IndexBatch(context.Background(), makePosts(3))
	if err != nil {
		t.Fatalf("처리 실패: %v", err)
	}
	if report.Saved != 0 {
		t.Fatalf("차원이 틀린 벡터가 %d건 저장되었습니다", report.Saved)
	}
	if len(report.Failed) != 3 {
		t.Fatalf("실패가 %d건입니다. 3건을 기대했습니다", len(report.Failed))
	}
}

func TestIndexBatchRejectsMissingModel(t *testing.T) {
	h := newHarness(t, 2)
	h.embedder.missModel = true

	report, _ := h.indexer.IndexBatch(context.Background(), makePosts(2))
	if report.Saved != 0 {
		t.Fatalf("모델이 빠졌는데 %d건 저장되었습니다", report.Saved)
	}
}

func TestIndexBatchRespectsConcurrencyLimit(t *testing.T) {
	h := newHarness(t, 3)

	if _, err := h.indexer.IndexBatch(context.Background(), makePosts(30)); err != nil {
		t.Fatalf("처리 실패: %v", err)
	}
	if peak := h.source.peak.Load(); peak > 3 {
		t.Fatalf("동시 내려받기가 %d까지 갔습니다. 3을 넘으면 안 됩니다", peak)
	}
}

func TestIndexBatchSubmitsInBatches(t *testing.T) {
	h := newHarness(t, 4)

	if _, err := h.indexer.IndexBatch(context.Background(), makePosts(20)); err != nil {
		t.Fatalf("처리 실패: %v", err)
	}
	if h.sink.batches < 2 {
		t.Fatalf("전송이 %d묶음입니다. 8개씩 나눠 보내야 합니다", h.sink.batches)
	}
}

// 전송이 실패하면 그 묶음의 게시물이 전부 재시도 목록에 올라야 합니다.
func TestIndexBatchReportsSinkFailure(t *testing.T) {
	h := newHarness(t, 2)
	h.sink.failNext = errors.New("중앙 서버가 응답하지 않습니다")

	report, err := h.indexer.IndexBatch(context.Background(), makePosts(8))
	if err == nil {
		t.Fatal("전송 실패는 오류로 올라와야 합니다")
	}
	if len(report.Failed) != 8 {
		t.Fatalf("실패가 %d건입니다. 8건을 기대했습니다", len(report.Failed))
	}
}

func TestIndexBatchEmptyInput(t *testing.T) {
	h := newHarness(t, 2)

	report, err := h.indexer.IndexBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("빈 입력은 그냥 넘어가야 합니다: %v", err)
	}
	if report.Total != 0 || report.Saved != 0 {
		t.Fatalf("보고서가 %+v입니다", report)
	}
}

func TestIndexBatchStopsOnCancel(t *testing.T) {
	h := newHarness(t, 2)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := h.indexer.IndexBatch(ctx, makePosts(50)); !errors.Is(err, context.Canceled) {
		t.Fatalf("취소되면 context.Canceled가 나와야 하는데 %v입니다", err)
	}
}

func TestIndexedImageCarriesMetadata(t *testing.T) {
	h := newHarness(t, 1)

	if _, err := h.indexer.IndexBatch(context.Background(), makePosts(1)); err != nil {
		t.Fatalf("처리 실패: %v", err)
	}

	got := h.sink.received[0]
	switch {
	case got.Image.SourcePostID != 1:
		t.Fatalf("게시물 번호가 %d입니다", got.Image.SourcePostID)
	case got.Image.PHash != "abc" || got.Image.DHash != "def":
		t.Fatalf("해시가 %q/%q입니다", got.Image.PHash, got.Image.DHash)
	case got.Image.Width != 800 || got.Image.Height != 1200:
		t.Fatalf("크기가 %dx%d입니다. 워커가 준 값을 써야 합니다", got.Image.Width, got.Image.Height)
	case got.Image.IndexedBy != "test-node":
		t.Fatalf("처리 노드가 %q입니다", got.Image.IndexedBy)
	case len(got.Vectors) != 2:
		t.Fatalf("벡터가 %d개입니다", len(got.Vectors))
	case string(got.Thumb) != "thumb":
		t.Fatalf("축소본이 %q입니다", got.Thumb)
	}
}

func TestClassifyOutcome(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Outcome
	}{
		{"성공", nil, OutcomeOK},
		{"기한 초과", context.DeadlineExceeded, OutcomeTimeout},
		{"감싼 기한 초과", fmt.Errorf("내려받기: %w", context.DeadlineExceeded), OutcomeTimeout},
		{"속도 제한", &throttleErr{}, OutcomeThrottled},
		{"감싼 속도 제한", fmt.Errorf("목록: %w", &throttleErr{}), OutcomeThrottled},
		{"재시도 가능", &retryErr{}, OutcomeThrottled},
		{"보통 오류", errors.New("무슨 일이 났습니다"), OutcomeError},
	}
	for _, tc := range cases {
		if got := classify(tc.err); got != tc.want {
			t.Errorf("%s: %v를 얻었습니다. %v를 기대했습니다", tc.name, got, tc.want)
		}
	}
}

type throttleErr struct{}

func (*throttleErr) Error() string   { return "속도를 줄이십시오" }
func (*throttleErr) Throttled() bool { return true }

type retryErr struct{}

func (*retryErr) Error() string   { return "잠시 뒤 다시" }
func (*retryErr) Retryable() bool { return true }

// 종료 중이라도 이미 내려받고 임베딩까지 끝낸 결과는 저장해야 합니다.
// 버리면 가장 비싼 단계를 다시 치러야 합니다.
func TestIndexBatchFlushesOnCancel(t *testing.T) {
	h := newHarness(t, 4)

	ctx, cancel := context.WithCancel(context.Background())
	posts := makePosts(6)

	// 처리가 시작된 뒤에 취소합니다.
	go func() {
		time.Sleep(15 * time.Millisecond)
		cancel()
	}()

	report, _ := h.indexer.IndexBatch(ctx, posts)

	if h.sink.count() == 0 {
		t.Fatal("취소되었다고 끝낸 작업을 버리면 안 됩니다")
	}
	if report.Saved == 0 {
		t.Fatalf("보고서에 저장이 %d건입니다", report.Saved)
	}
}

// 컨텍스트가 살아 있으면 그대로 씁니다.
func TestGracePeriodPassesLiveContext(t *testing.T) {
	ctx := context.Background()
	if got := gracePeriod(ctx, time.Second); got != ctx {
		t.Fatal("살아 있는 컨텍스트를 바꾸면 안 됩니다")
	}
}

// 끝난 컨텍스트에는 저장을 마칠 시간을 새로 줍니다.
func TestGracePeriodRevivesCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	grace := gracePeriod(ctx, time.Second)
	if grace.Err() != nil {
		t.Fatalf("새 컨텍스트가 이미 끝나 있습니다: %v", grace.Err())
	}
	if _, ok := grace.Deadline(); !ok {
		t.Fatal("기한이 없으면 영원히 매달릴 수 있습니다")
	}
}
