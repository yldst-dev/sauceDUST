package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"saucedust/internal/domain"
)

type CrawlerConfig struct {
	NodeID          string
	SourceSite      string
	ScopeKey        string
	Tags            string
	PollEvery       time.Duration
	BackfillWorkers int
	BaseRangeSize   int64
	RetryBatch      int
	Adaptive        bool
	// MaxImages는 이만큼 저장하면 스스로 멈춥니다. 0이면 끝없이 돕니다.
	//
	// 새 노드를 세운 뒤 정말 도는지 확인할 때 씁니다. 확인하려고 끝없이
	// 도는 것을 띄웠다가 손으로 죽이면, 어디까지 갔는지도 얼마나 걸렸는지도
	// 남지 않습니다.
	MaxImages int64
}

// Crawler는 세 가지 일을 동시에 돌립니다.
// 최신 따라잡기 하나, 과거 채우기 여러 개, 실패 재시도 하나입니다.
// 어느 하나가 잠시 실패해도 전체를 멈추지 않고 다음 주기에 다시 시도합니다.
type Crawler struct {
	cfg     CrawlerConfig
	source  SourceClient
	lease   LeaseRepository
	indexer *Indexer
	log     *slog.Logger

	throughput atomic.Uint64
	elapsed    atomic.Uint64

	// saved는 이번 실행에서 저장한 수입니다. MaxImages와 견줍니다.
	// throughput과 나눠 두는 이유는 그쪽이 구간 크기를 정하는 데 쓰여
	// 뜻이 다르기 때문입니다.
	saved atomic.Int64
	stop  context.CancelFunc
}

// Saved는 이번 실행에서 저장한 이미지 수입니다.
func (c *Crawler) Saved() int64 { return c.saved.Load() }

func NewCrawler(cfg CrawlerConfig, source SourceClient, lease LeaseRepository, indexer *Indexer, log *slog.Logger) (*Crawler, error) {
	switch {
	case source == nil:
		return nil, errors.New("수집 대상 클라이언트가 없습니다")
	case lease == nil:
		return nil, errors.New("임대 저장소가 없습니다")
	case indexer == nil:
		return nil, errors.New("인덱서가 없습니다")
	}

	if cfg.PollEvery <= 0 {
		cfg.PollEvery = 15 * time.Second
	}
	if cfg.BackfillWorkers < 0 {
		cfg.BackfillWorkers = 0
	}
	if cfg.BaseRangeSize <= 0 {
		cfg.BaseRangeSize = 10_000
	}
	if cfg.RetryBatch <= 0 {
		cfg.RetryBatch = 32
	}
	if log == nil {
		log = slog.Default()
	}
	return &Crawler{cfg: cfg, source: source, lease: lease, indexer: indexer, log: log}, nil
}

func (c *Crawler) Run(ctx context.Context) error {
	if err := c.bootstrap(ctx); err != nil {
		return err
	}

	// 상한이 있으면 다 채웠을 때 스스로 멈춥니다.
	// 작업자들이 같은 신호를 보도록 여기서 한 번만 감쌉니다.
	if c.cfg.MaxImages > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		c.stop = cancel
	}

	var wg sync.WaitGroup
	start := func(name string, fn func(context.Context)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(ctx)
			c.log.Info("작업자가 멈췄습니다", slog.String("worker", name))
		}()
	}

	start("catchup", c.runCatchup)
	start("retry", c.runRetry)
	for i := 0; i < c.cfg.BackfillWorkers; i++ {
		start(fmt.Sprintf("backfill-%d", i), c.runBackfill)
	}

	wg.Wait()
	return ctx.Err()
}

// bootstrap은 처음 실행할 때 진행점을 최신 게시물 번호로 잡습니다.
func (c *Crawler) bootstrap(ctx context.Context) error {
	latest, err := c.source.LatestPostID(ctx)
	if err != nil {
		return fmt.Errorf("최신 게시물 번호를 얻지 못했습니다: %w", err)
	}
	if err := c.lease.InitCatchupState(ctx, c.cfg.SourceSite, c.cfg.ScopeKey, latest); err != nil {
		return fmt.Errorf("크롤 상태를 초기화하지 못했습니다: %w", err)
	}
	c.log.Info("수집 시작점을 정했습니다", slog.Int64("latest_post", latest))
	return nil
}

