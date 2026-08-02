package postgres

import (
	"context"
	"errors"
	"testing"

	"saucedust/internal/domain"
)

// 하한을 두면 그 아래 번호는 아예 잘라 내지 않아야 합니다.
//
// 메모리가 넉넉하지 않은 곳에서 쓰는 장치입니다. Qdrant가 붙들고 있어야
// 하는 양이 장수에 정비례해서, 다 모으고 나서는 줄일 수가 없습니다.
func TestBackfillStopsAtTheFloor(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 10_000); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}
	registerNode(t, store, "alpha")

	const floor = 9_500
	lowest := int64(1 << 62)

	for i := 0; i < 50; i++ {
		r, err := store.AcquireBackfillRange(ctx, domain.LeaseRequest{
			SourceSite: "danbooru", ScopeKey: "default", NodeID: "alpha",
			RangeSize: 100, FloorID: floor,
		})
		if errors.Is(err, domain.ErrNoWork) {
			break
		}
		if err != nil {
			t.Fatalf("임대 실패: %v", err)
		}
		if r.LowerID < floor {
			t.Fatalf("구간이 %d까지 내려갔습니다. 하한은 %d입니다", r.LowerID, floor)
		}
		if r.LowerID < lowest {
			lowest = r.LowerID
		}
		if err := store.FinishRange(ctx, r, domain.RangeCompleted, 0, nil); err != nil {
			t.Fatalf("완료 보고 실패: %v", err)
		}
	}

	if lowest != floor {
		t.Errorf("가장 낮은 구간이 %d에서 멈췄습니다. 하한 %d까지는 가야 합니다", lowest, floor)
	}

	// 하한에 닿았으면 더 줄 것이 없어야 합니다.
	_, err := store.AcquireBackfillRange(ctx, domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "alpha",
		RangeSize: 100, FloorID: floor,
	})
	if !errors.Is(err, domain.ErrNoWork) {
		t.Errorf("하한 아래로 더 주려 합니다: %v", err)
	}
}

// 하한을 치웠으면 같은 상태에서 다시 내려갈 수 있어야 합니다.
// 하한은 이미 받은 구간을 지우는 것이 아니라 새로 자르는 것만 막습니다.
func TestFloorCanBeLoweredLater(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 10_000); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}
	registerNode(t, store, "alpha")

	req := domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "alpha",
		RangeSize: 100, FloorID: 9_900,
	}
	for {
		r, err := store.AcquireBackfillRange(ctx, req)
		if errors.Is(err, domain.ErrNoWork) {
			break
		}
		if err != nil {
			t.Fatalf("임대 실패: %v", err)
		}
		if err := store.FinishRange(ctx, r, domain.RangeCompleted, 0, nil); err != nil {
			t.Fatalf("완료 보고 실패: %v", err)
		}
	}

	req.FloorID = 9_000
	r, err := store.AcquireBackfillRange(ctx, req)
	if err != nil {
		t.Fatalf("하한을 낮췄는데 일을 주지 않습니다: %v", err)
	}
	if r.UpperID >= 9_900 {
		t.Errorf("이미 끝낸 구간을 다시 줬습니다: %d~%d", r.LowerID, r.UpperID)
	}
}

