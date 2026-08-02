package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"saucedust/internal/adapter/embedworker"
	"saucedust/internal/adapter/postgres"
	"saucedust/internal/adapter/telegram"
	"saucedust/internal/app"
)

// buildVersion은 빌드 시점에 주입됩니다. scripts/build_nodes.sh를 보십시오.
var buildVersion = "dev"

func main() {
	if err := run(); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		fmt.Fprintf(os.Stderr, "오류: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		usage()
		return nil
	}

	args := os.Args[2:]
	switch os.Args[1] {
	case "control":
		return cmdControl(ctx, args)
	case "worker":
		return cmdWorker(ctx, args)
	case "migrate":
		return cmdMigrate(ctx)
	case "node":
		return cmdNode(ctx, args)
	case "model":
		return cmdModel(ctx, args)
	case "probe":
		return cmdProbe(ctx, args)
	case "ranges":
		return cmdRanges(ctx, args)
	case "setup":
		return cmdSetup(ctx, args)
	case "doctor":
		return cmdDoctor(ctx, args)
	case "verify":
		return cmdVerify(ctx, args)
	case "stats":
		return cmdStats(ctx, args)
	case "check-post":
		return cmdCheckPost(ctx, args)
	case "rebuild":
		return cmdRebuild(ctx, args)
	case "reembed":
		return cmdReembed(ctx, args)
	case "version", "-v", "--version":
		fmt.Printf("saucedust %s (%s)\n", buildVersion, app.Version)
		return nil
	case "help", "-h", "--help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("알 수 없는 명령입니다: %s", os.Args[1])
	}
}

func usage() {
	fmt.Print(`saucedust

실행:
  saucedust control            중앙 노드를 띄웁니다. 결과 수신, 검색, 대시보드를 맡습니다
  saucedust control -no-crawl  수집 없이 서버 역할만 합니다
  saucedust worker             작업 노드를 띄웁니다. 수집해서 중앙으로 보냅니다

관리:
  saucedust migrate            데이터베이스 스키마를 적용합니다
  saucedust node ls            등록된 노드를 보여줍니다
  saucedust node reclaim       응답 없는 노드의 임대를 회수합니다
  saucedust model ls           등록된 임베딩 모델을 보여줍니다
  saucedust model add          임베딩 모델을 등록합니다
  saucedust probe              네트워크 경로를 측정합니다
  saucedust ranges status      수집 구간 상태를 보여줍니다
  saucedust ranges failed      재시도를 소진한 구간을 봅니다
  saucedust ranges reset       그 구간을 되살립니다
  saucedust ranges gaps        수집되지 않은 ID 대역을 찾습니다
  saucedust ranges fill        빈 대역을 수집 대상에 넣습니다
  saucedust rebuild            PostgreSQL 벡터로 Qdrant를 다시 채웁니다
  saucedust reembed            축소본으로 벡터를 다시 만듭니다 (모델을 바꿨을 때)

설치:
  saucedust setup              이 컴퓨터를 노드로 쓸 수 있게 준비합니다
  saucedust doctor             준비 상태를 확인합니다. 아무것도 바꾸지 않습니다

점검:
  saucedust verify             각 부품이 응답하는지 확인합니다
  saucedust stats              얼마나 쌓였는지 보여줍니다
  saucedust check-post <번호>   그 게시물이 수집됐는지 확인합니다

설정은 .env 또는 환경변수로 읽습니다. 항목은 internal/config/env.example를 보십시오.
`)
}

type namedTask struct {
	name string
	fn   func(context.Context) error
}

// runAll은 장기 실행 작업들을 함께 돌립니다.
// 하나가 오류로 끝나면 나머지도 정리하고 그 오류를 돌려줍니다.
func runAll(ctx context.Context, log *slog.Logger, tasks []namedTask) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg    sync.WaitGroup
		once  sync.Once
		first error
	)

	for _, task := range tasks {
		wg.Add(1)
		go func(t namedTask) {
			defer wg.Done()
			err := t.fn(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Error("작업이 오류로 끝났습니다",
					slog.String("task", t.name), slog.String("error", err.Error()))
				once.Do(func() { first = err })
			}
			cancel()
		}(task)
	}

	wg.Wait()
	log.Info("모두 정리했습니다")
	return first
}

var (
	_ app.NodeRepository       = (*postgres.Store)(nil)
	_ app.ModelRepository      = (*postgres.Store)(nil)
	_ app.LeaseRepository      = (*postgres.Store)(nil)
	_ app.ImageRepository      = (*postgres.Store)(nil)
	_ app.VectorRepository     = (*postgres.Store)(nil)
	_ app.QueryCacheRepository = (*postgres.Store)(nil)
)

var _ app.Embedder = (*embedworker.Client)(nil)

var (
	_ app.BotGateway    = (*telegram.Client)(nil)
	_ app.ImageSearcher = (*app.Search)(nil)
)
