package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"saucedust/internal/adapter/danbooru"
	"saucedust/internal/adapter/embedworker"
	"saucedust/internal/adapter/filestore"
	"saucedust/internal/adapter/flatindex"
	"saucedust/internal/adapter/httpapi"
	"saucedust/internal/adapter/netpath"
	"saucedust/internal/adapter/postgres"
	"saucedust/internal/adapter/qdrant"
	"saucedust/internal/adapter/telegram"
	"saucedust/internal/app"
	"saucedust/internal/config"
	"saucedust/internal/domain"
)

// 이 파일이 조립 지점입니다. 어떤 구현이 어떤 포트에 꽂히는지 여기서만 정합니다.
// 안쪽 계층은 서로의 존재를 모릅니다.

type nodeRuntime struct {
	cfg   *config.Config
	log   *slog.Logger
	store *postgres.Store

	indexOnce  sync.Once
	index      app.VectorIndex
	indexClose func() error
	indexErr   error
}

func (r *nodeRuntime) Close() {
	// 색인을 먼저 닫습니다. 걸어 둔 파일과 폴더 잠금을 놓아야 다음
	// 명령이 들어올 수 있습니다.
	if r.indexClose != nil {
		if err := r.indexClose(); err != nil {
			r.log.Warn("색인을 닫지 못했습니다", slog.String("error", err.Error()))
		}
		r.indexClose = nil
	}
	if r.store != nil {
		r.store.Close()
	}
}

func boot(ctx context.Context) (*nodeRuntime, error) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel(),
	}))

	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(findRoot(root))
	if err != nil {
		return nil, err
	}

	store, err := postgres.Open(ctx, postgres.Options{
		URL:            cfg.DatabaseURL,
		MaxConns:       cfg.PoolSize(),
		AcquireTimeout: cfg.AcquireTimeout,
	})
	if err != nil {
		return nil, err
	}
	return &nodeRuntime{cfg: cfg, log: log, store: store}, nil
}

