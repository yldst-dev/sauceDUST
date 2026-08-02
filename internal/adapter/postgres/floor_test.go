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
