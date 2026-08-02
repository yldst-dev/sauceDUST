package postgres

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"saucedust/internal/domain"
)

func testStore(t *testing.T) *Store {
	t.Helper()

	url := os.Getenv("SAUCEDUST_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SAUCEDUST_TEST_DATABASE_URL이 없어 건너뜁니다")
	}

	ctx := context.Background()
	store, err := Open(ctx, Options{URL: url, MaxConns: 16, Schema: "test_postgres"})
	if err != nil {
		t.Fatalf("연결 실패: %v", err)
	}
	t.Cleanup(store.Close)

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("마이그레이션 실패: %v", err)
	}
	if _, err := store.pool.Exec(ctx, `
TRUNCATE crawl_ranges, crawl_post_retries, crawl_states, node_models,
         node_metrics, nodes RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("초기화 실패: %v", err)
	}
	return store
}

func registerNode(t *testing.T, s *Store, id string) {
	t.Helper()
	err := s.RegisterNode(context.Background(), domain.Node{
		ID: id, Role: domain.RoleWorker, Hostname: id, Platform: "test",
	})
	if err != nil {
		t.Fatalf("노드 등록 실패: %v", err)
	}
}

// 여러 노드가 동시에 구간을 요청해도 같은 구간이 두 번 배정되면 안 됩니다.
func TestAcquireRangeIsExclusive(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 100_000); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}

	const workers = 12
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[int64]string{}

	for i := 0; i < workers; i++ {
		nodeID := "node-" + string(rune('a'+i))
		registerNode(t, store, nodeID)

		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			r, err := store.AcquireBackfillRange(ctx, domain.LeaseRequest{
				SourceSite: "danbooru", ScopeKey: "default",
				NodeID: node, RangeSize: 1000,
			})
			if err != nil {
				if !errors.Is(err, domain.ErrNoWork) {
					t.Errorf("%s 임대 실패: %v", node, err)
				}
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if prev, dup := seen[r.LowerID]; dup {
				t.Errorf("구간 %d~%d가 %s와 %s에 중복 배정되었습니다",
					r.LowerID, r.UpperID, prev, node)
			}
			seen[r.LowerID] = node
		}(nodeID)
	}
	wg.Wait()

	if len(seen) != workers {
		t.Fatalf("배정된 구간이 %d개입니다. %d개를 기대했습니다", len(seen), workers)
	}
}

// 다른 노드가 재시작해도 남의 임대를 빼앗으면 안 됩니다. Rust 구현에 있던 버그입니다.
func TestReclaimOwnLeasesDoesNotStealOthers(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 50_000); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}
	registerNode(t, store, "alpha")
	registerNode(t, store, "beta")

	alpha, err := store.AcquireBackfillRange(ctx, domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "alpha", RangeSize: 500,
	})
	if err != nil {
		t.Fatalf("alpha 임대 실패: %v", err)
	}
	beta, err := store.AcquireBackfillRange(ctx, domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "beta", RangeSize: 500,
	})
	if err != nil {
		t.Fatalf("beta 임대 실패: %v", err)
	}

	reclaimed, err := store.ReclaimOwnLeases(ctx, "beta")
	if err != nil {
		t.Fatalf("회수 실패: %v", err)
	}
	if reclaimed != 1 {
		t.Fatalf("회수 개수가 %d입니다. 1을 기대했습니다", reclaimed)
	}

	// alpha의 임대는 그대로 살아 있어야 하므로 완료 보고가 성공해야 합니다.
	if err := store.FinishRange(ctx, alpha, domain.RangeCompleted, 10, nil); err != nil {
		t.Fatalf("alpha 완료 보고가 실패했습니다. 임대를 빼앗겼습니다: %v", err)
	}

	// beta의 임대는 회수되었으므로 완료 보고가 거부되어야 합니다.
	err = store.FinishRange(ctx, beta, domain.RangeCompleted, 10, nil)
	if !errors.Is(err, domain.ErrLeaseConflict) {
		t.Fatalf("beta 완료 보고가 거부되어야 하는데 결과가 %v입니다", err)
	}
}

// 회수된 구간을 다른 노드가 가져간 뒤, 원래 노드의 뒤늦은 보고는 무시되어야 합니다.
func TestFinishRangeFencing(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 20_000); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}
	registerNode(t, store, "slow")
	registerNode(t, store, "fast")

	stale, err := store.AcquireBackfillRange(ctx, domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "slow", RangeSize: 100,
	})
	if err != nil {
		t.Fatalf("임대 실패: %v", err)
	}

	if _, err := store.ReclaimDeadNodeLeases(ctx, time.Nanosecond); err != nil {
		t.Fatalf("죽은 노드 회수 실패: %v", err)
	}

	taken, err := store.AcquireBackfillRange(ctx, domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "fast", RangeSize: 100,
	})
	if err != nil {
		t.Fatalf("재임대 실패: %v", err)
	}
	if taken.ID != stale.ID {
		t.Fatalf("회수된 구간이 재사용되지 않았습니다: %d != %d", taken.ID, stale.ID)
	}
	if taken.Attempts <= stale.Attempts {
		t.Fatalf("attempts가 올라가지 않았습니다: %d <= %d", taken.Attempts, stale.Attempts)
	}

	if err := store.FinishRange(ctx, stale, domain.RangeCompleted, 5, nil); !errors.Is(err, domain.ErrLeaseConflict) {
		t.Fatalf("뒤늦은 보고가 거부되어야 하는데 결과가 %v입니다", err)
	}
	if err := store.FinishRange(ctx, taken, domain.RangeCompleted, 5, nil); err != nil {
		t.Fatalf("현재 임대자의 보고가 실패했습니다: %v", err)
	}
}

// 백필이 바닥까지 내려가면 더 배정하지 않아야 합니다.
func TestBackfillExhausts(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 6); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}
	registerNode(t, store, "solo")

	var got int
	for i := 0; i < 10; i++ {
		r, err := store.AcquireBackfillRange(ctx, domain.LeaseRequest{
			SourceSite: "danbooru", ScopeKey: "default", NodeID: "solo", RangeSize: 2,
		})
		if errors.Is(err, domain.ErrNoWork) {
			break
		}
		if err != nil {
			t.Fatalf("임대 실패: %v", err)
		}
		got++
		if err := store.FinishRange(ctx, r, domain.RangeCompleted, 0, nil); err != nil {
			t.Fatalf("완료 실패: %v", err)
		}
	}
	if got != 3 {
		t.Fatalf("배정된 구간이 %d개입니다. 3개를 기대했습니다 (1~5를 2씩)", got)
	}
}

// 재시도 큐도 같은 항목을 두 노드에 주면 안 됩니다.
func TestLeaseRetriesIsExclusive(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	ids := []int64{1, 2, 3, 4, 5, 6, 7, 8}
	if err := store.EnqueueRetries(ctx, "danbooru", "default", ids, 0); err != nil {
		t.Fatalf("재시도 등록 실패: %v", err)
	}
	registerNode(t, store, "one")
	registerNode(t, store, "two")

	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[int64]string{}

	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(node string) {
			defer wg.Done()
			items, err := store.LeaseRetries(ctx, "danbooru", "default", node, 8, 0)
			if err != nil {
				t.Errorf("%s 임대 실패: %v", node, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			for _, item := range items {
				if prev, dup := seen[item.SourcePostID]; dup {
					t.Errorf("post %d가 %s와 %s에 중복 배정되었습니다",
						item.SourcePostID, prev, node)
				}
				seen[item.SourcePostID] = node
			}
		}(name)
	}
	wg.Wait()

	if len(seen) != len(ids) {
		t.Fatalf("배정된 항목이 %d개입니다. %d개를 기대했습니다", len(seen), len(ids))
	}
}
