package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"saucedust/internal/domain"
)

type IndexerConfig struct {
	NodeID        string
	SourceSite    string
	ScopeKey      string
	Tags          string
	MaxImageBytes int64
	SubmitBatch   int
	StepTimeout   time.Duration
}

// Indexer는 게시물 묶음을 받아 이미지를 내려받고 벡터를 만들어 내보냅니다.
// 어디서 받아 어디로 보내는지는 포트가 감추므로, 이 타입은 순서와 실패 처리만 압니다.
type Indexer struct {
	cfg      IndexerConfig
	source   SourceClient
	embedder Embedder
	sink     VectorSink
	images   ImageRepository
	limiter  *Limiter
	counters *Counters
	models   []domain.EmbeddingModel
	log      *slog.Logger
}

type IndexerDeps struct {
	Source   SourceClient
	Embedder Embedder
	Sink     VectorSink
	Images   ImageRepository
	Limiter  *Limiter
	Counters *Counters
	Models   []domain.EmbeddingModel
	Log      *slog.Logger
}

// BatchReport는 묶음 하나를 처리한 결과입니다.
type BatchReport struct {
	Total    int
	Saved    int
	Skipped  int
	Existing int
	Failed   []int64
}

func NewIndexer(cfg IndexerConfig, deps IndexerDeps) (*Indexer, error) {
	switch {
	case deps.Source == nil:
		return nil, errors.New("수집 대상 클라이언트가 없습니다")
	case deps.Embedder == nil:
		return nil, errors.New("임베딩 클라이언트가 없습니다")
	case deps.Sink == nil:
		return nil, errors.New("결과를 내보낼 곳이 없습니다")
	case deps.Images == nil:
		return nil, errors.New("이미지 저장소가 없습니다")
	case deps.Limiter == nil:
		return nil, errors.New("동시성 제어기가 없습니다")
	case len(deps.Models) == 0:
		return nil, errors.New("활성 임베딩 모델이 없습니다")
	}

	if cfg.MaxImageBytes <= 0 {
		cfg.MaxImageBytes = 32 << 20
	}
	if cfg.SubmitBatch <= 0 {
		cfg.SubmitBatch = 32
	}
	if cfg.StepTimeout <= 0 {
		cfg.StepTimeout = 120 * time.Second
	}
	if deps.Counters == nil {
		deps.Counters = &Counters{}
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}

	return &Indexer{
		cfg: cfg, source: deps.Source, embedder: deps.Embedder,
		sink: deps.Sink, images: deps.Images, limiter: deps.Limiter,
		counters: deps.Counters, models: deps.Models, log: deps.Log,
	}, nil
}

// IndexBatch는 게시물 묶음을 처리합니다. 개별 실패는 보고서에 모아 돌려주고
// 묶음 전체를 중단시키지 않습니다. 중단은 컨텍스트가 끝났을 때만 일어납니다.
func (ix *Indexer) IndexBatch(ctx context.Context, posts []domain.SourcePost) (BatchReport, error) {
	report := BatchReport{Total: len(posts)}
	if len(posts) == 0 {
		return report, nil
	}

	pending := make([]domain.SourcePost, 0, len(posts))
	ids := make([]int64, 0, len(posts))
	for _, post := range posts {
		if reason := post.SkipReason(); reason != "" {
			report.Skipped++
			continue
		}
		pending = append(pending, post)
		ids = append(ids, post.PostID)
	}
	if len(pending) == 0 {
		return report, nil
	}

	// 벡터까지 다 있는 것만 끝난 것으로 봅니다. 행만 있고 벡터가 없는
	// 것을 건너뛰면 그 이미지는 검색에 영영 안 걸립니다.
	existing, err := ix.images.ExistingPostIDs(ctx, ix.cfg.SourceSite, ids, ix.modelIDs())
	if err != nil {
		return report, fmt.Errorf("중복 확인에 실패했습니다: %w", err)
	}

	work := pending[:0]
	for _, post := range pending {
		if _, done := existing[post.PostID]; done {
			report.Existing++
			continue
		}
		work = append(work, post)
	}
	if len(work) == 0 {
		return report, nil
	}

	var (
		mu      sync.Mutex
		batch   = make([]domain.IndexedImage, 0, ix.cfg.SubmitBatch)
		wg      sync.WaitGroup
		flushed error
	)

	// 전송은 다운로드 슬롯 밖에서 합니다. 동시에 여러 묶음이 올라가도 되지만
	// 중앙 노드를 밀어붙이지 않도록 두 개까지만 허용합니다.
	senders := make(chan struct{}, 2)

	flush := func(ctx context.Context) {
		senders <- struct{}{}
		defer func() { <-senders }()

		mu.Lock()
		if len(batch) == 0 {
			mu.Unlock()
			return
		}
		ready := batch
		batch = make([]domain.IndexedImage, 0, ix.cfg.SubmitBatch)
		mu.Unlock()

		if err := ix.sink.Submit(ctx, ready); err != nil {
			mu.Lock()
			if flushed == nil {
				flushed = err
			}
			for _, item := range ready {
				report.Failed = append(report.Failed, item.Image.SourcePostID)
			}
			mu.Unlock()
			ix.counters.Failed.Add(int64(len(ready)))
			return
		}

		mu.Lock()
		report.Saved += len(ready)
		mu.Unlock()
		ix.counters.Saved.Add(int64(len(ready)))
	}

	for _, post := range work {
		if ctx.Err() != nil {
			break
		}

		release, err := ix.limiter.Acquire(ctx)
		if err != nil {
			break
		}

		wg.Add(1)
		go func(post domain.SourcePost) {
			defer wg.Done()

			indexed, err := ix.processPost(ctx, post)
			ix.limiter.Report(classify(err))

			// 슬롯을 여기서 놓습니다. 전송하는 동안 붙잡고 있으면
			// 그만큼 다른 이미지를 내려받지 못합니다.
			release()

			if err != nil {
				if ctx.Err() == nil {
					ix.log.Warn("게시물 처리 실패",
						slog.Int64("post", post.PostID), slog.String("error", err.Error()))
					mu.Lock()
					report.Failed = append(report.Failed, post.PostID)
					mu.Unlock()
					ix.counters.Failed.Add(1)
				}
				return
			}

			mu.Lock()
			batch = append(batch, *indexed)
			full := len(batch) >= ix.cfg.SubmitBatch
			mu.Unlock()

			if full {
				flush(ctx)
			}
		}(post)
	}

	wg.Wait()

	// 마지막 전송은 취소되더라도 반드시 해야 합니다. 여기까지 온 결과는
	// 이미 내려받고 임베딩까지 끝낸 것이라 버리면 그 비용을 다시 치릅니다.
	flush(gracePeriod(ctx, ix.cfg.StepTimeout))

	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	return report, flushed
}

