package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"saucedust/internal/domain"
)

// 임대 저장소를 메모리로 흉내 냅니다. PostgreSQL 동작은 어댑터 시험이 따로 봅니다.
type fakeLease struct {
	mu sync.Mutex

	watermark   int64
	frontier    int64
	rangeSize   int64
	handedOut   []domain.CrawlRange
	finished    []domain.RangeStatus
	enqueued    []int64
	advanced    []int64
	exhausted   bool
	nextID      int64
	retryQueue  []domain.PostRetry
	retryDone   []domain.RetryStatus
	floorSeen   int64
	rescheduled int
}

func (f *fakeLease) AcquireBackfillRange(_ context.Context, req domain.LeaseRequest) (*domain.CrawlRange, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.exhausted || f.frontier <= 1 {
		return nil, domain.ErrNoWork
	}
	f.nextID++
	f.rangeSize = req.RangeSize
	f.floorSeen = req.FloorID

	upper := f.frontier - 1
	lower := upper - req.RangeSize + 1
	if lower < 1 {
		lower = 1
	}
	f.frontier = lower

	out := domain.CrawlRange{
		ID: f.nextID, SourceSite: req.SourceSite, ScopeKey: req.ScopeKey,
		Direction: domain.DirectionBackfill, LowerID: lower, UpperID: upper,
		Attempts: 1, NodeID: req.NodeID,
	}
	f.handedOut = append(f.handedOut, out)
	return &out, nil
}

func (f *fakeLease) FinishRange(_ context.Context, _ *domain.CrawlRange, status domain.RangeStatus, _ int, _ error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finished = append(f.finished, status)
	return nil
}

func (f *fakeLease) InitCatchupState(_ context.Context, _, _ string, latest int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.frontier == 0 {
		f.frontier = latest
	}
	return nil
}

func (f *fakeLease) HighWatermark(context.Context, string, string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.watermark, nil
}

func (f *fakeLease) AdvanceWatermark(_ context.Context, _, _ string, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watermark = id
	f.advanced = append(f.advanced, id)
	return nil
}

func (f *fakeLease) EnqueueRetries(_ context.Context, _, _ string, ids []int64, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueued = append(f.enqueued, ids...)
	return nil
}

func (f *fakeLease) LeaseRetries(context.Context, string, string, string, int) ([]domain.PostRetry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.retryQueue
	f.retryQueue = nil
	return out, nil
}

func (f *fakeLease) FinishRetry(_ context.Context, _ domain.PostRetry, status domain.RetryStatus, _ error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retryDone = append(f.retryDone, status)
	return nil
}

func (f *fakeLease) RescheduleRetry(context.Context, domain.PostRetry, time.Duration, error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rescheduled++
	return nil
}

func (f *fakeLease) snapshot() fakeLease {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeLease{
		watermark: f.watermark, frontier: f.frontier, rangeSize: f.rangeSize,
		floorSeen:   f.floorSeen,
		handedOut:   append([]domain.CrawlRange(nil), f.handedOut...),
		finished:    append([]domain.RangeStatus(nil), f.finished...),
		enqueued:    append([]int64(nil), f.enqueued...),
		advanced:    append([]int64(nil), f.advanced...),
		retryDone:   append([]domain.RetryStatus(nil), f.retryDone...),
		rescheduled: f.rescheduled,
	}
}

// 구간과 최신 게시물을 돌려주는 수집 대상 흉내입니다.
type rangeSource struct {
	fakeSource
	latest    int64
	newPosts  []domain.SourcePost
	rangeFail error
}

func (r *rangeSource) LatestPostID(context.Context) (int64, error) { return r.latest, nil }

func (r *rangeSource) PostsInRange(_ context.Context, lower, upper int64, _ string) ([]domain.SourcePost, error) {
	if r.rangeFail != nil {
		return nil, r.rangeFail
	}
	var out []domain.SourcePost
	for id := lower; id <= upper && len(out) < 50; id++ {
		out = append(out, makeSourcePost(id))
	}
	return out, nil
}

