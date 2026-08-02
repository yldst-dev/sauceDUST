package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"saucedust/internal/adapter/danbooru"
	"saucedust/internal/adapter/filestore"
	"saucedust/internal/adapter/netpath"
	"saucedust/internal/app"
	"saucedust/internal/domain"
)

func cmdMigrate(ctx context.Context) error {
	rt, err := boot(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	if err := rt.store.Migrate(ctx); err != nil {
		return err
	}
	rt.log.Info("스키마를 적용했습니다")
	return nil
}

func cmdNode(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("node 하위 명령이 필요합니다: ls, reclaim")
	}

	rt, err := boot(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	switch args[0] {
	case "ls":
		nodes, err := rt.store.ListNodes(ctx, rt.cfg.NodeTimeout)
		if err != nil {
			return err
		}
		if len(nodes) == 0 {
			fmt.Println("등록된 노드가 없습니다.")
			return nil
		}

		fleet, err := rt.store.FleetSummary(ctx, 5*time.Minute)
		if err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\t역할\t상태\t장치\t경로\t동시\t초당\t저장\t실패\t마지막 신호")
		for _, n := range nodes {
			t := fleet.Nodes[n.ID]
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%.1f\t%d\t%d\t%s 전\n",
				n.ID, n.Role, n.Status, n.Device, n.NetMode, n.Concurrency,
				t.PerSecond, t.Saved, t.Failed,
				time.Since(n.HeartbeatAt).Truncate(time.Second))
		}
		return w.Flush()

	case "reclaim":
		count, err := rt.store.ReclaimDeadNodeLeases(ctx, rt.cfg.NodeTimeout)
		if err != nil {
			return err
		}
		rt.log.Info("임대를 회수했습니다", slog.Int64("count", count))
		return nil

	default:
		return fmt.Errorf("알 수 없는 node 하위 명령입니다: %s", args[0])
	}
}

func cmdModel(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("model 하위 명령이 필요합니다: ls, add")
	}

	rt, err := boot(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	switch args[0] {
	case "ls":
		models, err := rt.store.ActiveModels(ctx)
		if err != nil {
			return err
		}
		if len(models) == 0 {
			fmt.Println("등록된 활성 모델이 없습니다.")
			return nil
		}
		summary, err := rt.store.CrawlSummary(ctx, rt.cfg.SourceSite, rt.cfg.ScopeKey)
		if err != nil {
			return err
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\t용도\t백엔드\t차원\t입력\t컬렉션\t저장된 벡터")
		for _, m := range models {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%s\t%d\n",
				m.ID, m.Kind, m.Backend, m.VectorSize, m.InputSize,
				m.Collection, summary.VectorsByModel[m.ID])
		}
		return w.Flush()

	case "add":
		fs := flag.NewFlagSet("model add", flag.ContinueOnError)
		id := fs.String("id", "", "모델 식별자")
		kind := fs.String("kind", "copy", "용도: copy(원본 찾기) 또는 semantic(비슷한 그림)")
		backend := fs.String("backend", "", "실행 백엔드 이름")
		checkpoint := fs.String("checkpoint", "", "가중치 이름")
		size := fs.Int("vector-size", 0, "벡터 차원")
		input := fs.Int("input-size", 224, "모델 입력 픽셀")
		collection := fs.String("collection", "", "Qdrant 컬렉션 이름")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *size <= 0 {
			return errors.New("-id와 -vector-size는 필수입니다")
		}
		modelKind := domain.ModelKind(*kind)
		if modelKind != domain.ModelCopy && modelKind != domain.ModelSemantic {
			return fmt.Errorf("-kind는 copy 또는 semantic이어야 합니다: %q", *kind)
		}
		if *collection == "" {
			*collection = *id
		}
		if *backend == "" {
			*backend = *id
		}

		m := domain.EmbeddingModel{
			ID: *id, Kind: modelKind, Backend: *backend, Checkpoint: *checkpoint,
			VectorSize: *size, Distance: "cosine", Collection: *collection,
			InputSize: *input, Active: true,
		}
		if err := rt.store.UpsertModel(ctx, m); err != nil {
			return err
		}
		rt.log.Info("모델을 등록했습니다",
			slog.String("id", m.ID), slog.Int("vector_size", m.VectorSize))
		return nil

	default:
		return fmt.Errorf("알 수 없는 model 하위 명령입니다: %s", args[0])
	}
}