// gracePeriod는 컨텍스트가 이미 끝났으면 저장만 마칠 짧은 시간을 새로 줍니다.
// 아직 살아 있으면 그대로 씁니다.
func gracePeriod(ctx context.Context, limit time.Duration) context.Context {
	if ctx.Err() == nil {
		return ctx
	}
	if limit <= 0 {
		limit = 30 * time.Second
	}
	// 호출자가 정리를 기다려야 하므로 취소 함수는 버리고 시간으로만 제한합니다.
	grace, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	context.AfterFunc(grace, cancel)
	return grace
}

// processPost는 한 장의 흐름입니다. 내려받고, 워커에 한 번 보내고, 결과를 조립합니다.
// 이미지 디코드는 워커에서 한 번만 일어나므로 여기서는 바이트만 다룹니다.
func (ix *Indexer) processPost(ctx context.Context, post domain.SourcePost) (*domain.IndexedImage, error) {
	url := post.DownloadURL()
	if url == "" {
		return nil, errors.New("내려받을 URL이 없습니다")
	}

	downloadCtx, cancel := context.WithTimeout(ctx, ix.cfg.StepTimeout)
	raw, err := ix.source.Download(downloadCtx, url, ix.cfg.MaxImageBytes)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("내려받기 실패: %w", err)
	}
	ix.counters.Downloaded.Add(1)

	embedCtx, cancel := context.WithTimeout(ctx, ix.cfg.StepTimeout)
	result, err := ix.embedder.Embed(embedCtx, raw)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("임베딩 실패: %w", err)
	}
	if err := result.Validate(ix.models); err != nil {
		return nil, err
	}
	ix.counters.Embedded.Add(1)

	width, height := result.Hashes.Width, result.Hashes.Height
	if width == 0 {
		width = post.Width
	}
	if height == 0 {
		height = post.Height
	}

	return &domain.IndexedImage{
		Image: domain.Image{
			SourceSite:   post.Site,
			SourcePostID: post.PostID,
			SourceURL:    post.SourceURL,
			CanonicalURL: post.CanonicalURL,
			FileURL:      url,
			PreviewURL:   post.PreviewURL,
			MD5:          post.MD5,
			PHash:        result.Hashes.PHash,
			DHash:        result.Hashes.DHash,
			Width:        width,
			Height:       height,
			FileSize:     int64(len(raw)),
			Rating:       post.Rating,
			Score:        post.Score,
			Tags:         post.Tags,
			ArtistTags:   post.ArtistTags,
			IndexedBy:    ix.cfg.NodeID,
		},
		Vectors: result.Vectors,
		Thumb:   result.Thumb,
	}, nil
}

// retryabler는 어댑터가 상태 코드 기반으로 재시도 가치를 알려줄 때 쓰는 계약입니다.
type retryabler interface {
	Retryable() bool
}

type throttled interface {
	Throttled() bool
}

// classify는 오류를 동시성 조절기가 이해하는 신호로 바꿉니다.
func classify(err error) Outcome {
	if err == nil {
		return OutcomeOK
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return OutcomeTimeout
	}

	var t throttled
	if errors.As(err, &t) && t.Throttled() {
		return OutcomeThrottled
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return OutcomeTimeout
	}

	var r retryabler
	if errors.As(err, &r) && r.Retryable() {
		return OutcomeThrottled
	}
	return OutcomeError
}

// modelIDs는 지금 쓰는 모델 이름을 냅니다.
func (ix *Indexer) modelIDs() []string {
	out := make([]string, 0, len(ix.models))
	for _, m := range ix.models {
		out = append(out, m.ID)
	}
	return out
}