func (r *rangeSource) PostsAfter(context.Context, int64, string, int) ([]domain.SourcePost, error) {
	out := r.newPosts
	r.newPosts = nil
	return out, nil
}

func newCrawler(t *testing.T, cfg CrawlerConfig, source SourceClient, lease LeaseRepository) (*Crawler, *indexerHarness) {
	t.Helper()

	h := newHarness(t, 4)
	indexer, err := NewIndexer(IndexerConfig{
		NodeID: cfg.NodeID, SourceSite: cfg.SourceSite, ScopeKey: cfg.ScopeKey,
	}, IndexerDeps{
		Source: source, Embedder: h.embedder, Sink: h.sink, Images: h.images,
		Limiter: h.limiter, Models: testModels, Log: quietLogger(),
	})
	if err != nil {
		t.Fatalf("인덱서 생성 실패: %v", err)
	}

	crawler, err := NewCrawler(cfg, source, lease, indexer, nil, quietLogger())
	if err != nil {
		t.Fatalf("크롤러 생성 실패: %v", err)
	}
	return crawler, h
}

func baseConfig() CrawlerConfig {
	return CrawlerConfig{
		NodeID: "node-1", SourceSite: "danbooru", ScopeKey: "default",
		PollEvery: 15 * time.Millisecond, BackfillWorkers: 1,
		BaseRangeSize: 20, RetryBatch: 8,
	}
}

// 백필 작업자가 구간을 받아 처리하고 완료로 보고해야 합니다.
func TestCrawlerProcessesBackfillRanges(t *testing.T) {
	lease := &fakeLease{frontier: 61}
	source := &rangeSource{latest: 60}
	source.fakeSource.failURL = map[string]error{}

	crawler, h := newCrawler(t, baseConfig(), source, lease)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	crawler.Run(ctx)

	got := lease.snapshot()
	if len(got.handedOut) < 3 {
		t.Fatalf("구간을 %d개만 처리했습니다. 60개를 20씩 나눠 3개는 돼야 합니다", len(got.handedOut))
	}
	for _, status := range got.finished {
		if status != domain.RangeCompleted {
			t.Fatalf("구간 상태가 %q입니다", status)
		}
	}
	if h.sink.count() < 50 {
		t.Fatalf("저장된 이미지가 %d건입니다", h.sink.count())
	}
}

// 구간 안에서 일부가 실패하면 그 게시물만 재시도 큐로 가야 합니다.
func TestCrawlerQueuesFailedPosts(t *testing.T) {
	lease := &fakeLease{frontier: 21}
	source := &rangeSource{latest: 20}
	source.fakeSource.failURL = map[string]error{
		"https://cdn.example/5.jpg":  errors.New("연결 실패"),
		"https://cdn.example/11.jpg": errors.New("연결 실패"),
	}

	crawler, _ := newCrawler(t, baseConfig(), source, lease)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	crawler.Run(ctx)

	got := lease.snapshot()
	if len(got.enqueued) != 2 {
		t.Fatalf("재시도 큐에 %d건입니다. 2건을 기대했습니다: %v", len(got.enqueued), got.enqueued)
	}
	failed := map[int64]bool{}
	for _, id := range got.enqueued {
		failed[id] = true
	}
	if !failed[5] || !failed[11] {
		t.Fatalf("재시도 목록이 %v입니다", got.enqueued)
	}
}

// 목록 조회 자체가 실패하면 구간을 실패로 남겨 다른 노드가 다시 가져가야 합니다.
func TestCrawlerMarksRangeFailedOnSourceError(t *testing.T) {
	lease := &fakeLease{frontier: 41}
	source := &rangeSource{latest: 40, rangeFail: errors.New("서버가 응답하지 않습니다")}

	crawler, _ := newCrawler(t, baseConfig(), source, lease)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	crawler.Run(ctx)

	got := lease.snapshot()
	if len(got.finished) == 0 {
		t.Fatal("구간 결과를 보고하지 않았습니다")
	}
	if got.finished[0] != domain.RangeFailed {
		t.Fatalf("구간 상태가 %q입니다. failed여야 합니다", got.finished[0])
	}
}

