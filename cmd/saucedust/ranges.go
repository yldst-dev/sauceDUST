package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"text/tabwriter"
	"time"

	"saucedust/internal/domain"
)

// cmdRanges는 수집 구간을 들여다보고 손보는 명령입니다.
//
// 구간은 시도 상한을 넘으면 더 이상 배정되지 않습니다. 일시적인 통신 장애로
// 상한을 소진하면 그 ID 대역의 이미지는 영영 들어오지 않습니다. 오래 돌릴수록
// 이런 구멍이 쌓이므로 확인하고 되살릴 수단이 필요합니다.
func cmdRanges(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("ranges 하위 명령이 필요합니다: status, failed, reset, gaps, fill")
	}

	rt, err := boot(ctx)
	if err != nil {
		return err
	}
	defer rt.Close()

	site, scope := rt.cfg.SourceSite, rt.cfg.ScopeKey

	switch args[0] {
	case "status":
		return rt.rangeStatus(ctx, site, scope)
	case "failed":
		return rt.rangeFailed(ctx, site, scope, args[1:])
	case "reset":
		return rt.rangeReset(ctx, site, scope, args[1:])
	case "gaps":
		return rt.rangeGaps(ctx, site, scope, args[1:])
	case "fill":
		return rt.rangeFill(ctx, site, scope, args[1:])
	default:
		return fmt.Errorf("알 수 없는 ranges 하위 명령입니다: %s", args[0])
	}
}

func (r *nodeRuntime) rangeStatus(ctx context.Context, site, scope string) error {
	stats, err := r.store.RangeStats(ctx, site, scope)
	if err != nil {
		return err
	}
	crawl, err := r.store.CrawlSummary(ctx, site, scope)
	if err != nil {
		return err
	}
	stale, err := r.store.StaleRunningRanges(ctx, site, scope, 30*time.Minute)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "전체 구간\t%d\n", stats.Total)
	fmt.Fprintf(w, "  완료\t%d\n", stats.Completed)
	fmt.Fprintf(w, "  처리 중\t%d\t%s\n", stats.Running, staleNote(stale))
	fmt.Fprintf(w, "  비어 있음\t%d\n", stats.Empty)
	fmt.Fprintf(w, "  실패\t%d\n", stats.Failed)
	fmt.Fprintf(w, "  재시도 소진\t%d\t%s\n", stats.Exhausted, exhaustedNote(stats))
	fmt.Fprintf(w, "저장된 이미지\t%d\n", stats.Saved)
	fmt.Fprintf(w, "재시도 대기\t%d\n", crawl.RetryPending)
	fmt.Fprintf(w, "최신 진행점\t%d\n", crawl.HighWatermark)
	fmt.Fprintf(w, "과거 진행점\t%d\n", crawl.BackfillBefore)
	if err := w.Flush(); err != nil {
		return err
	}

	if stats.Complete() {
		fmt.Println("\n손볼 것이 없습니다.")
	}
	return nil
}

func staleNote(stale int64) string {
	if stale == 0 {
		return ""
	}
	return fmt.Sprintf("← %d개가 30분 넘게 멈춰 있습니다", stale)
}

func exhaustedNote(stats domain.RangeStats) string {
	if stats.Exhausted == 0 {
		return ""
	}
	return fmt.Sprintf("← 약 %d개 ID가 수집되지 않습니다. ranges reset으로 되살리십시오",
		stats.MissingIDs)
}

func (r *nodeRuntime) rangeFailed(ctx context.Context, site, scope string, args []string) error {
	fs := flag.NewFlagSet("ranges failed", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "보여줄 개수")
	if err := fs.Parse(args); err != nil {
		return err
	}

	items, err := r.store.ExhaustedRanges(ctx, site, scope, *limit)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("재시도를 소진한 구간이 없습니다.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "구간\tID 대역\t개수\t시도\t마지막 오류")
	for _, item := range items {
		fmt.Fprintf(w, "%d\t%d~%d\t%d\t%d\t%s\n",
			item.ID, item.LowerID, item.UpperID, item.Size(),
			item.Attempts, truncate(item.LastError, 60))
	}
	return w.Flush()
}

func (r *nodeRuntime) rangeReset(ctx context.Context, site, scope string, args []string) error {
	fs := flag.NewFlagSet("ranges reset", flag.ContinueOnError)
	id := fs.Int64("id", 0, "특정 구간만 초기화합니다")
	retries := fs.Bool("retries", false, "죽은 재시도 항목도 함께 되살립니다")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *id > 0 {
		if err := r.store.ResetRange(ctx, *id); err != nil {
			return err
		}
		r.log.Info("구간을 되살렸습니다", slog.Int64("range", *id))
		return nil
	}

	count, err := r.store.ResetExhaustedRanges(ctx, site, scope)
	if err != nil {
		return err
	}
	r.log.Info("재시도를 소진한 구간을 되살렸습니다", slog.Int64("count", count))

	if *retries {
		n, err := r.store.ResetRetryQueue(ctx, site, scope)
		if err != nil {
			return err
		}
		r.log.Info("죽은 재시도 항목을 되살렸습니다", slog.Int64("count", n))
	}
	return nil
}

func (r *nodeRuntime) rangeGaps(ctx context.Context, site, scope string, args []string) error {
	fs := flag.NewFlagSet("ranges gaps", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "보여줄 개수")
	if err := fs.Parse(args); err != nil {
		return err
	}

	gaps, err := r.store.CoverageGaps(ctx, site, scope, *limit)
	if err != nil {
		return err
	}
	if len(gaps) == 0 {
		fmt.Println("구간 사이에 빈틈이 없습니다.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "빈 대역\t개수")
	var total int64
	for _, gap := range gaps {
		fmt.Fprintf(w, "%d~%d\t%d\n", gap.From, gap.To, gap.Size())
		total += gap.Size()
	}
	fmt.Fprintf(w, "합계\t%d\n", total)
	if err := w.Flush(); err != nil {
		return err
	}

	fmt.Println("\nranges fill로 이 대역을 수집 대상에 넣을 수 있습니다.")
	return nil
}

func (r *nodeRuntime) rangeFill(ctx context.Context, site, scope string, args []string) error {
	fs := flag.NewFlagSet("ranges fill", flag.ContinueOnError)
	limit := fs.Int("limit", 100, "한 번에 메울 빈 대역 수")
	size := fs.Int64("range-size", 0, "만들 구간 크기. 비우면 설정값을 씁니다")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *size <= 0 {
		*size = r.cfg.BackfillRangeSize
	}

	gaps, err := r.store.CoverageGaps(ctx, site, scope, *limit)
	if err != nil {
		return err
	}
	if len(gaps) == 0 {
		fmt.Println("메울 빈틈이 없습니다.")
		return nil
	}

	created, err := r.store.PlanGapRanges(ctx, site, scope, gaps, *size)
	if err != nil {
		return err
	}
	r.log.Info("빈 대역을 수집 대상에 넣었습니다",
		slog.Int("gaps", len(gaps)), slog.Int64("ranges", created))
	return nil
}
