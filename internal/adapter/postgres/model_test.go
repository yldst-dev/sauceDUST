package postgres

import (
	"context"
	"testing"

	"saucedust/internal/domain"
)

func model(id string, kind domain.ModelKind, size int) domain.EmbeddingModel {
	return domain.EmbeddingModel{
		ID: id, Kind: kind, Backend: "transformers", Checkpoint: "x/" + id,
		VectorSize: size, Distance: "cosine", Collection: id, InputSize: 224, Active: true,
	}
}

func resetModels(t *testing.T, s *Store) {
	t.Helper()
	_, err := s.pool.Exec(context.Background(),
		`TRUNCATE image_vectors, embedding_models RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("초기화 실패: %v", err)
	}
}

// 용도별로 활성 모델은 하나뿐이어야 합니다. 둘이면 벡터가 섞입니다.
func TestUpsertModelSwapsActiveOfSameKind(t *testing.T) {
	store := testStore(t)
	resetModels(t, store)
	ctx := context.Background()

	if err := store.UpsertModel(ctx, model("old-copy", domain.ModelCopy, 512)); err != nil {
		t.Fatalf("첫 등록 실패: %v", err)
	}
	if err := store.UpsertModel(ctx, model("new-copy", domain.ModelCopy, 768)); err != nil {
		t.Fatalf("교체 실패: %v", err)
	}

	active, err := store.ActiveModels(ctx)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("활성 모델이 %d개입니다. 1개여야 합니다", len(active))
	}
	if active[0].ID != "new-copy" {
		t.Fatalf("활성 모델이 %q입니다", active[0].ID)
	}
}

// 용도가 다르면 함께 활성일 수 있어야 합니다. 벡터 두 개를 쓰는 구성입니다.
func TestDifferentKindsCoexist(t *testing.T) {
	store := testStore(t)
	resetModels(t, store)
	ctx := context.Background()

	if err := store.UpsertModel(ctx, model("dinov2", domain.ModelCopy, 768)); err != nil {
		t.Fatalf("등록 실패: %v", err)
	}
	if err := store.UpsertModel(ctx, model("siglip", domain.ModelSemantic, 768)); err != nil {
		t.Fatalf("등록 실패: %v", err)
	}

	active, err := store.ActiveModels(ctx)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if len(active) != 2 {
		t.Fatalf("활성 모델이 %d개입니다. 2개여야 합니다", len(active))
	}
}

// 같은 모델을 다시 등록해도 문제가 없어야 합니다.
func TestUpsertModelIsIdempotent(t *testing.T) {
	store := testStore(t)
	resetModels(t, store)
	ctx := context.Background()

	m := model("dinov2", domain.ModelCopy, 768)
	for i := 0; i < 3; i++ {
		if err := store.UpsertModel(ctx, m); err != nil {
			t.Fatalf("%d번째 등록 실패: %v", i, err)
		}
	}

	active, _ := store.ActiveModels(ctx)
	if len(active) != 1 {
		t.Fatalf("활성 모델이 %d개입니다", len(active))
	}
}

func TestDeactivateModel(t *testing.T) {
	store := testStore(t)
	resetModels(t, store)
	ctx := context.Background()

	if err := store.UpsertModel(ctx, model("dinov2", domain.ModelCopy, 768)); err != nil {
		t.Fatalf("등록 실패: %v", err)
	}
	if err := store.DeactivateModel(ctx, "dinov2"); err != nil {
		t.Fatalf("비활성화 실패: %v", err)
	}

	active, _ := store.ActiveModels(ctx)
	if len(active) != 0 {
		t.Fatalf("활성 모델이 %d개 남았습니다", len(active))
	}
	if err := store.DeactivateModel(ctx, "없는모델"); err == nil {
		t.Fatal("없는 모델은 오류여야 합니다")
	}
}

// 노드가 다른 모델을 올렸으면 작업을 시작하기 전에 막아야 합니다.
func TestVerifyNodeModelsCatchesMismatch(t *testing.T) {
	store := testStore(t)
	resetModels(t, store)
	ctx := context.Background()

	if err := store.UpsertModel(ctx, model("dinov2", domain.ModelCopy, 768)); err != nil {
		t.Fatalf("등록 실패: %v", err)
	}
	registerNode(t, store, "node-x")

	cases := map[string][]domain.EmbeddingModel{
		"모델 없음":   {model("something-else", domain.ModelCopy, 768)},
		"차원 다름":   {model("dinov2", domain.ModelCopy, 512)},
		"아무것도 없음": {},
	}
	for name, reported := range cases {
		if err := store.VerifyNodeModels(ctx, "node-x", reported); err == nil {
			t.Errorf("%s는 거부해야 합니다", name)
		}
	}

	ok := []domain.EmbeddingModel{model("dinov2", domain.ModelCopy, 768)}
	if err := store.VerifyNodeModels(ctx, "node-x", ok); err != nil {
		t.Fatalf("맞는 구성인데 거부했습니다: %v", err)
	}
}

// 작가 태그가 없는 게시물도 저장돼야 합니다.
// tags와 artist_tags가 NOT NULL이라 nil을 그대로 보내면 실패합니다.
func TestUpsertImageWithoutTags(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if _, err := store.pool.Exec(ctx,
		`TRUNCATE image_vectors, images RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("초기화 실패: %v", err)
	}

	img := &domain.Image{
		SourceSite: "danbooru", SourcePostID: 991,
		Width: 800, Height: 600, Rating: "g",
	}
	id, err := store.UpsertImage(ctx, img)
	if err != nil {
		t.Fatalf("태그 없는 이미지 저장 실패: %v", err)
	}

	got, err := store.ImageByID(ctx, id)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if got.Tags == nil || len(got.Tags) != 0 {
		t.Fatalf("태그가 %v입니다. 빈 배열이어야 합니다", got.Tags)
	}
	if got.ArtistTags == nil || len(got.ArtistTags) != 0 {
		t.Fatalf("작가 태그가 %v입니다", got.ArtistTags)
	}
}

// 묶음 저장에서도 마찬가지입니다. 한 건이 실패하면 묶음 전체가 날아갑니다.
func TestUpsertImagesWithoutTags(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	if _, err := store.pool.Exec(ctx,
		`TRUNCATE image_vectors, images RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("초기화 실패: %v", err)
	}

	imgs := []*domain.Image{
		{SourceSite: "danbooru", SourcePostID: 1001, Tags: []string{"1girl"}},
		{SourceSite: "danbooru", SourcePostID: 1002},
		{SourceSite: "danbooru", SourcePostID: 1003, ArtistTags: []string{"someone"}},
	}
	ids, err := store.UpsertImages(ctx, imgs)
	if err != nil {
		t.Fatalf("묶음 저장 실패: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("저장된 건수가 %d입니다", len(ids))
	}
}