// cmdProbe는 호스트별로 어떤 네트워크 경로가 통하는지, 어느 쪽이 빠른지 잽니다.
// 결과는 저장되어 다음 실행에서 가장 빠른 경로부터 시도하는 데 쓰입니다.
func cmdProbe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	samples := fs.Int("samples", 3, "경로마다 재는 횟수. 중앙값을 씁니다")
	parallel := fs.Int("parallel", 0, "이 값까지 동시 다운로드를 올려 노드 상한을 잽니다")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rt, err := boot(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	chain, err := rt.newNetChain()
	if err != nil {
		return err
	}

	targets := fs.Args()
	if len(targets) == 0 {
		targets = rt.defaultProbeTargets(ctx, chain)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "호스트\t경로\t결과\t걸린 시간\t받은 크기\t속도\tHTTP\t비고")

	best := map[string]netpath.ProbeResult{}
	for _, target := range targets {
		results, err := chain.Probe(ctx, target, *samples)
		if err != nil {
			return err
		}
		for _, r := range results {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Host, r.Mode, r.Verdict(), millis(r.Latency), bytesOf(r.Bytes),
				speed(r), dash(r.Proto), truncate(r.Detail, 44))

			if !r.OK {
				continue
			}
			if current, ok := best[r.Host]; !ok || r.Latency < current.Latency {
				best[r.Host] = r
			}
		}
		fmt.Fprintln(w, "\t\t\t\t\t\t\t")
	}
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Println("호스트별 권장 경로")
	for _, host := range sortedKeys(best) {
		r := best[host]
		fmt.Printf("  %-22s %-6s %s %s\n", host, r.Mode, millis(r.Latency), speed(r))
	}
	if len(best) == 0 {
		fmt.Println("  통하는 경로를 찾지 못했습니다. VPN 프록시를 설정하십시오.")
		return nil
	}

	if *parallel > 0 {
		return rt.measureDownloadCeiling(ctx, chain, *parallel)
	}
	return nil
}

// measureDownloadCeiling은 이 노드가 실제로 초당 몇 장을 내려받을 수 있는지 잽니다.
// 연결 하나의 속도만 봐서는 알 수 없습니다. 동시에 여러 개를 받아야 회선이
// 포화되는 지점이 보입니다.
func (r *nodeRuntime) measureDownloadCeiling(ctx context.Context, chain *netpath.Chain, maxParallel int) error {
	source, err := danbooru.New(chain, danbooru.Options{
		BaseURL: r.cfg.DanbooruAPI, UserAgent: r.cfg.UserAgent,
		RatePerSecond: r.cfg.RatePerSecond, Burst: r.cfg.RateBurst,
		Login: r.cfg.DanbooruLogin, APIKey: r.cfg.DanbooruAPIKey,
	})
	if err != nil {
		return err
	}

	posts, err := source.PostsAfter(ctx, 0, r.cfg.IndexTags, 200)
	if err != nil {
		return fmt.Errorf("측정할 게시물을 받지 못했습니다: %w", err)
	}

	urls := make([]string, 0, len(posts))
	for _, post := range posts {
		if url := post.DownloadURL(); url != "" {
			urls = append(urls, url)
		}
	}
	if len(urls) < 8 {
		return fmt.Errorf("측정할 이미지가 %d장뿐입니다", len(urls))
	}

	fmt.Printf("\n다운로드 상한 측정 (이미지 %d장)\n", len(urls))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "동시\t성공\t실패\t초당 장수\t속도\t평균 크기\t실패 사유")

	for _, concurrency := range parallelSteps(maxParallel) {
		count := concurrency * 4
		if count > len(urls) {
			count = len(urls)
		}

		var (
			wg      sync.WaitGroup
			slots   = make(chan struct{}, concurrency)
			total   atomic.Int64
			mu      sync.Mutex
			reasons = map[string]int{}
			started = time.Now()
		)
		for _, url := range urls[:count] {
			wg.Add(1)
			slots <- struct{}{}
			go func(url string) {
				defer wg.Done()
				defer func() { <-slots }()

				data, err := source.Download(ctx, url, 32<<20)
				if err != nil {
					mu.Lock()
					reasons[failureKind(err)]++
					mu.Unlock()
					return
				}
				total.Add(int64(len(data)))
			}(url)
		}
		wg.Wait()

		elapsed := time.Since(started).Seconds()
		var failed int
		for _, n := range reasons {
			failed += n
		}
		ok := count - failed
		bytes := float64(total.Load())

		fmt.Fprintf(w, "%d\t%d\t%d\t%.1f장\t%.1fMB/s\t%s\t%s\n",
			concurrency, ok, failed, float64(ok)/elapsed,
			bytes/elapsed/(1024*1024),
			bytesOf(int64(bytes/float64(max64(int64(ok), 1)))),
			describeReasons(reasons))

		// 실패가 나면 회선이나 상대 서버가 이미 한계입니다. 더 올리지 않습니다.
		if failed > 0 {
			fmt.Fprintln(w, "\t\t\t\t\t\t실패가 나와 여기서 멈춥니다")
			break
		}
	}
	return w.Flush()
}