// 최신 따라잡기는 처리한 가장 큰 번호까지 진행점을 올려야 합니다.
func TestCrawlerAdvancesWatermark(t *testing.T) {
	lease := &fakeLease{frontier: 1, watermark: 100, exhausted: true}
	source := &rangeSource{
		latest: 105,
		newPosts: []domain.SourcePost{
			makeSourcePost(101), makeSourcePost(102), makeSourcePost(103),
		},
	}
	source.fakeSource.failURL = map[string]error{}

	cfg := baseConfig()
	cfg.BackfillWorkers = 0

	crawler, _ := newCrawler(t, cfg, source, lease)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	crawler.Run(ctx)

	got := lease.snapshot()
	if got.watermark != 103 {
		t.Fatalf("진행점이 %d입니다. 103을 기대했습니다", got.watermark)
	}
}

// 실패한 게시물이 있어도 진행점은 올라가야 최신 수집이 막히지 않습니다.
func TestCatchupAdvancesDespiteFailures(t *testing.T) {
	lease := &fakeLease{frontier: 1, watermark: 200, exhausted: true}
	source := &rangeSource{
		latest:   205,
		newPosts: []domain.SourcePost{makeSourcePost(201), makeSourcePost(202)},
	}
	source.fakeSource.failURL = map[string]error{
		"https://cdn.example/201.jpg": errors.New("연결 실패"),
	}

	cfg := baseConfig()
	cfg.BackfillWorkers = 0

	crawler, _ := newCrawler(t, cfg, source, lease)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	crawler.Run(ctx)

	got := lease.snapshot()
	if got.watermark != 202 {
		t.Fatalf("진행점이 %d입니다. 실패가 있어도 202까지 올라야 합니다", got.watermark)
	}
	if len(got.enqueued) != 1 || got.enqueued[0] != 201 {
		t.Fatalf("재시도 목록이 %v입니다", got.enqueued)
	}
}

// 자동 조절을 끄면 구간 크기가 설정값 그대로여야 합니다.
func TestRangeSizeFixedWhenNotAdaptive(t *testing.T) {
	lease := &fakeLease{frontier: 201}
	source := &rangeSource{latest: 200}
	source.fakeSource.failURL = map[string]error{}

	cfg := baseConfig()
	cfg.Adaptive = false
	cfg.BaseRangeSize = 25

	crawler, _ := newCrawler(t, cfg, source, lease)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	crawler.Run(ctx)

	if got := lease.snapshot().rangeSize; got != 25 {
		t.Fatalf("구간 크기가 %d입니다. 25로 고정되어야 합니다", got)
	}
}

// 배정할 구간이 없으면 조용히 쉬어야 합니다. 오류가 아닙니다.
func TestCrawlerIdlesWhenNoWork(t *testing.T) {
	lease := &fakeLease{exhausted: true}
	source := &rangeSource{latest: 10}
	source.fakeSource.failURL = map[string]error{}

	crawler, _ := newCrawler(t, baseConfig(), source, lease)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	if err := crawler.Run(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("일이 없을 때 오류가 났습니다: %v", err)
	}
	if len(lease.snapshot().handedOut) != 0 {
		t.Fatal("배정할 구간이 없는데 처리했습니다")
	}
}

func TestRetryDelayGrows(t *testing.T) {
	cases := map[int]time.Duration{
		0: 2 * time.Minute,
		1: 2 * time.Minute,
		3: 8 * time.Minute,
		6: 64 * time.Minute,
		9: 64 * time.Minute,
	}
	for attempts, want := range cases {
		if got := retryDelay(attempts); got != want {
			t.Errorf("%d회차 대기가 %v입니다. %v를 기대했습니다", attempts, got, want)
		}
	}
}

