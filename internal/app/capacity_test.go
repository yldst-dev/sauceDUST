package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"saucedust/internal/domain"
)

// 실측에서 뽑은 값과 맞아야 합니다.
// 768차원이 장당 1,044바이트, 512차원이 712바이트였습니다.
func TestBytesPerImageMatchesMeasurement(t *testing.T) {
	tests := []struct {
		dims []int
		want int64
		tol  int64
	}{
		{[]int{768}, 1044, 50},
		{[]int{512}, 712, 50},
		{[]int{768, 512}, 1756, 100},
	}

	for _, tc := range tests {
		got := IndexBytesPerImage(tc.dims)
		if diff := got - tc.want; diff > tc.tol || diff < -tc.tol {
			t.Errorf("%v차원이 장당 %d바이트입니다. %d 근처를 기대했습니다",
				tc.dims, got, tc.want)
		}
	}
}

func TestBytesPerImageIgnoresBadSizes(t *testing.T) {
	if got := IndexBytesPerImage([]int{0, -5}); got != 0 {
		t.Errorf("쓸 수 없는 차원에서 %d가 나왔습니다", got)
	}
	if got := IndexBytesPerImage(nil); got != 0 {
		t.Errorf("모델이 없는데 %d가 나왔습니다", got)
	}
}

// 8 GB 중 Qdrant에 4 GB를 줬을 때 문서에 적은 장수가 나와야 합니다.
func TestCapacityMatchesTheDocumentedNumbers(t *testing.T) {
	const gb = 1 << 30

	one := ImageCapacity(4*gb+qdrantBaseBytes, []int{768})
	if one < 3_000_000 || one > 5_000_000 {
		t.Errorf("모델 하나에 %d장이 나왔습니다. 400만 장 근처를 기대했습니다", one)
	}

	two := ImageCapacity(4*gb+qdrantBaseBytes, []int{768, 512})
	if two < 1_800_000 || two > 3_000_000 {
		t.Errorf("모델 둘에 %d장이 나왔습니다. 240만 장 근처를 기대했습니다", two)
	}
	if two >= one {
		t.Error("모델을 늘렸는데 담을 장수가 줄지 않습니다")
	}
}

// 기본 몫보다 적게 주면 한 장도 담을 수 없습니다.
// 0을 내야 하고, 음수가 나오면 상한 검사가 뒤집힙니다.
func TestCapacityNeverGoesNegative(t *testing.T) {
	for _, bytes := range []int64{0, 1 << 20, qdrantBaseBytes, qdrantBaseBytes - 1} {
		if got := ImageCapacity(bytes, []int{768}); got != 0 {
			t.Errorf("%d바이트에 %d장이 나왔습니다", bytes, got)
		}
	}
}

type countingRepo struct {
	count atomic.Int64
	calls atomic.Int64
	err   error
}

func (c *countingRepo) CountImages(context.Context) (int64, error) {
	c.calls.Add(1)
	if c.err != nil {
		return 0, c.err
	}
	return c.count.Load(), nil
}

// 상한에 닿으면 수집을 멈춰야 합니다. 죽는 것이 아니라 멈추는 것입니다.
func TestCrawlerStopsWhenIndexIsFull(t *testing.T) {
	repo := &countingRepo{}
	repo.count.Store(500)

	cfg := baseConfig()
	cfg.MaxIndexed = 500

	lease := &fakeLease{frontier: 5000}
	source := &rangeSource{latest: 4999}
	source.fakeSource.failURL = map[string]error{}

	crawler := newCrawlerWithCounter(t, cfg, source, lease, repo)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	crawler.Run(ctx)

	if got := lease.snapshot(); len(got.handedOut) != 0 {
		t.Errorf("찬 상태인데 구간을 %d개 받았습니다", len(got.handedOut))
	}
	if crawler.Saved() != 0 {
		t.Errorf("찬 상태인데 %d장을 저장했습니다", crawler.Saved())
	}
}

