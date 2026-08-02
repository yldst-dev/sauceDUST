package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"saucedust/internal/domain"
)

// 실패한 구간을 쉬지 않고 다시 잡으면 시도 횟수를 몇 초 만에 소진합니다.
//
// 상대 사이트가 몇 시간 멈추면 그 사이 백로그 전체가 다섯 번씩 실패해
// 죽고, 상대가 돌아와도 그 ID 대역은 영영 수집되지 않습니다.
func TestFailedRangeWaitsBeforeRetry(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 10_000); err != nil {
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
	if err := store.FinishRange(ctx, first, domain.RangeFailed, 0, errTestFailure); err != nil {
		t.Fatalf("실패 보고 실패: %v", err)
	}

	// 바로 다시 잡으면 안 됩니다. 새 구간은 줘도 됩니다.
	for i := 0; i < 5; i++ {
		got, err := store.AcquireBackfillRange(ctx, req)
		if errors.Is(err, domain.ErrNoWork) {
			break
		}
		if err != nil {
			t.Fatalf("임대 실패: %v", err)
		}
		if got.ID == first.ID {
			t.Fatalf("방금 실패한 구간을 쉬지 않고 다시 줬습니다 (%d번째)", i+1)
		}
		if err := store.FinishRange(ctx, got, domain.RangeCompleted, 0, nil); err != nil {
			t.Fatalf("완료 보고 실패: %v", err)
		}
	}

	// 쉬는 시간이 지나면 다시 나와야 합니다. 영영 묻히면 안 됩니다.
	if _, err := store.pool.Exec(ctx,
		`UPDATE crawl_ranges SET ready_at = now() - interval '1 hour' WHERE id = $1`,
		first.ID); err != nil {
		t.Fatalf("시각 조정 실패: %v", err)
	}
	back, err := store.AcquireBackfillRange(ctx, req)
	if err != nil {
		t.Fatalf("쉬는 시간이 지났는데 주지 않습니다: %v", err)
	}
	if back.ID != first.ID {
		t.Errorf("다른 구간이 나왔습니다: %d", back.ID)
	}
}

// 쉬는 시간은 시도할수록 길어져야 합니다.
func TestBackoffGrowsWithAttempts(t *testing.T) {
	tests := []struct {
		attempts int
		want     time.Duration
	}{
		{0, time.Minute},
		{1, time.Minute},
		{2, 2 * time.Minute},
		{3, 4 * time.Minute},
		{4, 8 * time.Minute},
		{5, 16 * time.Minute},
		{6, 30 * time.Minute},
		{99, 30 * time.Minute},
	}
	for _, tc := range tests {
		if got := retryBackoff(tc.attempts); got != tc.want {
			t.Errorf("%d번째 실패에 %v를 쉽니다. %v를 기대했습니다", tc.attempts, got, tc.want)
		}
	}
}

// 살아 있는 노드의 구간을 다른 노드가 빼앗으면 안 됩니다.
//
// 기본값인 구간 10,000개를 초당 5회로 받으면 33분이 걸리는데 임대 만료는
// 30분입니다. 정상 처리만으로도 만료를 넘깁니다.
func TestRenewKeepsTheLease(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 10_000); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}
	registerNode(t, store, "alpha")
	registerNode(t, store, "beta")

	req := domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "alpha", RangeSize: 100,
	}
	mine, err := store.AcquireBackfillRange(ctx, req)
	if err != nil {
		t.Fatalf("임대 실패: %v", err)
	}

	// 임대한 지 오래된 것처럼 만듭니다.
	if _, err := store.pool.Exec(ctx,
		`UPDATE crawl_ranges SET leased_at = now() - interval '45 minutes' WHERE id = $1`,
		mine.ID); err != nil {
		t.Fatalf("시각 조정 실패: %v", err)
	}

	// 갱신하면 다시 살아나야 합니다.
	if err := store.RenewRange(ctx, mine); err != nil {
		t.Fatalf("갱신 실패: %v", err)
	}

	other := req
	other.NodeID = "beta"
	stolen, err := store.AcquireBackfillRange(ctx, other)
	if err == nil && stolen.ID == mine.ID {
		t.Error("갱신했는데 다른 노드가 빼앗았습니다")
	}
}