// 하한을 올리기 전에 실패한 구간이 계속 다시 나오면 안 됩니다.
//
// 임대는 새 구간을 자르기 전에 실패한 구간부터 다시 씁니다. 그쪽 질의에
// 하한이 없으면 하한을 올려도 옛 실패 구간으로 계속 과거를 긁습니다.
func TestFloorAlsoBlocksFailedRanges(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 10_000); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}
	registerNode(t, store, "alpha")

	noFloor := domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "alpha", RangeSize: 100,
	}

	// 하한 없이 과거로 내려갑니다. 실패시키면 바로 재사용되므로 완료로
	// 보고하며 걸어 내려간 뒤, 마지막 하나만 실패로 남깁니다.
	for i := 0; i < 3; i++ {
		r, err := store.AcquireBackfillRange(ctx, noFloor)
		if err != nil {
			t.Fatalf("임대 실패: %v", err)
		}
		if err := store.FinishRange(ctx, r, domain.RangeCompleted, 0, nil); err != nil {
			t.Fatalf("완료 보고 실패: %v", err)
		}
	}

	deep, err := store.AcquireBackfillRange(ctx, noFloor)
	if err != nil {
		t.Fatalf("임대 실패: %v", err)
	}
	if err := store.FinishRange(ctx, deep, domain.RangeFailed, 0, errTestFailure); err != nil {
		t.Fatalf("실패 보고 실패: %v", err)
	}

	floor := deep.UpperID + 1
	withFloor := noFloor
	withFloor.FloorID = floor

	// 하한을 실패 구간 위로 올렸으니 그 구간은 다시 나오면 안 됩니다.
	for i := 0; i < 20; i++ {
		r, err := store.AcquireBackfillRange(ctx, withFloor)
		if errors.Is(err, domain.ErrNoWork) {
			return
		}
		if err != nil {
			t.Fatalf("임대 실패: %v", err)
		}
		if r.LowerID < floor {
			t.Fatalf("하한을 %d로 올렸는데 %d~%d 구간을 줬습니다",
				floor, r.LowerID, r.UpperID)
		}
		if err := store.FinishRange(ctx, r, domain.RangeCompleted, 0, nil); err != nil {
			t.Fatalf("완료 보고 실패: %v", err)
		}
	}
}

// 하한을 다시 낮추면 남겨 둔 몫이 되살아나야 합니다.
func TestLoweringFloorRevivesTheFailedRange(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 10_000); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}
	registerNode(t, store, "alpha")

	req := domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "alpha", RangeSize: 100,
	}
	deep, err := store.AcquireBackfillRange(ctx, req)
	if err != nil {
		t.Fatalf("임대 실패: %v", err)
	}
	if err := store.FinishRange(ctx, deep, domain.RangeFailed, 0, errTestFailure); err != nil {
		t.Fatalf("실패 보고 실패: %v", err)
	}

	// 하한을 올리면 안 나옵니다.
	req.FloorID = deep.UpperID + 1
	if _, err := store.AcquireBackfillRange(ctx, req); !errors.Is(err, domain.ErrNoWork) {
		t.Fatalf("하한 위로 올렸는데 일을 줬습니다: %v", err)
	}

	// 낮추면 원래 경계 그대로 다시 나와야 합니다.
	req.FloorID = 0
	back, err := store.AcquireBackfillRange(ctx, req)
	if err != nil {
		t.Fatalf("하한을 낮췄는데 일을 주지 않습니다: %v", err)
	}
	if back.LowerID != deep.LowerID || back.UpperID != deep.UpperID {
		t.Errorf("구간이 %d~%d로 돌아왔습니다. %d~%d를 기대했습니다",
			back.LowerID, back.UpperID, deep.LowerID, deep.UpperID)
	}
}

// 재시도 큐도 하한 아래는 내주면 안 됩니다.
func TestRetryQueueRespectsTheFloor(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 10_000); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}
	registerNode(t, store, "alpha")

	err := store.EnqueueRetries(ctx, "danbooru", "default",
		[]int64{100, 5_000, 9_500, 9_900}, 0)
	if err != nil {
		t.Fatalf("재시도 등록 실패: %v", err)
	}

	items, err := store.LeaseRetries(ctx, "danbooru", "default", "alpha", 10, 9_800)
	if err != nil {
		t.Fatalf("재시도 임대 실패: %v", err)
	}
	for _, item := range items {
		if item.SourcePostID < 9_800 {
			t.Errorf("하한 9800 아래 게시물 %d를 내줬습니다", item.SourcePostID)
		}
	}
	if len(items) != 1 {
		t.Errorf("하한 위 항목은 하나뿐인데 %d개를 줬습니다", len(items))
	}
}

var errTestFailure = errors.New("시험용 실패")

