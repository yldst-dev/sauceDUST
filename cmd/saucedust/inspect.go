package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"saucedust/internal/domain"
)

// cmdVerify는 각 부품이 실제로 응답하는지 확인합니다.
// 새 노드를 놓았을 때 무엇이 빠졌는지 한 번에 알려줍니다.
func cmdVerify(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	rt, err := boot(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "항목\t결과\t비고")

	var failed int
	check := func(name string, fn func() (string, error)) {
		note, err := fn()
		if err != nil {
			failed++
			fmt.Fprintf(w, "%s\t실패\t%s\n", name, truncate(err.Error(), 70))
			return
		}
		fmt.Fprintf(w, "%s\t정상\t%s\n", name, note)
	}

	check("PostgreSQL", func() (string, error) {
		if err := rt.store.Ping(ctx); err != nil {
			return "", err
		}
		n, err := rt.store.CountImages(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("이미지 %s건", comma(n)), nil
	})

	models, modelErr := rt.store.ActiveModels(ctx)
	check("등록된 모델", func() (string, error) {
		if modelErr != nil {
			return "", modelErr
		}
		if len(models) == 0 {
			return "", errors.New("활성 모델이 없습니다. model add로 등록하십시오")
		}
		names := make([]string, 0, len(models))
		for _, m := range models {
			names = append(names, fmt.Sprintf("%s(%s, %d차원)", m.ID, m.Kind, m.VectorSize))
		}
		return joinComma(names), nil
	})

	check("벡터 색인", func() (string, error) {
		index, err := rt.newIndex(true)
		if err != nil {
			return "", err
		}
		if err := index.Ping(ctx); err != nil {
			return "", err
		}
		if len(models) == 0 {
			return "연결됨", nil
		}
		parts := make([]string, 0, len(models))
		for _, m := range models {
			n, err := index.Count(ctx, m.Collection)
			if err != nil {
				parts = append(parts, fmt.Sprintf("%s: 컬렉션 없음", m.Collection))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s %s점", m.Collection, comma(n)))
		}
		return joinComma(parts), nil
	})

	check("임베딩 워커", func() (string, error) {
		embedder, err := rt.newEmbedder()
		if err != nil {
			return "", err
		}
		info, err := embedder.Describe(ctx)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("장치 %s, 모델 %d개", info.Device, len(info.Models)), nil
	})

	check("수집 대상", func() (string, error) {
		chain, err := rt.newNetChain()
		if err != nil {
			return "", err
		}
		source, err := rt.newSource(chain)
		if err != nil {
			return "", err
		}
		latest, err := source.LatestPostID(ctx)
		if err != nil {
			return "", err
		}
		auth := "익명"
		if source.Authenticated() {
			auth = "인증됨"
		}
		return fmt.Sprintf("최신 게시물 %s, %s, 경로 %s",
			comma(latest), auth, chain.ModeFor("danbooru.donmai.us")), nil
	})

	if err := w.Flush(); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d개 항목이 실패했습니다", failed)
	}
	fmt.Println("\n모두 정상입니다.")
	return nil
}

// cmdStats는 저장소가 실제로 얼마나 쌓였는지 보여줍니다.
func cmdStats(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	rt, err := boot(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	images, err := rt.store.CountImages(ctx)
	if err != nil {
		return err
	}
	crawl, err := rt.store.CrawlSummary(ctx, rt.cfg.SourceSite, rt.cfg.ScopeKey)
	if err != nil {
		return err
	}
	ranges, err := rt.store.RangeStats(ctx, rt.cfg.SourceSite, rt.cfg.ScopeKey)
	if err != nil {
		return err
	}
	nodes, err := rt.store.ListNodes(ctx, rt.cfg.NodeTimeout)
	if err != nil {
		return err
	}
	fleet, err := rt.store.FleetSummary(ctx, 5*time.Minute)
	if err != nil {
		return err
	}

	var online int
	for _, n := range nodes {
		if n.Status == "online" {
			online++
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "수집한 이미지\t%s\n", comma(images))
	for model, count := range crawl.VectorsByModel {
		fmt.Fprintf(w, "  %s 벡터\t%s\n", model, comma(count))
	}
	fmt.Fprintf(w, "구간 완료\t%s\n", comma(ranges.Completed))
	fmt.Fprintf(w, "구간 실패\t%s\n", comma(ranges.Failed))
	fmt.Fprintf(w, "재시도 대기\t%s\n", comma(crawl.RetryPending))
	fmt.Fprintf(w, "최신 진행점\t%s\n", comma(crawl.HighWatermark))
	fmt.Fprintf(w, "과거 진행점\t%s\n", comma(crawl.BackfillBefore))
	fmt.Fprintf(w, "살아 있는 노드\t%d / %d\n", online, len(nodes))
	fmt.Fprintf(w, "전체 처리량\t%.1f장/초\n", fleet.TotalPerSecond)

	if crawl.HighWatermark > 0 {
		done := crawl.HighWatermark - crawl.BackfillBefore
		pct := float64(done) / float64(crawl.HighWatermark) * 100
		fmt.Fprintf(w, "훑은 비율\t%.2f%%\n", pct)
	}
	return w.Flush()
}

// cmdCheckPost는 특정 게시물이 실제로 들어와 있는지 확인합니다.
// 검색 결과가 예상과 다를 때 그 게시물이 애초에 수집됐는지 가립니다.
func cmdCheckPost(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("check-post", flag.ContinueOnError)
	site := fs.String("site", "", "출처 사이트. 비우면 설정값을 씁니다")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("게시물 번호를 하나 지정하십시오")
	}

	postID, err := strconv.ParseInt(fs.Arg(0), 10, 64)
	if err != nil {
		return fmt.Errorf("게시물 번호가 숫자가 아닙니다: %q", fs.Arg(0))
	}

	rt, err := boot(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	if *site == "" {
		*site = rt.cfg.SourceSite
	}

	img, err := rt.store.ImageBySource(ctx, *site, postID)
	if errors.Is(err, domain.ErrNotFound) {
		fmt.Printf("게시물 %d는 아직 수집되지 않았습니다.\n", postID)
		fmt.Println("검색해도 나오지 않습니다. 비슷한 다른 그림이 대신 나옵니다.")
		return rt.explainWhyMissing(ctx, postID)
	}
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "내부 번호\t%d\n", img.ID)
	fmt.Fprintf(w, "출처\t%s #%d\n", img.SourceSite, img.SourcePostID)
	fmt.Fprintf(w, "크기\t%dx%d\n", img.Width, img.Height)
	fmt.Fprintf(w, "등급\t%s\n", domain.RatingLabel(img.Rating))
	fmt.Fprintf(w, "pHash\t%s\n", img.PHash)
	fmt.Fprintf(w, "dHash\t%s\n", img.DHash)
	fmt.Fprintf(w, "태그\t%d개\n", len(img.Tags))
	fmt.Fprintf(w, "축소본\t%s\n", yesNo(img.ThumbPath != ""))
	fmt.Fprintf(w, "처리 노드\t%s\n", img.IndexedBy)
	fmt.Fprintf(w, "수집 시각\t%s\n", img.CreatedAt.Format(time.RFC3339))
	if err := w.Flush(); err != nil {
		return err
	}

	models, err := rt.store.ActiveModels(ctx)
	if err != nil {
		return err
	}
	for _, m := range models {
		missing, err := rt.store.VectorsMissing(ctx, []int64{img.ID}, m.ID)
		if err != nil {
			return err
		}
		fmt.Printf("모델 %s 벡터: %s\n", m.ID, yesNo(len(missing) == 0))
	}
	return nil
}

// explainWhyMissing은 그 번호가 어느 구간에 속하고 그 구간이 어떤 상태인지 알려줍니다.
func (r *nodeRuntime) explainWhyMissing(ctx context.Context, postID int64) error {
	stats, err := r.store.RangeStats(ctx, r.cfg.SourceSite, r.cfg.ScopeKey)
	if err != nil {
		return err
	}
	crawl, err := r.store.CrawlSummary(ctx, r.cfg.SourceSite, r.cfg.ScopeKey)
	if err != nil {
		return err
	}

	switch {
	case postID > crawl.HighWatermark:
		fmt.Println("\n최신 진행점보다 새 게시물입니다. 곧 수집됩니다.")
	case postID < crawl.BackfillBefore:
		fmt.Println("\n아직 과거 수집이 여기까지 내려오지 않았습니다.")
	case stats.Exhausted > 0:
		fmt.Printf("\n재시도를 소진한 구간이 %d개 있습니다. "+
			"ranges failed로 확인하고 ranges reset으로 되살리십시오.\n", stats.Exhausted)
	default:
		fmt.Println("\n삭제되었거나 지원하지 않는 형식일 수 있습니다.")
	}
	return nil
}

func yesNo(v bool) string {
	if v {
		return "있음"
	}
	return "없음"
}

// comma는 큰 숫자에 자릿점을 찍습니다.
func comma(v int64) string {
	s := strconv.FormatInt(v, 10)

	// 부호는 자릿수 세기에서 빼 둡니다. 같이 세면 -123이 -,123이 됩니다.
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	if len(s) <= 3 {
		return sign + s
	}

	var out []byte
	for i, digit := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, digit)
	}
	return sign + string(out)
}

func joinComma(parts []string) string {
	out := ""
	for i, part := range parts {
		if i > 0 {
			out += ", "
		}
		out += part
	}
	return out
}