func (c *Crawler) runCatchup(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.PollEvery)
	defer ticker.Stop()

	for {
		if err := c.catchupOnce(ctx); err != nil && ctx.Err() == nil {
			c.log.Warn("최신 따라잡기 실패", slog.String("error", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Crawler) catchupOnce(ctx context.Context) error {
	watermark, err := c.lease.HighWatermark(ctx, c.cfg.SourceSite, c.cfg.ScopeKey)
	if err != nil {
		return err
	}

	posts, err := c.source.PostsAfter(ctx, watermark, c.cfg.Tags, 200)
	if err != nil {
		return err
	}
	if len(posts) == 0 {
		return nil
	}

	report, err := c.index(ctx, posts)
	if err != nil {
		return err
	}

	// 실패한 게시물은 재시도 큐로 넘기고, 워터마크는 그대로 올립니다.
	// 그래야 실패 한 건이 최신 수집 전체를 막지 않습니다.
	c.enqueueFailures(ctx, report.Failed)

	var highest int64
	for _, post := range posts {
		if post.PostID > highest {
			highest = post.PostID
		}
	}
	if highest > watermark {
		return c.lease.AdvanceWatermark(ctx, c.cfg.SourceSite, c.cfg.ScopeKey, highest)
	}
	return nil
}

func (c *Crawler) runBackfill(ctx context.Context) {
	idle := 30 * time.Second

	for {
		if ctx.Err() != nil {
			return
		}

		lease, err := c.lease.AcquireBackfillRange(ctx, domain.LeaseRequest{
			SourceSite: c.cfg.SourceSite,
			ScopeKey:   c.cfg.ScopeKey,
			NodeID:     c.cfg.NodeID,
			RangeSize:  c.rangeSize(),
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !errors.Is(err, domain.ErrNoWork) {
				c.log.Warn("구간 임대 실패", slog.String("error", err.Error()))
			}
			if !sleepCtx(ctx, idle) {
				return
			}
			continue
		}

		c.processRange(ctx, lease)
	}
}

func (c *Crawler) processRange(ctx context.Context, lease *domain.CrawlRange) {
	started := time.Now()

	posts, err := c.source.PostsInRange(ctx, lease.LowerID, lease.UpperID, c.cfg.Tags)
	if err != nil {
		c.finish(ctx, lease, domain.RangeFailed, 0, err)
		return
	}
	if len(posts) == 0 {
		c.finish(ctx, lease, domain.RangeEmpty, 0, nil)
		return
	}

	report, err := c.index(ctx, posts)
	if err != nil {
		c.finish(ctx, lease, domain.RangeFailed, report.Saved, err)
		return
	}

	c.enqueueFailures(ctx, report.Failed)
	c.recordThroughput(report.Saved, time.Since(started))

	status := domain.RangeCompleted
	if len(report.Failed) > 0 {
		status = domain.RangeFailed
	}
	c.finish(ctx, lease, status, report.Saved, nil)
}

func (c *Crawler) finish(ctx context.Context, lease *domain.CrawlRange, status domain.RangeStatus, saved int, cause error) {
	if ctx.Err() != nil {
		return
	}
	if err := c.lease.FinishRange(ctx, lease, status, saved, cause); err != nil {
		if errors.Is(err, domain.ErrLeaseConflict) {
			c.log.Info("구간 임대가 이미 회수되어 결과를 버립니다",
				slog.Int64("range", lease.ID))
			return
		}
		c.log.Warn("구간 완료 처리 실패", slog.String("error", err.Error()))
	}
}

func (c *Crawler) runRetry(ctx context.Context) {
	ticker := time.NewTicker(c.cfg.PollEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		items, err := c.lease.LeaseRetries(ctx, c.cfg.SourceSite, c.cfg.ScopeKey, c.cfg.NodeID, c.cfg.RetryBatch)
		if err != nil {
			if ctx.Err() == nil {
				c.log.Warn("재시도 임대 실패", slog.String("error", err.Error()))
			}
			continue
		}
		for _, item := range items {
			c.retryOne(ctx, item)
		}
	}
}

func (c *Crawler) retryOne(ctx context.Context, item domain.PostRetry) {
	post, err := c.source.PostByID(ctx, item.SourcePostID)
	if err != nil {
		c.reschedule(ctx, item, err)
		return
	}

	report, err := c.index(ctx, []domain.SourcePost{*post})
	switch {
	case err != nil:
		c.reschedule(ctx, item, err)
	case len(report.Failed) > 0:
		c.reschedule(ctx, item, errors.New("재시도에서도 실패했습니다"))
	default:
		if err := c.lease.FinishRetry(ctx, item, domain.RetrySucceeded, nil); err != nil && ctx.Err() == nil {
			c.log.Warn("재시도 완료 처리 실패", slog.String("error", err.Error()))
		}
	}
}

func (c *Crawler) reschedule(ctx context.Context, item domain.PostRetry, cause error) {
	if ctx.Err() != nil {
		return
	}
	delay := retryDelay(item.Attempts)
	if err := c.lease.RescheduleRetry(ctx, item, delay, cause); err != nil {
		c.log.Warn("재시도 재예약 실패", slog.String("error", err.Error()))
	}
}

func (c *Crawler) index(ctx context.Context, posts []domain.SourcePost) (BatchReport, error) {
	// 상한이 있으면 남은 만큼만 넘깁니다. 묶음이 끝난 뒤에 세면 한 묶음
	// 크기만큼 넘칩니다. 구간이 50개면 20을 시켜도 50을 받아 옵니다.
	// 여기서 자르면 필요 없는 것을 내려받지도 않습니다.
	if left := c.remaining(); left >= 0 && int64(len(posts)) > left {
		posts = posts[:left]
	}
	if len(posts) == 0 {
		return BatchReport{}, nil
	}
	return c.indexer.IndexBatch(ctx, posts)
}

// remaining은 상한까지 남은 수입니다. 상한이 없으면 -1입니다.
func (c *Crawler) remaining() int64 {
	if c.cfg.MaxImages <= 0 {
		return -1
	}
	left := c.cfg.MaxImages - c.saved.Load()
	if left < 0 {
		return 0
	}
	return left
}

func (c *Crawler) enqueueFailures(ctx context.Context, failed []int64) {
	if len(failed) == 0 || ctx.Err() != nil {
		return
	}
	if err := c.lease.EnqueueRetries(ctx, c.cfg.SourceSite, c.cfg.ScopeKey, failed, time.Minute); err != nil {
		c.log.Warn("재시도 등록 실패", slog.String("error", err.Error()))
	}
}

// rangeSize는 최근 처리 속도로 구간 크기를 정합니다.
// 느린 노드가 큰 구간을 오래 붙잡아 다른 노드를 놀게 하지 않기 위해서입니다.
func (c *Crawler) recordThroughput(saved int, took time.Duration) {
	if saved <= 0 || took <= 0 {
		return
	}
	c.throughput.Add(uint64(saved))
	c.elapsed.Add(uint64(took.Seconds()))

	// 상한을 채웠으면 멈춥니다. 이미 돌고 있는 묶음은 끝까지 가므로
	// 실제 저장 수는 상한을 조금 넘을 수 있습니다.
	total := c.saved.Add(int64(saved))
	if c.cfg.MaxImages > 0 && total >= c.cfg.MaxImages && c.stop != nil {
		c.log.Info("정한 만큼 모았습니다. 멈춥니다",
			slog.Int64("모은 수", total), slog.Int64("상한", c.cfg.MaxImages))
		c.stop()
	}
}

func (c *Crawler) rangeSize() int64 {
	if !c.cfg.Adaptive {
		return c.cfg.BaseRangeSize
	}
	saved := c.throughput.Load()
	seconds := c.elapsed.Load()
	if saved == 0 || seconds == 0 {
		return c.cfg.BaseRangeSize
	}
	return RangeSizeFor(float64(saved)/float64(seconds), c.cfg.BaseRangeSize)
}

func retryDelay(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 6 {
		attempts = 6
	}
	return time.Duration(1<<attempts) * time.Minute
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