func logLevel() slog.Level {
	if os.Getenv("SAUCEDUST_DEBUG") != "" {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func findRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// activeModels는 등록된 모델을 읽고, 이 노드의 워커가 같은 모델을 올렸는지 확인합니다.
// 노드마다 다른 모델을 쓰면 벡터를 비교할 수 없으므로 여기서 막습니다.
func (r *nodeRuntime) activeModels(ctx context.Context, embedder app.Embedder) ([]domain.EmbeddingModel, error) {
	models, err := r.store.ActiveModels(ctx)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("등록된 활성 모델이 없습니다. saucedust model add로 먼저 등록하십시오")
	}
	if embedder == nil {
		return models, nil
	}

	info, err := embedder.Describe(ctx)
	if err != nil {
		return nil, fmt.Errorf("임베딩 워커에 물어보지 못했습니다: %w", err)
	}
	if err := r.store.VerifyNodeModels(ctx, r.cfg.NodeID, info.Models); err != nil {
		return nil, err
	}
	r.log.Info("모델 구성을 확인했습니다",
		slog.String("device", string(info.Device)), slog.Int("models", len(models)))
	return models, nil
}

func (r *nodeRuntime) newEmbedder() (*embedworker.Client, error) {
	return embedworker.New(embedworker.Options{
		BaseURL: r.cfg.EmbedWorkerURL,
		Timeout: 3 * time.Minute,
	})
}

// newIndex는 벡터 색인을 냅니다. 프로세스 안에서 하나만 둡니다.
//
// 납작한 색인은 파일을 직접 붙입니다. 인스턴스가 둘이면 각자 제 자리 수를
// 세고 그 자리에 쓰므로 서로를 덮습니다. Qdrant는 서버라 몇 개를 만들어도
// 괜찮았지만 같은 배선을 쓰므로 여기서 하나로 묶습니다.
//
// readOnly는 처음 부를 때만 봅니다. 한 프로세스가 두 역할을 하지 않습니다.
func (r *nodeRuntime) newIndex(readOnly bool) (app.VectorIndex, error) {
	r.indexOnce.Do(func() {
		switch r.cfg.IndexKind {
		case domain.IndexQdrant:
			r.index, r.indexErr = qdrant.New(qdrant.Options{
				BaseURL:  r.cfg.QdrantURL,
				APIKey:   r.cfg.QdrantAPIKey,
				Quantize: true,
				Log:      r.log,
			})
		default:
			var store *flatindex.Store
			store, r.indexErr = flatindex.New(flatindex.Options{
				Dir:      r.cfg.IndexDir,
				Source:   r.store,
				ReadOnly: readOnly,
				Log:      r.log,
			})
			if r.indexErr == nil {
				r.index, r.indexClose = store, store.Close
			}
		}
	})
	return r.index, r.indexErr
}

// pathMemory는 학습한 경로를 얼마나 오래 믿을지입니다.
// 차단 장비 설정은 바뀌므로 무한정 믿으면 안 됩니다.
const pathMemory = 24 * time.Hour

func (r *nodeRuntime) newNetChain() (*netpath.Chain, error) {
	chain, err := r.newBareNetChain()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	known, err := r.store.BestPaths(ctx, r.cfg.NodeID, pathMemory)
	if err != nil {
		r.log.Debug("지난 경로 기록을 읽지 못했습니다", slog.String("error", err.Error()))
		return chain, nil
	}
	if applied := chain.Prime(known); applied > 0 {
		r.log.Info("지난 실행에서 통했던 경로를 씁니다", slog.Int("hosts", applied))
	}
	return chain, nil
}

func (r *nodeRuntime) newBareNetChain() (*netpath.Chain, error) {
	return netpath.New(netpath.Options{
		Order:         r.cfg.NetOrder,
		ProxyURL:      r.cfg.ProxyURL,
		FragmentParts: r.cfg.FragmentParts,
		FragmentDelay: r.cfg.FragmentDelay,
		AllowPrivate:  r.cfg.AllowPrivate,
		Logger:        r.log,
		Recorder: func(ctx context.Context, host string, mode domain.NetMode, ok bool, latency time.Duration, detail string) {
			// 측정 기록은 대시보드용 참고 자료이므로 실패해도 흐름을 막지 않습니다.
			recCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer cancel()
			if err := r.store.RecordProbe(recCtx, r.cfg.NodeID, host, mode, ok, latency, detail); err != nil {
				r.log.Debug("경로 기록 실패", slog.String("error", err.Error()))
			}
		},
	})
}

func (r *nodeRuntime) newSource(chain *netpath.Chain) (*danbooru.Client, error) {
	return danbooru.New(chain, danbooru.Options{
		BaseURL:       r.cfg.DanbooruAPI,
		UserAgent:     r.cfg.UserAgent,
		RatePerSecond: r.cfg.RatePerSecond,
		Burst:         r.cfg.RateBurst,
		Login:         r.cfg.DanbooruLogin,
		APIKey:        r.cfg.DanbooruAPIKey,
	})
}

func (r *nodeRuntime) newLimiter() *app.Limiter {
	return app.NewLimiter(app.LimiterConfig{
		Start:    r.cfg.Concurrency,
		Min:      r.cfg.MinConcurrency,
		Max:      r.cfg.MaxConcurrency,
		Interval: 10 * time.Second,
		Enabled:  r.cfg.Adaptive,
	}, app.SystemClock)
}

func (r *nodeRuntime) newFleet() (*app.Fleet, error) {
	return app.NewFleet(app.FleetConfig{
		NodeID:         r.cfg.NodeID,
		Role:           r.cfg.Role,
		HeartbeatEvery: r.cfg.HeartbeatEvery,
		NodeTimeout:    r.cfg.NodeTimeout,
	}, r.store, r.log)
}

// newBot은 텔레그램 봇을 만듭니다. 토큰이 없으면 nil을 돌려줍니다.
//
// 봇은 우회 경로를 그대로 씁니다. 텔레그램도 막힐 수 있고, 막히면 같은 방법으로
// 뚫어야 하기 때문입니다.
func (r *nodeRuntime) newBot(search app.ImageSearcher) (*app.Bot, error) {
	if r.cfg.TelegramToken == "" {
		return nil, nil
	}

	chain, err := r.newNetChain()
	if err != nil {
		return nil, err
	}
	gateway, err := telegram.New(chain, telegram.Options{
		Token:       r.cfg.TelegramToken,
		PollTimeout: r.cfg.TelegramPollTimeout,
	})
	if err != nil {
		return nil, err
	}

	return app.NewBot(app.BotConfig{
		AllowedUsers: r.cfg.TelegramAllowedUsers,
	}, gateway, search, r.log)
}

// newIngest는 control 노드에서 결과를 실제로 저장하는 유스케이스를 만듭니다.
func (r *nodeRuntime) newIngest(ctx context.Context, models []domain.EmbeddingModel) (*app.Ingest, error) {
	index, err := r.newIndex(false)
	if err != nil {
		return nil, err
	}
	for _, m := range models {
		if err := index.EnsureCollection(ctx, m); err != nil {
			return nil, err
		}
	}

	thumbs, err := filestore.NewThumbStore(r.cfg.ThumbDir)
	if err != nil {
		return nil, err
	}
	r.log.Info("축소본 저장 위치", slog.String("path", thumbs.Root()))

	return app.NewIngest(app.IngestDeps{
		Images: r.store, Vector: r.store, Index: index,
		Thumbs: thumbs, Models: models, Log: r.log,
		MaxIndexed: r.cfg.MaxIndexed, Counter: r.store,
	})
}

// newSink는 결과를 어디로 보낼지 정합니다.
// control 노드는 저장소에 바로 쓰고, worker 노드는 중앙 서버로 보냅니다.
func (r *nodeRuntime) newSink(ctx context.Context, models []domain.EmbeddingModel) (app.VectorSink, error) {
	if r.cfg.Role == domain.RoleControl {
		return r.newIngest(ctx, models)
	}
	return httpapi.NewClient(httpapi.ClientOptions{
		BaseURL: r.cfg.ControlURL,
		Token:   r.cfg.ControlToken,
		NodeID:  r.cfg.NodeID,
	})
}

// newCrawler는 수집에 필요한 모든 조각을 엮습니다.
func (r *nodeRuntime) newCrawler(ctx context.Context, models []domain.EmbeddingModel, embedder app.Embedder, sink app.VectorSink, fleet *app.Fleet, maxImages int64) (*app.Crawler, *netpath.Chain, *app.Limiter, error) {
	chain, err := r.newNetChain()
	if err != nil {
		return nil, nil, nil, err
	}
	source, err := r.newSource(chain)
	if err != nil {
		return nil, nil, nil, err
	}

	limiter := r.newLimiter()
	indexer, err := app.NewIndexer(app.IndexerConfig{
		NodeID:     r.cfg.NodeID,
		SourceSite: r.cfg.SourceSite,
		ScopeKey:   r.cfg.ScopeKey,
	}, app.IndexerDeps{
		Source: source, Embedder: embedder, Sink: sink, Images: r.store,
		Limiter: limiter, Counters: fleet.Counters(), Models: models, Log: r.log,
	})
	if err != nil {
		return nil, nil, nil, err
	}

	crawler, err := app.NewCrawler(app.CrawlerConfig{
		NodeID:          r.cfg.NodeID,
		SourceSite:      r.cfg.SourceSite,
		ScopeKey:        r.cfg.ScopeKey,
		Tags:            r.cfg.IndexTags,
		PollEvery:       r.cfg.PollEvery,
		BackfillWorkers: r.cfg.BackfillWorkers,
		BaseRangeSize:   r.cfg.BackfillRangeSize,
		BackfillFloor:   r.cfg.BackfillFloor,
		MaxIndexed:      r.cfg.MaxIndexed,
		Adaptive:        r.cfg.Adaptive,
		MaxImages:       maxImages,
	}, source, r.store, indexer, r.store, r.log)
	if err != nil {
		return nil, nil, nil, err
	}
	return crawler, chain, limiter, nil
}