// 상한 아래면 평소대로 돌아야 합니다.
func TestCrawlerRunsBelowTheLimit(t *testing.T) {
	repo := &countingRepo{}
	repo.count.Store(10)

	cfg := baseConfig()
	cfg.MaxIndexed = 1_000_000

	lease := &fakeLease{frontier: 200}
	source := &rangeSource{latest: 199}
	source.fakeSource.failURL = map[string]error{}

	crawler := newCrawlerWithCounter(t, cfg, source, lease, repo)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	crawler.Run(ctx)

	if got := lease.snapshot(); len(got.handedOut) == 0 {
		t.Error("여유가 있는데 구간을 하나도 받지 않았습니다")
	}
}

// 1,000만 행을 세는 것은 공짜가 아닙니다. 구간마다 세면 안 됩니다.
func TestCountIsCached(t *testing.T) {
	repo := &countingRepo{}
	repo.count.Store(10)

	cfg := baseConfig()
	cfg.MaxIndexed = 1_000_000
	cfg.BackfillWorkers = 2

	lease := &fakeLease{frontier: 3000}
	source := &rangeSource{latest: 2999}
	source.fakeSource.failURL = map[string]error{}

	crawler := newCrawlerWithCounter(t, cfg, source, lease, repo)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	crawler.Run(ctx)

	// 30초 동안 기억해 두므로 반 초 안에는 몇 번 되지 않아야 합니다.
	if calls := repo.calls.Load(); calls > 5 {
		t.Errorf("반 초 동안 %d번 셌습니다. 기억해 두고 쓰지 않는 것 같습니다", calls)
	}
}

// 세지 못한다고 수집을 세우면 안 됩니다. 저장소가 잠깐 흔들린 것일 수 있습니다.
func TestCountFailureDoesNotStopCrawling(t *testing.T) {
	repo := &countingRepo{err: errors.New("연결이 끊겼습니다")}

	cfg := baseConfig()
	cfg.MaxIndexed = 100

	lease := &fakeLease{frontier: 200}
	source := &rangeSource{latest: 199}
	source.fakeSource.failURL = map[string]error{}

	crawler := newCrawlerWithCounter(t, cfg, source, lease, repo)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	crawler.Run(ctx)

	if got := lease.snapshot(); len(got.handedOut) == 0 {
		t.Error("세지 못했다고 수집을 세웠습니다")
	}
}

// 상한을 뒀는데 세는 것이 없으면 조용히 무제한으로 도는 대신 막아야 합니다.
func TestLimitWithoutCounterIsRejected(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxIndexed = 100

	lease := &fakeLease{frontier: 200}
	source := &rangeSource{latest: 199}

	if _, err := NewCrawler(cfg, source, lease, newTestIndexer(t, cfg, source), nil,
		quietLogger()); err == nil {
		t.Error("세는 것 없이 상한만 받았습니다")
	}
}

func newTestIndexer(t *testing.T, cfg CrawlerConfig, source SourceClient) *Indexer {
	t.Helper()
	h := newHarness(t, 2)
	indexer, err := NewIndexer(IndexerConfig{
		NodeID: cfg.NodeID, SourceSite: cfg.SourceSite, ScopeKey: cfg.ScopeKey,
	}, IndexerDeps{
		Source: source, Embedder: h.embedder, Sink: h.sink, Images: h.images,
		Limiter: h.limiter, Models: testModels, Log: quietLogger(),
	})
	if err != nil {
		t.Fatalf("인덱서 생성 실패: %v", err)
	}
	return indexer
}

func newCrawlerWithCounter(t *testing.T, cfg CrawlerConfig, source SourceClient,
	lease LeaseRepository, counter IndexCounter) *Crawler {
	t.Helper()
	crawler, err := NewCrawler(cfg, source, lease, newTestIndexer(t, cfg, source),
		counter, quietLogger())
	if err != nil {
		t.Fatalf("크롤러 생성 실패: %v", err)
	}
	return crawler
}

