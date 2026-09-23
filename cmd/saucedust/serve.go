package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"path/filepath"
	"time"

	"saucedust/internal/adapter/httpapi"
	"saucedust/internal/app"
	"saucedust/internal/domain"
)

// cmdControl은 중앙 노드를 띄웁니다.
// 결과 수신, 검색, 대시보드를 맡고, 원하면 수집도 함께 합니다.
func cmdControl(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("control", flag.ContinueOnError)
	noCrawl := fs.Bool("no-crawl", false, "수집은 하지 않고 서버 역할만 합니다")
	limit := fs.Int64("limit", 0, "이만큼 모으면 멈춥니다. 새 노드를 확인할 때 씁니다")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rt, err := boot(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	if rt.cfg.Role != domain.RoleControl {
		return errors.New("SAUCEDUST_NODE_ROLE이 control이어야 이 명령을 쓸 수 있습니다")
	}
	if err := rt.store.Migrate(ctx); err != nil {
		return err
	}

	embedder, err := rt.newEmbedder()
	if err != nil {
		return err
	}

	// 모델 보고는 노드 행을 참조하므로 등록이 먼저여야 합니다.
	fleet, err := rt.newFleet()
	if err != nil {
		return err
	}
	if err := fleet.Register(ctx); err != nil {
		return err
	}

	// 워커가 아직 안 떠 있을 수 있으므로 수집을 끈 경우에는 모델 확인을 건너뜁니다.
	var probe app.Embedder
	if !*noCrawl {
		probe = embedder
	}
	models, err := rt.activeModels(ctx, probe)
	if err != nil {
		return err
	}

	ingest, err := rt.newIngest(ctx, models)
	if err != nil {
		return err
	}
	index, err := rt.newIndex(false)
	if err != nil {
		return err
	}
	search, err := app.NewSearch(app.SearchConfig{Limit: 5, Candidates: 25}, app.SearchDeps{
		Embedder: embedder, Index: index, Images: rt.store,
		Cache: rt.store, Models: models, Log: rt.log,
	})
	if err != nil {
		return err
	}

	adminUser, adminPass, adminFile, err := httpapi.EnsureAdmin(
		rt.cfg.DataDir, rt.cfg.AdminUser, rt.cfg.AdminPassword, rt.log)
	if err != nil {
		return err
	}
	rt.log.Info("제어 화면", slog.String("url", "http://"+rt.cfg.ControlBind+"/"))

	tgPath := filepath.Join(rt.cfg.DataDir, "telegram.json")
	if saved, ok, err := loadTelegramFile(tgPath); err != nil {
		return err
	} else if ok {
		rt.cfg.TelegramToken = saved
	}
	bots := newBotHost(rt, search, rt.cfg.TelegramToken, tgPath, rt.log)

	server, err := httpapi.New(httpapi.Config{
		Bind:          rt.cfg.ControlBind,
		Token:         rt.cfg.ControlToken,
		NodeTimeout:   rt.cfg.NodeTimeout,
		SourceSite:    rt.cfg.SourceSite,
		ScopeKey:      rt.cfg.ScopeKey,
		AdminUser:     adminUser,
		AdminPassword: adminPass,
		AdminFile:     adminFile,
		NodeID:        rt.cfg.NodeID,
		Role:          string(rt.cfg.Role),
		IndexKind:     string(rt.cfg.IndexKind),
		DataDir:       rt.cfg.DataDir,
		IndexDir:      rt.cfg.IndexDir,
		ThumbDir:      rt.cfg.ThumbDir,
		RangeSize:     rt.cfg.BackfillRangeSize,
	}, httpapi.Deps{
		Ingest: ingest, Search: search, Stats: rt.store, Images: rt.store,
		Index: index, Embedder: embedder, Log: rt.log, Telegram: bots,
		Ops: newWebOps(rt, ingest, embedder, bots, adminUser, adminFile),
	})
	if err != nil {
		return err
	}

	reportCapacity(ctx, rt, models)

	if rt.cfg.ControlToken == "" {
		rt.log.Warn("토큰이 없어 이 컴퓨터 안에서만 씁니다",
			slog.String("bind", rt.cfg.ControlBind),
			slog.String("고치려면", "SAUCEDUST_CONTROL_TOKEN과 SAUCEDUST_CONTROL_BIND를 함께 설정하십시오"))
	}

	tasks := []namedTask{
		{"server", server.Run},
		{"fleet", fleet.Run},
		{"bot", bots.Run},
	}

	if !*noCrawl {
		crawler, chain, limiter, err := rt.newCrawler(ctx, models, embedder, ingest, fleet, *limit)
		if err != nil {
			return err
		}
		fleet.SetNetMode(chain.Mode())
		tasks = append(tasks,
			namedTask{"crawler", crawler.Run},
			namedTask{"telemetry", telemetryLoop(fleet, chain, limiter)})
	}

	return runAll(ctx, rt.log, tasks)
}

// cmdWorker는 작업 노드를 띄웁니다.
// 구간을 임대받아 수집하고, 결과는 중앙 노드로 보냅니다.
func cmdWorker(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	limit := fs.Int64("limit", 0, "이만큼 모으면 멈춥니다. 새 노드를 확인할 때 씁니다")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rt, err := boot(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	if rt.cfg.Role != domain.RoleWorker {
		return errors.New("SAUCEDUST_NODE_ROLE이 worker여야 이 명령을 쓸 수 있습니다")
	}

	embedder, err := rt.newEmbedder()
	if err != nil {
		return err
	}
	if err := embedder.Health(ctx); err != nil {
		return err
	}

	// 모델 보고는 노드 행을 참조하므로 등록이 먼저여야 합니다.
	fleet, err := rt.newFleet()
	if err != nil {
		return err
	}
	if err := fleet.Register(ctx); err != nil {
		return err
	}

	models, err := rt.activeModels(ctx, embedder)
	if err != nil {
		return err
	}

	sink, err := rt.newSink(ctx, models)
	if err != nil {
		return err
	}

	info, err := embedder.Describe(ctx)
	if err != nil {
		return err
	}
	fleet.SetDevice(info.Device)

	crawler, chain, limiter, err := rt.newCrawler(ctx, models, embedder, sink, fleet, *limit)
	if err != nil {
		return err
	}
	fleet.SetNetMode(chain.Mode())

	rt.log.Info("작업 노드를 시작합니다",
		slog.String("node", rt.cfg.NodeID),
		slog.String("device", string(info.Device)),
		slog.String("control", rt.cfg.ControlURL))

	return runAll(ctx, rt.log, []namedTask{
		{"fleet", fleet.Run},
		{"crawler", crawler.Run},
		{"telemetry", telemetryLoop(fleet, chain, limiter)},
	})
}

type netModeSource interface{ Mode() domain.NetMode }

// telemetryLoop은 현재 경로와 동시성 값을 노드 상태에 반영합니다.
// 대시보드가 이 값을 읽어 보여줍니다.
func telemetryLoop(fleet *app.Fleet, chain netModeSource, limiter *app.Limiter) func(context.Context) error {
	return func(ctx context.Context) error {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				limiter.Tick()
				fleet.SetNetMode(chain.Mode())
				fleet.SetConcurrency(limiter.Limit())
			}
		}
	}
}
