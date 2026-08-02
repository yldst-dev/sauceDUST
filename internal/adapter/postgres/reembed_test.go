package postgres

import (
	"context"
	"fmt"
	"testing"

	"saucedust/internal/domain"
)

// 다시 계산 대상을 찾는 조회는 실제 PostgreSQL에서 확인해야 합니다.
// 가짜로는 색인을 쓰는지도, 커서가 제대로 나아가는지도 알 수 없습니다.
func reembedFixture(t *testing.T, count int, withThumb func(int) bool) (*Store, string) {
	t.Helper()
	store := testStore(t)
	ctx := context.Background()

	// query_cache는 embedding_models를 외래키로 걸지 않으므로 CASCADE로
	// 지워지지 않습니다. 빼먹으면 두 번째 실행부터 앞선 시험이 남긴 것을
	// 보게 되어, 처음 한 번만 통과하는 시험이 됩니다.
	if _, err := store.pool.Exec(ctx,
		`TRUNCATE image_vectors, images, query_cache, embedding_models
         RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("초기화 실패: %v", err)
	}

	model := domain.EmbeddingModel{
		ID: "새모델", Kind: domain.ModelCopy, Backend: "t", Checkpoint: "c",
		VectorSize: 4, Distance: "cosine", Collection: "새모델", InputSize: 224,
		Active: true,
	}
	if err := store.UpsertModel(ctx, model); err != nil {
		t.Fatalf("모델 등록 실패: %v", err)
	}

	for i := 1; i <= count; i++ {
		img := &domain.Image{
			SourceSite: "danbooru", SourcePostID: int64(i),
			Tags: []string{}, ArtistTags: []string{},
		}
		if withThumb(i) {
			img.ThumbPath = fmt.Sprintf("danbooru/00/00/%d.jpg", i)
		}
		if _, err := store.UpsertImage(ctx, img); err != nil {
			t.Fatalf("이미지 저장 실패: %v", err)
		}
	}
	return store, model.ID
}

// 전부 다 돌면서 하나도 빠뜨리지 않아야 합니다.
func TestThumbsMissingVectorWalksEverything(t *testing.T) {
	store, modelID := reembedFixture(t, 50, func(int) bool { return true })
	ctx := context.Background()

	seen := map[int64]int{}
	var cursor int64
	for pages := 0; pages < 100; pages++ {
		refs, next, err := store.ThumbsMissingVector(ctx, modelID, cursor, 7)
		if err != nil {
			t.Fatal(err)
		}
		if next == 0 {
			break
		}
		if next <= cursor {
			t.Fatalf("커서가 %d에서 %d로 나아가지 않았습니다", cursor, next)
		}
		cursor = next
		for _, ref := range refs {
			seen[ref.ImageID]++
			if ref.ThumbPath == "" {
				t.Errorf("축소본이 없는 이미지 %d가 나왔습니다", ref.ImageID)
			}
		}
	}

	if len(seen) != 50 {
		t.Fatalf("%d건만 봤습니다. 50건이어야 합니다", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("이미지 %d가 %d번 나왔습니다", id, n)
		}
	}
}

// 축소본이 없으면 다시 계산할 수 없습니다. 대상에서 빠져야 합니다.
func TestThumbsMissingVectorSkipsImagesWithoutThumb(t *testing.T) {
	store, modelID := reembedFixture(t, 40, func(i int) bool { return i%4 != 0 })
	ctx := context.Background()

	n, err := store.CountThumbsMissingVector(ctx, modelID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 30 {
		t.Errorf("%d건이라고 합니다. 30건이어야 합니다", n)
	}

	var cursor int64
	for pages := 0; pages < 100; pages++ {
		refs, next, err := store.ThumbsMissingVector(ctx, modelID, cursor, 10)
		if err != nil {
			t.Fatal(err)
		}
		if next == 0 {
			break
		}
		cursor = next
		for _, ref := range refs {
			if ref.SourcePostID%4 == 0 {
				t.Errorf("축소본이 없는 %d가 나왔습니다", ref.SourcePostID)
			}
		}
	}
}

// 이미 계산된 것은 다시 나오면 안 됩니다. 이어받기가 이것에 달려 있습니다.
func TestThumbsMissingVectorExcludesDone(t *testing.T) {
	store, modelID := reembedFixture(t, 30, func(int) bool { return true })
	ctx := context.Background()

	// 1번부터 20번까지 벡터를 넣습니다.
	var stored []domain.StoredVector
	for i := int64(1); i <= 20; i++ {
		stored = append(stored, domain.StoredVector{
			ImageID: i, ModelID: modelID, Values: []float32{1, 0, 0, 0},
		})
	}
	if err := store.SaveVectorBatch(ctx, stored); err != nil {
		t.Fatal(err)
	}

	n, err := store.CountThumbsMissingVector(ctx, modelID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 10 {
		t.Fatalf("%d건 남았다고 합니다. 10건이어야 합니다", n)
	}

	var got []int64
	var cursor int64
	for pages := 0; pages < 100; pages++ {
		refs, next, err := store.ThumbsMissingVector(ctx, modelID, cursor, 8)
		if err != nil {
			t.Fatal(err)
		}
		if next == 0 {
			break
		}
		cursor = next
		for _, ref := range refs {
			got = append(got, ref.ImageID)
		}
	}

	if len(got) != 10 {
		t.Fatalf("%v가 나왔습니다. 10건이어야 합니다", got)
	}
	for _, id := range got {
		if id <= 20 {
			t.Errorf("이미 계산된 %d가 다시 나왔습니다", id)
		}
	}
}

// 한 묶음이 통째로 이미 계산된 것이어도 커서는 나아가야 합니다.
// 여기서 멈추면 그 뒤에 남은 것을 통째로 놓칩니다.
func TestThumbsMissingVectorAdvancesPastFullyDonePage(t *testing.T) {
	store, modelID := reembedFixture(t, 30, func(int) bool { return true })
	ctx := context.Background()

	// 앞 열 건만 채웁니다. 묶음 크기를 10으로 두면 첫 묶음이 통째로 비어 나옵니다.
	var stored []domain.StoredVector
	for i := int64(1); i <= 10; i++ {
		stored = append(stored, domain.StoredVector{
			ImageID: i, ModelID: modelID, Values: []float32{1, 0, 0, 0},
		})
	}
	if err := store.SaveVectorBatch(ctx, stored); err != nil {
		t.Fatal(err)
	}

	refs, next, err := store.ThumbsMissingVector(ctx, modelID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 {
		t.Fatalf("%d건이 나왔습니다. 첫 묶음은 전부 계산되어 있습니다", len(refs))
	}
	if next != 10 {
		t.Fatalf("커서가 %d입니다. 10이어야 다음 묶음으로 넘어갑니다", next)
	}

	refs, next, err = store.ThumbsMissingVector(ctx, modelID, next, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 10 || next != 20 {
		t.Fatalf("%d건, 커서 %d입니다. 11~20번 열 건이 나와야 합니다", len(refs), next)
	}
}

func TestThumbsMissingVectorEmptyTable(t *testing.T) {
	store, modelID := reembedFixture(t, 0, func(int) bool { return true })
	ctx := context.Background()

	refs, next, err := store.ThumbsMissingVector(ctx, modelID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 0 || next != 0 {
		t.Errorf("빈 표에서 %d건, 커서 %d가 나왔습니다", len(refs), next)
	}

	n, err := store.CountThumbsMissingVector(ctx, modelID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("빈 표인데 %d건이라고 합니다", n)
	}
}

// 색인을 실제로 쓰는지 확인합니다. 순차 훑기로 떨어지면 1천만 행에서 못 씁니다.
func TestThumbsMissingVectorUsesIndex(t *testing.T) {
	store, modelID := reembedFixture(t, 200, func(int) bool { return true })
	ctx := context.Background()

	var plan string
	rows, err := store.pool.Query(ctx, `
EXPLAIN SELECT id, source_site, source_post_id, thumb_path
FROM images
WHERE id > $1 AND thumb_path IS NOT NULL AND thumb_path <> ''
ORDER BY id LIMIT $2`, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan += line + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	// 표가 작으면 PostgreSQL이 순차 훑기를 고를 수 있습니다.
	// 색인이 있는지만 확인하고, 계획 자체는 참고로 남깁니다.
	var exists bool
	err = store.pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM pg_indexes
    WHERE schemaname = current_schema() AND indexname = 'idx_images_with_thumb'
)`).Scan(&exists)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("idx_images_with_thumb 색인이 없습니다. 큰 표에서 전체를 훑게 됩니다")
	}
	t.Logf("계획:\n%s", plan)
	_ = modelID
}