// 색인이 차도 크롤러가 반환하면 안 됩니다.
//
// 중앙 노드는 같은 프로세스에서 검색과 대시보드를 함께 맡습니다. runAll은
// 작업 하나가 반환하면 오류가 없어도 공용 취소를 걸어 나머지를 전부
// 내립니다. 그래서 "색인이 찼다"가 "검색이 죽었다"가 되어 버립니다.
func TestFullCrawlerKeepsRunning(t *testing.T) {
	repo := &countingRepo{}
	repo.count.Store(500)

	cfg := baseConfig()
	cfg.MaxIndexed = 500

	lease := &fakeLease{frontier: 5000}
	source := &rangeSource{latest: 4999}
	source.fakeSource.failURL = map[string]error{}

	crawler := newCrawlerWithCounter(t, cfg, source, lease, repo)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		crawler.Run(ctx)
		close(done)
	}()

	// 찬 상태에서도 바깥이 끝날 때까지 살아 있어야 합니다.
	select {
	case <-done:
		if ctx.Err() == nil {
			t.Fatal("색인이 찼다고 크롤러가 스스로 끝냈습니다. 검색까지 같이 내려갑니다")
		}
	case <-ctx.Done():
		<-done
	}

	if got := lease.snapshot(); len(got.handedOut) != 0 {
		t.Errorf("찬 상태인데 구간을 %d개 받았습니다", len(got.handedOut))
	}
}

// 상한을 올리면 다시 띄우지 않고도 이어서 돌아야 합니다.
func TestCrawlerResumesWhenRoomAppears(t *testing.T) {
	repo := &countingRepo{}
	repo.count.Store(500)

	cfg := baseConfig()
	cfg.MaxIndexed = 500

	lease := &fakeLease{frontier: 5000}
	source := &rangeSource{latest: 4999}
	source.fakeSource.failURL = map[string]error{}

	crawler := newCrawlerWithCounter(t, cfg, source, lease, repo)
	if !crawler.atCapacity(context.Background()) {
		t.Fatal("찬 상태를 알아보지 못했습니다")
	}

	// 자리가 생기면 다시 받아야 합니다. 기억해 둔 값은 지웁니다.
	repo.count.Store(10)
	crawler.countedAt.Store(0)

	if crawler.atCapacity(context.Background()) {
		t.Error("여유가 생겼는데 계속 찼다고 합니다")
	}
}

// 찬 동안 재시도를 계속 꺼내면 임대할 때마다 시도 횟수가 올라갑니다.
// 다섯 번을 채운 게시물은 나중에 자리가 생겨도 다시 잡히지 않습니다.
func TestFullIndexDoesNotBurnRetries(t *testing.T) {
	repo := &countingRepo{}
	repo.count.Store(500)

	cfg := baseConfig()
	cfg.MaxIndexed = 500

	lease := &fakeLease{frontier: 5000}
	lease.retryQueue = []domain.PostRetry{{ID: 1, SourcePostID: 10}}
	source := &rangeSource{latest: 4999}
	source.fakeSource.failURL = map[string]error{}

	crawler := newCrawlerWithCounter(t, cfg, source, lease, repo)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	crawler.Run(ctx)

	if got := lease.snapshot(); len(got.retryDone) != 0 {
		t.Errorf("찬 상태인데 재시도를 %d건 처리했습니다", len(got.retryDone))
	}
}

// -limit에 걸려 잘라 낸 구간을 완료로 적으면 안 됩니다.
//
// 처리하지 않은 구간이 끝난 것으로 남으면 그 ID 대역에 구멍이 나고,
// 나중에 다시 돌려도 채워지지 않습니다.
func TestLimitCutDoesNotMarkRangeComplete(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxImages = 5

	lease := &fakeLease{frontier: 500}
	source := &rangeSource{latest: 499}
	source.fakeSource.failURL = map[string]error{}

	crawler, _ := newCrawler(t, cfg, source, lease)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	crawler.Run(ctx)

	got := lease.snapshot()
	for _, status := range got.finished {
		if status == domain.RangeCompleted && crawler.Saved() >= cfg.MaxImages {
			// 상한에 걸린 뒤 완료로 적힌 구간이 있는지 봅니다.
			if len(got.releasedRanges) == 0 {
				t.Error("상한에 걸렸는데 반납한 구간이 없습니다")
			}
			return
		}
	}
}
