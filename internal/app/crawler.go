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
	// MaxIndexed는 색인 전체가 담을 수 있는 장수입니다. 0이면 재지 않습니다.
	//
	// MaxImages와 다릅니다. 저쪽은 이번 실행에서 몇 장 저장했는지를 보고
	// 새 노드를 확인할 때 씁니다. 이쪽은 저장소에 이미 들어 있는 전체를
	// 보고, 메모리가 감당할 수 있는 선을 넘지 않게 막습니다.
	//
	// 여러 노드가 같은 저장소에 넣으므로 각자 제 메모리를 보고 판단하면
	// 틀립니다. 한계는 중앙의 Qdrant 것이지 작업 노드 것이 아닙니다.
	// 그래서 노드마다 같은 값을 두고 공유 저장소의 수를 봅니다.
	MaxIndexed int64

	// BackfillFloor는 과거로 내려가는 하한입니다. 이 번호보다 작은 게시물은
	// 아예 보지 않습니다. 0이면 1번까지 내려갑니다.
	//
	// 메모리가 넉넉하지 않은 곳에서 씁니다. Qdrant가 붙들고 있어야 하는
	// 양은 장수에 정비례하므로, 다 모으고 나서 줄일 수가 없습니다.
	// 최신 것부터 채우니 하한을 두면 최근 구간만 남습니다.
	BackfillFloor int64
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
	saved     atomic.Int64
	stop      context.CancelFunc
	limitDone atomic.Bool

	counter    IndexCounter
	warnedFull atomic.Bool
	lastCount  atomic.Int64
	countedAt  atomic.Int64
}

// IndexCounter는 저장소에 지금 몇 장 들어 있는지 셉니다.
type IndexCounter interface {
	CountImages(ctx context.Context) (int64, error)
}

// Saved는 이번 실행에서 저장한 이미지 수입니다.
func (c *Crawler) Saved() int64 { return c.saved.Load() }