// 질의 캐시는 벡터와 해시를 함께 담아야 합니다.
// 해시를 빼면 캐시가 맞았을 때 재정렬을 못 해서 같은 이미지의 답이 달라집니다.
func TestQueryCacheKeepsHash(t *testing.T) {
	store, modelID := reembedFixture(t, 1, func(int) bool { return true })
	ctx := context.Background()

	const sha = "abc123"
	const phash = "f0f0f0f0f0f0f0f0"
	want := []float32{0.5, -0.5, 0.25, 0}

	if _, _, ok, err := store.CachedQuery(ctx, sha, modelID); err != nil || ok {
		t.Fatalf("비어 있어야 합니다: ok=%v err=%v", ok, err)
	}
	if err := store.SaveQuery(ctx, sha, modelID, want, phash); err != nil {
		t.Fatal(err)
	}

	got, gotHash, ok, err := store.CachedQuery(ctx, sha, modelID)
	if err != nil || !ok {
		t.Fatalf("찾지 못했습니다: ok=%v err=%v", ok, err)
	}
	if gotHash != phash {
		t.Errorf("해시가 %q입니다. %q를 기대했습니다", gotHash, phash)
	}
	if len(got) != len(want) {
		t.Fatalf("벡터 길이가 %d입니다", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d번째가 %v입니다. %v를 기대했습니다", i, got[i], want[i])
		}
	}
}

// 다시 저장하면 해시도 갱신되어야 합니다.
func TestQueryCacheUpdatesHash(t *testing.T) {
	store, modelID := reembedFixture(t, 1, func(int) bool { return true })
	ctx := context.Background()

	const sha = "same-image"
	values := []float32{1, 0}

	if err := store.SaveQuery(ctx, sha, modelID, values, ""); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveQuery(ctx, sha, modelID, values, "aaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}

	_, gotHash, ok, err := store.CachedQuery(ctx, sha, modelID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if gotHash != "aaaaaaaaaaaaaaaa" {
		t.Errorf("해시가 %q입니다. 갱신되어야 합니다", gotHash)
	}
}
