package postgres

import (
	"context"
	"testing"
	"time"

	"saucedust/internal/domain"
)

func seedRange(t *testing.T, s *Store, lower, upper int64, status domain.RangeStatus, attempts int) int64 {
	t.Helper()

	var id int64
	err := s.pool.QueryRow(context.Background(), `
INSERT INTO crawl_ranges (source_site, scope_key, direction, lower_id, upper_id,
                          status, attempts, leased_at)
VALUES ('danbooru', 'default', 'backfill', $1, $2, $3, $4, now())
RETURNING id`, lower, upper, string(status), attempts).Scan(&id)
	if err != nil {
		t.Fatalf("구간 준비 실패: %v", err)
	}
	return id
}

func clearRanges(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.pool.Exec(context.Background(),
		`TRUNCATE crawl_ranges, crawl_post_retries RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("초기화 실패: %v", err)
	}
}

func TestRangeStatsCountsByStatus(t *testing.T) {
	store := testStore(t)
	clearRanges(t, store)
	ctx := context.Background()

	seedRange(t, store, 1, 10, domain.RangeCompleted, 1)
	seedRange(t, store, 11, 20, domain.RangeCompleted, 1)
	seedRange(t, store, 21, 30, domain.RangeRunning, 1)
	seedRange(t, store, 31, 40, domain.RangeEmpty, 1)
	seedRange(t, store, 41, 50, domain.RangeFailed, 2)
	seedRange(t, store, 51, 60, domain.RangeFailed, maxAttempts)

	stats, err := store.RangeStats(ctx, "danbooru", "default")
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	switch {
	case stats.Total != 6:
		t.Fatalf("전체가 %d입니다", stats.Total)
	case stats.Completed != 2:
		t.Fatalf("완료가 %d입니다", stats.Completed)
	case stats.Running != 1:
		t.Fatalf("처리 중이 %d입니다", stats.Running)
	case stats.Empty != 1:
		t.Fatalf("비어 있음이 %d입니다", stats.Empty)
	case stats.Failed != 2:
		t.Fatalf("실패가 %d입니다", stats.Failed)
	case stats.Exhausted != 1:
		t.Fatalf("소진이 %d입니다. 시도 상한을 넘은 것만 세야 합니다", stats.Exhausted)
	case stats.MissingIDs != 20:
		t.Fatalf("빠진 ID가 %d개입니다. 실패 구간 두 개의 20개를 기대했습니다", stats.MissingIDs)
	}
}

func TestRangeStatsCompleteFlag(t *testing.T) {
	store := testStore(t)
	clearRanges(t, store)
	ctx := context.Background()

	seedRange(t, store, 1, 10, domain.RangeCompleted, 1)
	stats, _ := store.RangeStats(ctx, "danbooru", "default")
	if !stats.Complete() {
		t.Fatal("완료만 있으면 끝난 것으로 봐야 합니다")
	}

	seedRange(t, store, 11, 20, domain.RangeFailed, maxAttempts)
	stats, _ = store.RangeStats(ctx, "danbooru", "default")
	if stats.Complete() {
		t.Fatal("실패가 남아 있으면 끝난 것이 아닙니다")
	}
}

// 시도 상한을 넘긴 구간만 목록에 나와야 합니다. 아직 여지가 있는 것은 제외합니다.
func TestExhaustedRangesOnlyListsUnrecoverable(t *testing.T) {
	store := testStore(t)
	clearRanges(t, store)
	ctx := context.Background()

	seedRange(t, store, 1, 10, domain.RangeFailed, 1)
	dead := seedRange(t, store, 11, 20, domain.RangeFailed, maxAttempts)
	seedRange(t, store, 21, 30, domain.RangeCompleted, maxAttempts)

	items, err := store.ExhaustedRanges(ctx, "danbooru", "default", 10)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("%d건이 나왔습니다. 1건이어야 합니다", len(items))
	}
	if items[0].ID != dead {
		t.Fatalf("구간 %d가 나왔습니다. %d를 기대했습니다", items[0].ID, dead)
	}
	if items[0].Size() != 10 {
		t.Fatalf("크기가 %d입니다", items[0].Size())
	}
}

// 되살린 구간은 다시 배정돼야 합니다. 이것이 안 되면 데이터가 영영 빕니다.
func TestResetExhaustedRangesMakesThemLeasableAgain(t *testing.T) {
	store := testStore(t)
	clearRanges(t, store)
	ctx := context.Background()
	registerNode(t, store, "reviver")

	seedRange(t, store, 100, 109, domain.RangeFailed, maxAttempts)

	// 되살리기 전에는 배정되지 않아야 합니다.
	if _, err := store.reuseRange(ctx, domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "reviver", RangeSize: 10,
	}); err == nil {
		t.Fatal("소진된 구간이 배정되었습니다")
	}

	count, err := store.ResetExhaustedRanges(ctx, "danbooru", "default")
	if err != nil {
		t.Fatalf("되살리기 실패: %v", err)
	}
	if count != 1 {
		t.Fatalf("%d건을 되살렸습니다", count)
	}

	got, err := store.reuseRange(ctx, domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "reviver", RangeSize: 10,
	})
	if err != nil {
		t.Fatalf("되살린 구간이 배정되지 않습니다: %v", err)
	}
	if got.LowerID != 100 || got.UpperID != 109 {
		t.Fatalf("배정된 구간이 %d~%d입니다", got.LowerID, got.UpperID)
	}
}

func TestResetSingleRange(t *testing.T) {
	store := testStore(t)
	clearRanges(t, store)
	ctx := context.Background()

	id := seedRange(t, store, 1, 10, domain.RangeCompleted, 3)
	if err := store.ResetRange(ctx, id); err != nil {
		t.Fatalf("초기화 실패: %v", err)
	}

	items, _ := store.ExhaustedRanges(ctx, "danbooru", "default", 10)
	if len(items) != 0 {
		t.Fatal("시도 횟수가 0으로 돌아가야 합니다")
	}

	stats, _ := store.RangeStats(ctx, "danbooru", "default")
	if stats.Failed != 1 {
		t.Fatalf("실패가 %d입니다. 다시 배정되도록 실패로 두어야 합니다", stats.Failed)
	}

	if err := store.ResetRange(ctx, 999999); err == nil {
		t.Fatal("없는 구간은 오류여야 합니다")
	}
}

// 구간 사이에 빠진 ID 대역을 찾아야 합니다.
func TestCoverageGapsFindsHoles(t *testing.T) {
	store := testStore(t)
	clearRanges(t, store)
	ctx := context.Background()

	// 1~100, 200~300만 있고 101~199가 비어 있습니다.
	seedRange(t, store, 1, 100, domain.RangeCompleted, 1)
	seedRange(t, store, 200, 300, domain.RangeCompleted, 1)

	gaps, err := store.CoverageGaps(ctx, "danbooru", "default", 10)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if len(gaps) != 1 {
		t.Fatalf("빈틈이 %d개입니다: %+v", len(gaps), gaps)
	}
	if gaps[0].From != 101 || gaps[0].To != 199 {
		t.Fatalf("빈틈이 %d~%d입니다. 101~199를 기대했습니다", gaps[0].From, gaps[0].To)
	}
	if gaps[0].Size() != 99 {
		t.Fatalf("크기가 %d입니다", gaps[0].Size())
	}
}

func TestCoverageGapsEmptyWhenContiguous(t *testing.T) {
	store := testStore(t)
	clearRanges(t, store)
	ctx := context.Background()

	seedRange(t, store, 1, 100, domain.RangeCompleted, 1)
	seedRange(t, store, 101, 200, domain.RangeCompleted, 1)
	seedRange(t, store, 201, 300, domain.RangeCompleted, 1)

	gaps, err := store.CoverageGaps(ctx, "danbooru", "default", 10)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if len(gaps) != 0 {
		t.Fatalf("이어져 있는데 빈틈 %d개가 나왔습니다: %+v", len(gaps), gaps)
	}
}

// 빈 대역을 배정 가능한 구간으로 만들어야 합니다.
func TestPlanGapRangesCreatesLeasableRanges(t *testing.T) {
	store := testStore(t)
	clearRanges(t, store)
	ctx := context.Background()
	registerNode(t, store, "filler")

	gaps := []domain.IDGap{{From: 1, To: 25}}
	created, err := store.PlanGapRanges(ctx, "danbooru", "default", gaps, 10)
	if err != nil {
		t.Fatalf("생성 실패: %v", err)
	}
	// 25개를 10씩 나누면 3개입니다.
	if created != 3 {
		t.Fatalf("%d개를 만들었습니다. 3개를 기대했습니다", created)
	}

	got, err := store.reuseRange(ctx, domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "filler", RangeSize: 10,
	})
	if err != nil {
		t.Fatalf("만든 구간이 배정되지 않습니다: %v", err)
	}
	if got.LowerID < 1 || got.UpperID > 25 {
		t.Fatalf("배정된 구간이 %d~%d입니다", got.LowerID, got.UpperID)
	}
}

// 같은 대역을 두 번 메워도 중복이 생기면 안 됩니다.
func TestPlanGapRangesIsIdempotent(t *testing.T) {
	store := testStore(t)
	clearRanges(t, store)
	ctx := context.Background()

	gaps := []domain.IDGap{{From: 1, To: 20}}
	if _, err := store.PlanGapRanges(ctx, "danbooru", "default", gaps, 10); err != nil {
		t.Fatalf("첫 생성 실패: %v", err)
	}
	created, err := store.PlanGapRanges(ctx, "danbooru", "default", gaps, 10)
	if err != nil {
		t.Fatalf("두 번째 생성 실패: %v", err)
	}
	if created != 0 {
		t.Fatalf("%d개가 중복 생성됐습니다", created)
	}

	stats, _ := store.RangeStats(ctx, "danbooru", "default")
	if stats.Total != 2 {
		t.Fatalf("구간이 %d개입니다. 2개여야 합니다", stats.Total)
	}
}

func TestStaleRunningRanges(t *testing.T) {
	store := testStore(t)
	clearRanges(t, store)
	ctx := context.Background()

	seedRange(t, store, 1, 10, domain.RangeRunning, 1)

	fresh, err := store.StaleRunningRanges(ctx, "danbooru", "default", time.Hour)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if fresh != 0 {
		t.Fatalf("방금 만든 구간이 %d개 멈춘 것으로 잡혔습니다", fresh)
	}

	stale, err := store.StaleRunningRanges(ctx, "danbooru", "default", time.Nanosecond)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if stale != 1 {
		t.Fatalf("멈춘 구간이 %d개입니다. 1개여야 합니다", stale)
	}
}

func TestResetRetryQueueRevivesDeadItems(t *testing.T) {
	store := testStore(t)
	clearRanges(t, store)
	ctx := context.Background()
	registerNode(t, store, "retrier")

	if err := store.EnqueueRetries(ctx, "danbooru", "default", []int64{1, 2, 3}, 0); err != nil {
		t.Fatalf("등록 실패: %v", err)
	}
	if _, err := store.pool.Exec(ctx,
		`UPDATE crawl_post_retries SET status = 'dead', attempts = $1`, maxAttempts); err != nil {
		t.Fatalf("상태 변경 실패: %v", err)
	}

	// 죽은 항목은 임대되지 않아야 합니다.
	items, _ := store.LeaseRetries(ctx, "danbooru", "default", "retrier", 10)
	if len(items) != 0 {
		t.Fatalf("죽은 항목 %d건이 임대되었습니다", len(items))
	}

	count, err := store.ResetRetryQueue(ctx, "danbooru", "default")
	if err != nil {
		t.Fatalf("되살리기 실패: %v", err)
	}
	if count != 3 {
		t.Fatalf("%d건을 되살렸습니다", count)
	}

	items, _ = store.LeaseRetries(ctx, "danbooru", "default", "retrier", 10)
	if len(items) != 3 {
		t.Fatalf("되살린 뒤 %d건만 임대됩니다", len(items))
	}
}