func NewCrawler(cfg CrawlerConfig, source SourceClient, lease LeaseRepository, indexer *Indexer, counter IndexCounter, log *slog.Logger) (*Crawler, error) {
	switch {
	case source == nil:
		return nil, errors.New("수집 대상 클라이언트가 없습니다")
	case lease == nil:
		return nil, errors.New("임대 저장소가 없습니다")
	case indexer == nil:
		return nil, errors.New("인덱서가 없습니다")
	case cfg.MaxIndexed > 0 && counter == nil:
		return nil, errors.New("담을 장수를 정했는데 세는 것이 없습니다")
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
	return &Crawler{cfg: cfg, source: source, lease: lease, indexer: indexer,
		counter: counter, log: log}, nil
}

func (c *Crawler) Run(ctx context.Context) error {
	if err := c.bootstrap(ctx); err != nil {
		return err
	}

	// 상한이 있으면 다 채웠을 때 스스로 멈춥니다.
	// 작업자들이 같은 신호를 보도록 여기서 한 번만 감쌉니다.
	// MaxImages는 확인용이라 다 채우면 프로세스를 끝내는 것이 맞습니다.
	// MaxIndexed는 다릅니다. 수집만 접고 검색은 계속해야 하므로 여기서
	// 취소를 걸지 않습니다.
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
	if c.atCapacity(ctx) {
		return nil
	}

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

	report, trimmed, err := c.index(ctx, posts)

	// 저장한 수는 어느 경로에서 왔든 세야 합니다. 여기서 세지 않으면
	// remaining()이 줄지 않아 -limit이 따라잡기만으로는 멈추지 않고,
	// 자를 때 쓰는 남은 몫도 언제나 상한 그대로가 됩니다.
	c.countSaved(report.Saved)

	if err != nil {
		return err
	}

	// 실패한 게시물은 재시도 큐로 넘기고, 워터마크는 그대로 올립니다.
	// 그래야 실패 한 건이 최신 수집 전체를 막지 않습니다.
	c.enqueueFailures(ctx, report.Failed)

	// 잘라 냈으면 워터마크를 올리면 안 됩니다.
	//
	// 최신 200개를 받아 와 앞의 다섯 개만 처리했는데 200개 전부의
	// 최댓값으로 올리면 나머지 195개는 다시 안 봅니다. 워터마크는
	// 되돌아가지 않으므로 영구 구멍입니다. -limit은 문서가 첫 실행으로
	// 안내하는 명령이라 처음 써 보는 사람이 바로 밟습니다.
	if trimmed {
		return nil
	}

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

		// 찼다고 반환하면 runAll이 검색까지 내립니다. 쉬면서 다시 봅니다.
		// 상한을 올리면 다음 주기에 스스로 이어서 돕니다.
		if c.atCapacity(ctx) {
			if !sleepCtx(ctx, idle) {
				return
			}
			continue
		}

		lease, err := c.lease.AcquireBackfillRange(ctx, domain.LeaseRequest{
			SourceSite: c.cfg.SourceSite,
			ScopeKey:   c.cfg.ScopeKey,
			NodeID:     c.cfg.NodeID,
			RangeSize:  c.rangeSize(),
			FloorID:    c.cfg.BackfillFloor,
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

	// 처리가 임대 만료보다 오래 걸릴 수 있습니다. 기본값으로도 33분이
	// 걸리는데 만료는 30분입니다. 살아 있는 동안 계속 알립니다.
	ctx, stopRenew := context.WithCancel(ctx)
	defer stopRenew()
	go c.renewWhileWorking(ctx, lease)

	posts, err := c.source.PostsInRange(ctx, lease.LowerID, lease.UpperID, c.cfg.Tags)
	if err != nil {
		c.finishOrRelease(ctx, lease, domain.RangeFailed, 0, err)
		return
	}
	if len(posts) == 0 {
		c.finishOrRelease(ctx, lease, domain.RangeEmpty, 0, nil)
		return
	}

	report, trimmed, err := c.index(ctx, posts)

	// 잘라 냈으면 이 구간은 다 한 것이 아닙니다. 오류보다 먼저 봅니다.
	if trimmed {
		c.releaseRange(ctx, lease)
		return
	}
	if err != nil {
		if errors.Is(err, domain.ErrIndexFull) {
			// 기억해 둔 장수가 낡았습니다. 그대로 두면 곧바로 같은
			// 구간을 다시 잡아 내려받고 또 버립니다.
			c.forgetCount()
			c.releaseRange(ctx, lease)
			sleepCtx(ctx, fullBackoff)
			return
		}
		c.finishOrRelease(ctx, lease, domain.RangeFailed, report.Saved, err)
		return
	}

	c.enqueueFailures(ctx, report.Failed)
	c.recordThroughput(report.Saved, time.Since(started))

	status := domain.RangeCompleted
	if len(report.Failed) > 0 {
		status = domain.RangeFailed
	}
	c.finishOrRelease(ctx, lease, status, report.Saved, nil)
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

		// 찬 상태에서 재시도를 계속 꺼내면 임대할 때마다 시도 횟수가
		// 올라가고, 다섯 번을 채운 게시물은 나중에 자리가 생겨도
		// 다시 잡히지 않습니다.
		if c.atCapacity(ctx) {
			continue
		}

		items, err := c.lease.LeaseRetries(ctx, c.cfg.SourceSite, c.cfg.ScopeKey, c.cfg.NodeID,
			c.cfg.RetryBatch, c.cfg.BackfillFloor)
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

	report, _, err := c.index(ctx, []domain.SourcePost{*post})
	switch {
	case errors.Is(err, domain.ErrIndexFull):
		// 색인이 찬 것은 이 게시물의 잘못이 아닙니다. 시도 횟수를
		// 쓰지 않고 돌려놓아야 자리가 생겼을 때 다시 잡힙니다.
		c.releaseRetry(ctx, item)
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

// index는 묶음을 처리하고, 상한 때문에 잘라 냈는지 함께 알려 줍니다.
//
// 잘랐는지를 저장 수로 되짚으면 안 됩니다. 이미 있는 게시물이 섞이면
// 저장 수가 자른 수보다 적어 남은 몫이 0에 닿지 않고, 처리하지 않은
// 구간이 완료로 닫힙니다. 20개 구간에서 5개만 보고 닫으면 나머지
// 15개는 영영 안 모입니다. 두 번 그렇게 틀렸으므로 직접 돌려줍니다.
func (c *Crawler) index(ctx context.Context, posts []domain.SourcePost) (BatchReport, bool, error) {
	// 자리를 먼저 잡고 그만큼만 넘깁니다.
	//
	// 남은 몫을 읽고 나서 저장하면 작업자 둘이 같은 값을 보고 각자
	// 그만큼 저장해 상한의 배수만큼 넘칩니다. -limit 20에 60장이
	// 들어갔습니다. 원자적으로 떼어 오면 넘치지 않습니다.
	take := c.reserve(len(posts))
	trimmed := take < len(posts)
	posts = posts[:take]
	if len(posts) == 0 {
		return BatchReport{}, trimmed, nil
	}
	report, err := c.indexer.IndexBatch(ctx, posts)

	// 멈춤 신호는 처리가 끝난 뒤에 보냅니다. 자리를 잡자마자 보내면
	// 그 자리에서 취소가 걸려 방금 잡은 몫을 처리하지 못합니다.
	c.noteLimitReached(c.saved.Load())
	return report, trimmed, err
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
	// 평생 누적이 아니라 최근 것만 봅니다. 그냥 더하기만 하면 처음
	// 느렸던 구간이 영원히 평균을 눌러 자동 조절이 굳습니다.
	// 절반씩 잊는 방식이라 최근 것이 빨리 반영됩니다.
	if c.throughput.Load() > throughputDecayAt {
		c.throughput.Store(c.throughput.Load() / 2)
		c.elapsed.Store(c.elapsed.Load() / 2)
	}
	c.throughput.Add(uint64(saved))

	// 초 단위로 자르면 1초 미만 구간이 전부 0이 되어 분모가 안 큽니다.
	// 여기 오기 전에 took > 0을 확인했으므로 음수가 될 수 없습니다.
	millis := took.Milliseconds()
	if millis < 1 {
		millis = 1
	}
	c.elapsed.Add(uint64(millis))

	c.countSaved(saved)
}

// reserve는 상한에서 처리할 몫을 원자적으로 떼어 옵니다.
//
// 상한이 없으면 달라는 대로 줍니다. 상한이 있으면 남은 만큼만 주고,
// 준 만큼을 바로 차감해 다른 작업자가 같은 자리를 또 가져가지 못하게
// 합니다. 그래서 세는 값은 "저장한 수"가 아니라 "처리하기로 한 수"이며,
// -limit은 "이만큼 처리하고 멈춘다"는 뜻입니다.
func (c *Crawler) reserve(want int) int {
	if c.cfg.MaxImages <= 0 || want <= 0 {
		return want
	}
	for {
		cur := c.saved.Load()
		left := c.cfg.MaxImages - cur
		if left <= 0 {
			return 0
		}
		take := int64(want)
		if take > left {
			take = left
		}
		if c.saved.CompareAndSwap(cur, cur+take) {
			return int(take)
		}
	}
}

// noteLimitReached는 상한을 채웠으면 한 번만 알리고 멈춥니다.
func (c *Crawler) noteLimitReached(total int64) {
	if c.cfg.MaxImages <= 0 || total < c.cfg.MaxImages || c.stop == nil {
		return
	}
	if c.limitDone.CompareAndSwap(false, true) {
		c.log.Info("정한 만큼 모았습니다. 멈춥니다",
			slog.Int64("처리한 수", total), slog.Int64("상한", c.cfg.MaxImages))
		c.stop()
	}
}

// countSaved는 저장한 수를 세고, 상한을 채웠으면 멈춥니다.
//
// 속도 기록과 나눠 둡니다. 속도는 구간 크기를 정하는 데 쓰는데 따라잡기는
// 구간과 모양이 달라 섞으면 구간 크기가 엉뚱해집니다. 세는 것은 어느
// 경로에서 왔든 해야 합니다.
func (c *Crawler) countSaved(saved int) {
	// 상한이 있으면 reserve가 미리 떼어 가며 이미 셌습니다. 여기서 또
	// 세면 두 번 세어 상한의 절반에서 멈춥니다.
	if saved <= 0 || c.cfg.MaxImages > 0 {
		return
	}
	c.saved.Add(int64(saved))
}

func (c *Crawler) rangeSize() int64 {
	if !c.cfg.Adaptive {
		return c.cfg.BaseRangeSize
	}
	saved := c.throughput.Load()
	millis := c.elapsed.Load()
	if saved == 0 || millis == 0 {
		return c.cfg.BaseRangeSize
	}
	perSecond := float64(saved) * 1000 / float64(millis)
	return RangeSizeFor(perSecond, c.cfg.BaseRangeSize)
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

// releaseRange는 못 넣은 구간을 시도 횟수 없이 돌려놓습니다.
//
// 끝나는 중에도 반납해야 합니다. MaxImages에 닿으면 그 자리에서 취소가
// 걸리는데, 취소됐다고 그냥 나가면 구간이 running으로 버려져 임대가
// 만료될 때까지 아무도 못 잡습니다. 정리는 취소와 무관하게 합니다.
func (c *Crawler) releaseRange(parent context.Context, lease *domain.CrawlRange) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), cleanupWindow)
	defer cancel()

	if err := c.lease.ReleaseRange(ctx, lease); err != nil {
		if errors.Is(err, domain.ErrLeaseConflict) {
			return
		}
		c.log.Warn("구간 반납 실패", slog.String("error", err.Error()))
	}
}

// releaseRetry는 못 넣은 재시도를 시도 횟수 없이 돌려놓습니다.
// releaseRange와 같은 이유로 취소와 무관하게 정리합니다.
func (c *Crawler) releaseRetry(parent context.Context, item domain.PostRetry) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), cleanupWindow)
	defer cancel()

	if err := c.lease.ReleaseRetry(ctx, item); err != nil {
		c.log.Warn("재시도 반납 실패", slog.String("error", err.Error()))
	}
}