// 정한 만큼 모으면 스스로 멈춰야 합니다.
// 새 노드가 도는지 확인하려고 끝없이 도는 것을 띄웠다가 손으로 죽이면,
// 어디까지 갔는지도 얼마나 걸렸는지도 남지 않습니다.
func TestCrawlerStopsAtLimit(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxImages = 20
	crawler, _ := newCrawler(t, cfg, &rangeSource{latest: 60}, &fakeLease{frontier: 61})

	// Run이 감싸는 취소 함수를 흉내 냅니다.
	var stopped bool
	crawler.stop = func() { stopped = true }

	crawler.recordThroughput(8, time.Second)
	if stopped {
		t.Fatal("8건에서 멈췄습니다")
	}
	if got := crawler.Saved(); got != 8 {
		t.Errorf("%d건이라고 합니다", got)
	}

	crawler.recordThroughput(12, time.Second)
	if !stopped {
		t.Error("20건을 채웠는데 멈추지 않았습니다")
	}
	if got := crawler.Saved(); got != 20 {
		t.Errorf("%d건이라고 합니다. 20이어야 합니다", got)
	}
}

// 상한이 없으면 계속 돌아야 합니다. 기본값이 0입니다.
func TestCrawlerWithoutLimitKeepsGoing(t *testing.T) {
	crawler, _ := newCrawler(t, baseConfig(), &rangeSource{latest: 60}, &fakeLease{frontier: 61})

	var stopped bool
	crawler.stop = func() { stopped = true }

	for i := 0; i < 50; i++ {
		crawler.recordThroughput(100, time.Second)
	}
	if stopped {
		t.Error("상한이 없는데 멈췄습니다")
	}
	if got := crawler.Saved(); got != 5000 {
		t.Errorf("%d건이라고 합니다", got)
	}
}

// 상한이 있으면 남은 만큼만 처리해야 합니다.
// 묶음이 끝난 뒤에만 세면 구간 크기만큼 넘칩니다.
func TestCrawlerTrimsBatchToRemaining(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxImages = 20
	crawler, _ := newCrawler(t, cfg, &rangeSource{latest: 60}, &fakeLease{frontier: 61})

	if got := crawler.remaining(); got != 20 {
		t.Fatalf("남은 수가 %d입니다", got)
	}

	crawler.saved.Store(15)
	if got := crawler.remaining(); got != 5 {
		t.Errorf("15건 뒤 남은 수가 %d입니다. 5여야 합니다", got)
	}

	crawler.saved.Store(25)
	if got := crawler.remaining(); got != 0 {
		t.Errorf("상한을 넘긴 뒤 남은 수가 %d입니다. 0이어야 합니다", got)
	}
}

func TestCrawlerWithoutLimitHasNoRemaining(t *testing.T) {
	crawler, _ := newCrawler(t, baseConfig(), &rangeSource{latest: 60}, &fakeLease{frontier: 61})
	if got := crawler.remaining(); got != -1 {
		t.Errorf("상한이 없는데 남은 수가 %d입니다", got)
	}
}

// 상한을 정하면 그 근처에서 멈춰야 합니다. 크게 넘기면 확인용으로 쓸 수 없습니다.
func TestCrawlerRunRespectsLimit(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxImages = 20
	cfg.BackfillWorkers = 1

	source := &rangeSource{latest: 600}
	source.fakeSource.failURL = map[string]error{}
	crawler, _ := newCrawler(t, cfg, source, &fakeLease{frontier: 601})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	crawler.Run(ctx)

	got := crawler.Saved()
	if got < 20 {
		t.Errorf("%d건만 모았습니다. 20건은 채워야 합니다", got)
	}
	// 여러 작업자가 동시에 돌므로 조금은 넘을 수 있습니다.
	// 구간 크기만큼 넘치면 자르는 것이 동작하지 않는 것입니다.
	if got > 40 {
		t.Errorf("%d건이나 모았습니다. 상한 20에서 크게 넘쳤습니다", got)
	}
	t.Logf("상한 20에 실제 %d건", got)
}