// failureKind는 실패를 굵게 분류합니다. 속도 제한인지 연결이 끊긴 것인지에 따라
// 대응이 다릅니다. 앞의 것은 동시 수를 줄이면 되고, 뒤의 것은 차단일 수 있습니다.
func failureKind(err error) string {
	var status *danbooru.StatusError
	if errors.As(err, &status) {
		return fmt.Sprintf("HTTP %d", status.Code)
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "context deadline exceeded"), strings.Contains(msg, "timeout"):
		return "시간 초과"
	case strings.Contains(msg, "connection reset"):
		return "연결 끊김"
	case strings.Contains(msg, "no such host"):
		return "이름 조회 실패"
	default:
		return "기타"
	}
}

func describeReasons(reasons map[string]int) string {
	if len(reasons) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(reasons))
	for _, kind := range sortedKeys(reasons) {
		parts = append(parts, fmt.Sprintf("%s %d", kind, reasons[kind]))
	}
	return strings.Join(parts, ", ")
}

func parallelSteps(limit int) []int {
	var out []int
	for _, step := range []int{1, 4, 8, 16, 32, 64} {
		if step > limit {
			break
		}
		out = append(out, step)
	}
	if len(out) == 0 {
		return []int{limit}
	}
	return out
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// defaultProbeTargets는 API 한 곳과 실제 이미지 한 장을 고릅니다.
// CDN 루트를 재면 항상 403이 나와 경로가 뚫렸는지와 무관한 값이 됩니다.
func (r *nodeRuntime) defaultProbeTargets(ctx context.Context, chain *netpath.Chain) []string {
	targets := []string{r.cfg.DanbooruAPI + "/posts.json?limit=1"}

	source, err := danbooru.New(chain, danbooru.Options{
		BaseURL: r.cfg.DanbooruAPI, UserAgent: r.cfg.UserAgent,
		RatePerSecond: r.cfg.RatePerSecond, Burst: r.cfg.RateBurst,
		Login: r.cfg.DanbooruLogin, APIKey: r.cfg.DanbooruAPIKey,
	})
	if err != nil {
		return targets
	}

	lookupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	posts, err := source.PostsAfter(lookupCtx, 0, r.cfg.IndexTags, 20)
	if err != nil {
		fmt.Printf("실제 이미지 주소를 찾지 못해 API만 측정합니다: %v\n\n", err)
		return targets
	}
	for _, post := range posts {
		if url := post.DownloadURL(); url != "" {
			return append(targets, url)
		}
	}
	return targets
}

func millis(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

func bytesOf(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.0fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}

func speed(r netpath.ProbeResult) string {
	if v := r.Throughput(); v > 0 {
		return fmt.Sprintf("%.1fMB/s", v)
	}
	return "-"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cmdRebuild는 PostgreSQL에 남은 벡터로 Qdrant를 다시 채웁니다.
// Qdrant 저장소를 잃어버려도 다시 크롤링할 필요가 없습니다.
func cmdRebuild(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rebuild", flag.ContinueOnError)
	batch := fs.Int("batch", 512, "한 번에 처리할 벡터 수")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rt, err := boot(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	models, err := rt.activeModels(ctx, nil)
	if err != nil {
		return err
	}
	ingest, err := rt.newIngest(ctx, models)
	if err != nil {
		return err
	}

	started := time.Now()
	total, err := ingest.RebuildIndex(ctx, *batch)
	if err != nil {
		return err
	}
	rt.log.Info("색인을 다시 채웠습니다",
		slog.Int("vectors", total), slog.Duration("took", time.Since(started)))
	return nil
}

// cmdReembed는 보관해 둔 축소본으로 벡터를 다시 만듭니다.
//
// 모델을 바꾸면 쌓인 벡터가 전부 쓸모없어집니다. 원본은 저장하지 않지만
// 축소본은 남겨 두므로 Danbooru를 다시 훑지 않아도 됩니다. 1,190만 장을
// 다시 내려받으면 초당 5회 제한에서 28일이지만, 축소본에서 다시 계산하면
// 네트워크를 쓰지 않아 GPU 속도만큼 빠릅니다.
//
// rebuild와 다릅니다. rebuild는 PostgreSQL에 이미 있는 벡터를 Qdrant로
// 옮기는 것이고, 이쪽은 벡터 자체를 새 모델로 다시 만드는 것입니다.
func cmdReembed(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reembed", flag.ContinueOnError)
	modelID := fs.String("model", "", "다시 계산할 모델 이름. 비우면 활성 모델 전부")
	batch := fs.Int("batch", 256, "한 번에 가져올 이미지 수")
	workers := fs.Int("workers", 8, "워커에 동시에 보낼 요청 수")
	dryRun := fs.Bool("dry-run", false, "할 일만 세어 보고 실제로 하지 않습니다")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rt, err := boot(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	embedder, err := rt.newEmbedder()
	if err != nil {
		return err
	}
	models, err := rt.activeModels(ctx, embedder)
	if err != nil {
		return err
	}
	if *modelID != "" {
		models = filterModels(models, *modelID)
		if len(models) == 0 {
			return fmt.Errorf("활성 모델 중에 %q가 없습니다", *modelID)
		}
	}

	if *dryRun {
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "모델\t다시 계산할 건수")
		for _, m := range models {
			n, err := rt.store.CountThumbsMissingVector(ctx, m.ID)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "%s\t%s\n", m.ID, comma(n))
		}
		return w.Flush()
	}

	if err := checkReembedRoom(ctx, rt, models); err != nil {
		return err
	}

	index, err := rt.newQdrant()
	if err != nil {
		return err
	}
	thumbs, err := filestore.NewThumbStore(rt.cfg.ThumbDir)
	if err != nil {
		return err
	}

	reembed, err := app.NewReembed(app.ReembedDeps{
		Images: rt.store, Vector: rt.store, Index: index, Thumbs: thumbs,
		Embedder: embedder, Log: rt.log, Workers: *workers, BatchSize: *batch,
	})
	if err != nil {
		return err
	}

	for _, m := range models {
		result, err := reembed.Run(ctx, m)
		if err != nil {
			return err
		}
		if result.Missing > 0 {
			rt.log.Warn("축소본이 없어 건너뛴 것이 있습니다. 이것은 다시 내려받아야 합니다",
				slog.String("model", m.ID), slog.Int("건수", result.Missing))
		}
	}
	return nil
}

func filterModels(models []domain.EmbeddingModel, id string) []domain.EmbeddingModel {
	var out []domain.EmbeddingModel
	for _, m := range models {
		if m.ID == id {
			out = append(out, m)
		}
	}
	return out
}

// truncate는 표 한 칸에 들어가도록 문자열을 줄입니다.
//
// n은 바이트 수입니다. 바이트로 그냥 자르면 한글 한 글자가 중간에서
// 끊겨 터미널에 깨진 문자가 찍힙니다. 이 프로그램의 오류 메시지는 전부
// 한국어라 늘 일어나는 일입니다. 글자 경계까지만 담습니다.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	end := 0
	for i := range s {
		if i > n {
			break
		}
		end = i
	}
	return s[:end] + "…"
}