// 이미 회수된 구간은 갱신되면 안 됩니다. 그걸 모르고 계속 일하면
// 같은 구간을 둘이 처리합니다.
func TestRenewFailsAfterReclaim(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if err := store.InitCatchupState(ctx, "danbooru", "default", 10_000); err != nil {
		t.Fatalf("상태 초기화 실패: %v", err)
	}
	registerNode(t, store, "alpha")
	registerNode(t, store, "beta")

	req := domain.LeaseRequest{
		SourceSite: "danbooru", ScopeKey: "default", NodeID: "alpha", RangeSize: 100,
	}
	mine, err := store.AcquireBackfillRange(ctx, req)
	if err != nil {
		t.Fatalf("임대 실패: %v", err)
	}

	if _, err := store.pool.Exec(ctx,
		`UPDATE crawl_ranges SET leased_at = now() - interval '45 minutes' WHERE id = $1`,
		mine.ID); err != nil {
		t.Fatalf("시각 조정 실패: %v", err)
	}

	other := req
	other.NodeID = "beta"
	if _, err := store.AcquireBackfillRange(ctx, other); err != nil {
		t.Fatalf("beta가 회수하지 못했습니다: %v", err)
	}

	if err := store.RenewRange(ctx, mine); !errors.Is(err, domain.ErrLeaseConflict) {
		t.Errorf("회수된 뒤에도 갱신이 됐습니다: %v", err)
	}
}

// 행만 있고 벡터가 없으면 아직 안 끝난 것입니다.
//
// 메타데이터를 쓴 뒤 벡터를 넣기 전에 끊기면 행만 남습니다. 존재만 보고
// 건너뛰면 그 이미지는 벡터 없이 영영 남아 검색에 안 걸립니다.
func TestExistingMeansFullyIndexed(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	model := domain.EmbeddingModel{
		ID: "m1", Kind: domain.ModelCopy, Backend: "transformers", Checkpoint: "c",
		VectorSize: 4, Distance: "cosine", Collection: "m1", InputSize: 224, Active: true,
	}
	if err := store.UpsertModel(ctx, model); err != nil {
		t.Fatalf("모델 등록 실패: %v", err)
	}

	img := &domain.Image{
		SourceSite: "danbooru", SourcePostID: 7, MD5: "m", PHash: "p", DHash: "d",
		Tags: []string{}, ArtistTags: []string{}, IndexedBy: "test",
	}
	id, err := store.UpsertImage(ctx, img)
	if err != nil {
		t.Fatalf("이미지 저장 실패: %v", err)
	}

	// 벡터 없이 행만 있는 상태입니다.
	got, err := store.ExistingPostIDs(ctx, "danbooru", []int64{7}, []string{"m1"})
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if len(got) != 0 {
		t.Fatal("벡터가 없는데 끝났다고 합니다. 그 이미지는 영영 검색에 안 걸립니다")
	}

	// 벡터를 넣으면 끝난 것입니다.
	err = store.SaveVectorBatch(ctx, []domain.StoredVector{
		{ImageID: id, ModelID: "m1", Values: []float32{1, 0, 0, 0}},
	})
	if err != nil {
		t.Fatalf("벡터 저장 실패: %v", err)
	}
	got, err = store.ExistingPostIDs(ctx, "danbooru", []int64{7}, []string{"m1"})
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if len(got) != 1 || got[7] != id {
		t.Errorf("벡터까지 넣었는데 끝났다고 하지 않습니다: %v", got)
	}

	// 모델을 하나 더 요구하면 다시 안 끝난 것입니다.
	got, err = store.ExistingPostIDs(ctx, "danbooru", []int64{7}, []string{"m1", "m2"})
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if len(got) != 0 {
		t.Error("두 번째 모델 벡터가 없는데 끝났다고 합니다")
	}

	// 모델 목록이 비면 예전처럼 존재만 봅니다.
	got, err = store.ExistingPostIDs(ctx, "danbooru", []int64{7}, nil)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if len(got) != 1 {
		t.Error("모델을 안 넘겼는데 존재조차 못 찾습니다")
	}
}