// 하한을 가로지르는 실패 구간의 위쪽 몫은 잃으면 안 됩니다.
//
// 901~1000이 실패해 있는데 하한을 950으로 올린 상황입니다. 통째로 빼면
// 950~1000까지 사라지고, backfill_before_id가 이미 901이라 새로 자를
// 수도 없어 그 몫이 영영 안 모입니다.
func TestStraddlingFailedRangeKeepsItsAllowedPart(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 1_001); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}
	registerNode(t, store, "alpha")

	req := domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "alpha", RangeSize: 100,
	}
	first, err := store.AcquireBackfillRange(ctx, req)
	if err != nil {
		t.Fatalf("임대 실패: %v", err)
	}
	if first.LowerID != 901 || first.UpperID != 1000 {
		t.Fatalf("준비가 잘못됐습니다. 구간이 %d~%d입니다", first.LowerID, first.UpperID)
	}
	if err := store.FinishRange(ctx, first, domain.RangeFailed, 0, errTestFailure); err != nil {
		t.Fatalf("실패 보고 실패: %v", err)
	}

	// 하한을 구간 한가운데로 올립니다.
	req.FloorID = 950
	got, err := store.AcquireBackfillRange(ctx, req)
	if err != nil {
		t.Fatalf("하한 위 몫을 주지 않습니다: %v", err)
	}
	if got.LowerID != 950 || got.UpperID != 1000 {
		t.Fatalf("구간이 %d~%d입니다. 950~1000을 기대했습니다", got.LowerID, got.UpperID)
	}
	if err := store.FinishRange(ctx, got, domain.RangeCompleted, 0, nil); err != nil {
		t.Fatalf("완료 보고 실패: %v", err)
	}

	// 하한 아래 몫은 남아 있다가 하한을 낮추면 나와야 합니다.
	req.FloorID = 0
	rest, err := store.AcquireBackfillRange(ctx, req)
	if err != nil {
		t.Fatalf("하한을 낮췄는데 남은 몫을 주지 않습니다: %v", err)
	}
	if rest.LowerID != 901 || rest.UpperID != 949 {
		t.Errorf("남은 몫이 %d~%d입니다. 901~949를 기대했습니다", rest.LowerID, rest.UpperID)
	}
}

// 시도 횟수를 쓰지 않고 되돌릴 수 있어야 합니다.
//
// 색인이 차서 못 넣은 것은 그 구간의 잘못이 아닙니다. 실패로 적으면
// 다섯 번 만에 죽어서 자리가 생겨도 다시 잡히지 않습니다.
func TestReleaseGivesTheAttemptBack(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 1_001); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}
	registerNode(t, store, "alpha")

	req := domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "alpha", RangeSize: 100,
	}

	// 다섯 번을 채우면 죽습니다. 여섯 번 반납해도 계속 나와야 합니다.
	var last *domain.CrawlRange
	for i := 0; i < 6; i++ {
		r, err := store.AcquireBackfillRange(ctx, req)
		if err != nil {
			t.Fatalf("%d번째 임대에서 막혔습니다: %v", i+1, err)
		}
		if last != nil && r.ID != last.ID {
			t.Fatalf("%d번째에 다른 구간이 나왔습니다", i+1)
		}
		if r.Attempts > 1 {
			t.Errorf("%d번째 시도 횟수가 %d입니다. 반납했으면 1이어야 합니다", i+1, r.Attempts)
		}
		if err := store.ReleaseRange(ctx, r); err != nil {
			t.Fatalf("반납 실패: %v", err)
		}
		last = r
	}
}

// 재시도도 마찬가지입니다.
func TestReleaseRetryGivesTheAttemptBack(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	registerNode(t, store, "alpha")
	if err := store.EnqueueRetries(ctx, "danbooru", "default", []int64{42}, 0); err != nil {
		t.Fatalf("재시도 등록 실패: %v", err)
	}

	for i := 0; i < 6; i++ {
		items, err := store.LeaseRetries(ctx, "danbooru", "default", "alpha", 10, 0)
		if err != nil {
			t.Fatalf("재시도 임대 실패: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("%d번째에 %d건이 나왔습니다. 반납했으면 계속 나와야 합니다", i+1, len(items))
		}
		if err := store.ReleaseRetry(ctx, items[0]); err != nil {
			t.Fatalf("반납 실패: %v", err)
		}
	}
}