// selfStopped는 우리가 스스로 멈췄는지 봅니다.
//
// MaxImages에 닿으면 그 자리에서 취소가 걸리는데, 이때 다른 작업자가
// 하던 구간은 취소 오류로 끝납니다. 그것을 실패로 적으면 시도 횟수를
// 깎고, 취소 때문에 finish가 그냥 나가면 running으로 버려집니다.
// 어느 쪽이든 그 구간이 손해를 봅니다. 우리 사정이므로 반납해야 합니다.
func (c *Crawler) selfStopped() bool {
	return c.cfg.MaxImages > 0 && c.remaining() == 0
}

// finishOrRelease는 우리가 멈춘 것이면 반납하고, 아니면 결과를 적습니다.
func (c *Crawler) finishOrRelease(ctx context.Context, lease *domain.CrawlRange,
	status domain.RangeStatus, saved int, cause error) {
	if c.selfStopped() {
		c.releaseRange(ctx, lease)
		return
	}
	c.finish(ctx, lease, status, saved, cause)
}

// fullBackoff는 색인이 차서 거절당한 뒤 쉬는 시간입니다.
// 쉬지 않으면 구간 하나를 33분 내려받고 버리기를 끝없이 되풀이합니다.
const fullBackoff = 30 * time.Second

// cleanupWindow는 끝나는 중에 뒷정리에 쓰는 시간입니다.
// 취소된 뒤에도 임대를 돌려놓아야 하므로 짧게 따로 잡습니다.
const cleanupWindow = 5 * time.Second

// throughputDecayAt은 이만큼 쌓이면 절반으로 줄여 최근 것에 무게를 줍니다.
const throughputDecayAt = 10_000

// renewLeaseEvery는 임대 갱신 주기입니다.
// 만료가 30분이므로 그 절반보다 짧게 잡아 한 번 놓쳐도 버팁니다.
const renewLeaseEvery = 5 * time.Minute

// renewWhileWorking은 구간을 처리하는 동안 임대를 살려 둡니다.
func (c *Crawler) renewWhileWorking(ctx context.Context, lease *domain.CrawlRange) {
	ticker := time.NewTicker(renewLeaseEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if err := c.lease.RenewRange(ctx, lease); err != nil {
			if errors.Is(err, domain.ErrLeaseConflict) {
				// 이미 빼앗겼습니다. 더 알려도 소용없습니다.
				c.log.Warn("구간 임대를 잃었습니다", slog.Int64("range", lease.ID))
				return
			}
			if ctx.Err() == nil {
				c.log.Warn("임대 갱신 실패", slog.String("error", err.Error()))
			}
		}
	}
}
