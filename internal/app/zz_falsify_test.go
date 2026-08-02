package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"saucedust/internal/domain"
)

type alwaysFullSink struct {
	mu    sync.Mutex
	calls int
}

func (s *alwaysFullSink) Submit(context.Context, []domain.IndexedImage) error {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return domain.ErrIndexFull
}

// reuseLease는 실제 reuseRange 의미를 흉내 냅니다.
// 반납된 구간은 attempts가 깎인 채 다시 맨 앞에 놓입니다.
type reuseLease struct {
	fakeLease
	mu         sync.Mutex
	held       *domain.CrawlRange
	handouts   int
	releases   int
	attempts   int
	maxHandout int
}

func (r *reuseLease) AcquireBackfillRange(_ context.Context, req domain.LeaseRequest) (*domain.CrawlRange, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handouts >= r.maxHandout {
		return nil, domain.ErrNoWork
	}
	r.handouts++
	r.attempts++
	if r.attempts >= maxAttemptsForTest {
		return nil, domain.ErrNoWork
	}
	out := domain.CrawlRange{
		ID: 1, SourceSite: req.SourceSite, ScopeKey: req.ScopeKey,
		Direction: domain.DirectionBackfill, LowerID: 1, UpperID: 20,
		Attempts: r.attempts, NodeID: req.NodeID,
	}
	r.held = &out
	return &out, nil
}

func (r *reuseLease) ReleaseRange(_ context.Context, _ *domain.CrawlRange) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releases++
	if r.attempts > 0 {
		r.attempts--
	}
	return nil
}

func (r *reuseLease) FinishRange(_ context.Context, _ *domain.CrawlRange, status domain.RangeStatus, _ int, _ error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fakeLease.finished = append(r.fakeLease.finished, status)
	return nil
}

func (r *reuseLease) InitCatchupState(context.Context, string, string, int64) error { return nil }
func (r *reuseLease) HighWatermark(context.Context, string, string) (int64, error)  { return 9999, nil }
func (r *reuseLease) LeaseRetries(context.Context, string, string, string, int, int64) ([]domain.PostRetry, error) {
	return nil, nil
}

const maxAttemptsForTest = 5

// 주장 2를 재현합니다. MaxIndexed를 빠뜨린 노드가 507을 받으면
// 같은 구간을 시도 횟수 소모 없이 무한히 다시 받는지 봅니다.
func TestFalsifyIndexFullReleaseLoop(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxIndexed = 0
	cfg.BackfillWorkers = 1

	source := &rangeSource{latest: 4999}
	source.fakeSource.failURL = map[string]error{}

	sink := &alwaysFullSink{}
	h := newHarness(t, 2)
	indexer, err := NewIndexer(IndexerConfig{
		NodeID: cfg.NodeID, SourceSite: cfg.SourceSite, ScopeKey: cfg.ScopeKey,
		SubmitBatch: 4,
	}, IndexerDeps{
		Source: source, Embedder: h.embedder, Sink: sink, Images: h.images,
		Limiter: h.limiter, Models: testModels, Log: quietLogger(),
	})
	if err != nil {
		t.Fatalf("인덱서 생성 실패: %v", err)
	}

	lease := &reuseLease{maxHandout: 1000}
	crawler, err := NewCrawler(cfg, source, lease, indexer, nil, quietLogger())
	if err != nil {
		t.Fatalf("크롤러 생성 실패: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	crawler.Run(ctx)

	lease.mu.Lock()
	handouts, releases, attempts := lease.handouts, lease.releases, lease.attempts
	finished := len(lease.fakeLease.finished)
	lease.mu.Unlock()

	t.Logf("임대 %d회, 반납 %d회, 남은 attempts %d, finish %d회, submit %d회",
		handouts, releases, attempts, finished, sink.calls)

	if handouts < 3 {
		t.Errorf("같은 구간을 %d번만 받았습니다. 고리가 없을 수 있습니다", handouts)
	}
	if attempts > 1 {
		t.Errorf("attempts가 %d까지 올랐습니다. 상계되지 않는 것 같습니다", attempts)
	}
	if finished != 0 {
		t.Errorf("finish가 %d회 불렸습니다. 실패로 적히면 고리가 끊깁니다", finished)
	}
}
