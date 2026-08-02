package app

import (
	"context"
	"testing"

	"saucedust/internal/domain"
)

type fakeIndex struct {
	matches map[string][]domain.VectorMatch
}

func (f *fakeIndex) EnsureCollection(context.Context, domain.EmbeddingModel) error { return nil }

func (f *fakeIndex) Upsert(context.Context, string, []domain.VectorPoint) error { return nil }

func (f *fakeIndex) Search(_ context.Context, collection string, _ []float32, limit int) ([]domain.VectorMatch, error) {
	hits := f.matches[collection]
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (f *fakeIndex) Count(context.Context, string) (int64, error) { return 0, nil }

func (f *fakeIndex) Ping(context.Context) error { return nil }

type stubImages struct {
	byID map[int64]domain.Image
}

func (s *stubImages) UpsertImage(context.Context, *domain.Image) (int64, error) { return 0, nil }

func (s *stubImages) UpsertImages(context.Context, []*domain.Image) ([]int64, error) {
	return nil, nil
}

func (s *stubImages) ExistingPostIDs(context.Context, string, []int64) (map[int64]int64, error) {
	return nil, nil
}

func (s *stubImages) ImageByID(context.Context, int64) (*domain.Image, error) {
	return nil, domain.ErrNotFound
}

func (s *stubImages) ImageBySource(context.Context, string, int64) (*domain.Image, error) {
	return nil, domain.ErrNotFound
}

func (s *stubImages) ImagesByIDs(_ context.Context, ids []int64) (map[int64]domain.Image, error) {
	out := map[int64]domain.Image{}
	for _, id := range ids {
		if img, ok := s.byID[id]; ok {
			out[id] = img
		}
	}
	return out, nil
}

func (s *stubImages) CountImages(context.Context) (int64, error) { return 0, nil }

type hashEmbedder struct {
	phash string
}

func (h *hashEmbedder) Describe(context.Context) (domain.EmbedderInfo, error) {
	return domain.EmbedderInfo{Device: domain.DeviceCPU, Models: testModels}, nil
}

func (h *hashEmbedder) Health(context.Context) error { return nil }

func (h *hashEmbedder) EmbedQuery(ctx context.Context, image []byte) (domain.EmbedResult, error) {
	return h.Embed(ctx, image)
}

func (h *hashEmbedder) Embed(context.Context, []byte) (domain.EmbedResult, error) {
	return domain.EmbedResult{
		Vectors: []domain.Vector{
			{ModelID: "copy-model", Values: []float32{1, 0, 0, 0}},
			{ModelID: "semantic-model", Values: []float32{1, 0, 0, 0}},
		},
		Hashes: domain.Hashes{PHash: h.phash},
	}, nil
}

// 벡터 점수가 낮아도 해시가 같으면 원본이므로 1등이 되어야 합니다.
func TestSearchPromotesExactHashMatch(t *testing.T) {
	const queryHash = "f0f0f0f0f0f0f0f0"

	index := &fakeIndex{matches: map[string][]domain.VectorMatch{
		"copy": {
			{ImageID: 1, Score: 0.99},
			{ImageID: 2, Score: 0.95},
			{ImageID: 3, Score: 0.90},
		},
	}}
	images := &stubImages{byID: map[int64]domain.Image{
		1: {ID: 1, SourcePostID: 100, PHash: "0000000000000000"},
		2: {ID: 2, SourcePostID: 200, PHash: "00000000000000ff"},
		3: {ID: 3, SourcePostID: 300, PHash: queryHash},
	}}

	search, err := NewSearch(SearchConfig{Limit: 3, Candidates: 10}, SearchDeps{
		Embedder: &hashEmbedder{phash: queryHash},
		Index:    index, Images: images, Models: testModels, Log: quietLogger(),
	})
	if err != nil {
		t.Fatalf("검색기 생성 실패: %v", err)
	}

	result, err := search.ByImage(context.Background(), []byte("query"))
	if err != nil {
		t.Fatalf("검색 실패: %v", err)
	}
	if len(result.Hits) != 3 {
		t.Fatalf("결과가 %d건입니다", len(result.Hits))
	}
	if result.Hits[0].Image.SourcePostID != 300 {
		t.Fatalf("1등이 게시물 %d입니다. 해시가 같은 300이 와야 합니다",
			result.Hits[0].Image.SourcePostID)
	}
	if !result.ExactMatch {
		t.Fatal("원본을 찾았다고 표시해야 합니다")
	}
	if result.Hits[0].HashDistance != 0 {
		t.Fatalf("해시 거리가 %d입니다", result.Hits[0].HashDistance)
	}
}

// 해시가 모두 다르면 벡터 점수 순서를 그대로 유지해야 합니다.
func TestSearchKeepsVectorOrderWithoutHashMatch(t *testing.T) {
	index := &fakeIndex{matches: map[string][]domain.VectorMatch{
		"copy": {
			{ImageID: 1, Score: 0.99},
			{ImageID: 2, Score: 0.95},
		},
	}}
	images := &stubImages{byID: map[int64]domain.Image{
		1: {ID: 1, SourcePostID: 100, PHash: "0000000000000000"},
		2: {ID: 2, SourcePostID: 200, PHash: "1111111111111111"},
	}}

	search, _ := NewSearch(SearchConfig{Limit: 5}, SearchDeps{
		Embedder: &hashEmbedder{phash: "ffffffffffffffff"},
		Index:    index, Images: images, Models: testModels, Log: quietLogger(),
	})

	result, err := search.ByImage(context.Background(), []byte("query"))
	if err != nil {
		t.Fatalf("검색 실패: %v", err)
	}
	if result.Hits[0].Image.SourcePostID != 100 {
		t.Fatalf("1등이 게시물 %d입니다", result.Hits[0].Image.SourcePostID)
	}
	if result.ExactMatch {
		t.Fatal("같은 그림이 없으면 원본을 찾았다고 하면 안 됩니다")
	}
}

// 복제본 탐지용 모델이 있으면 그것을 씁니다.
func TestSearchPrefersCopyModel(t *testing.T) {
	index := &fakeIndex{matches: map[string][]domain.VectorMatch{
		"semantic": {{ImageID: 9, Score: 0.9}},
	}}
	images := &stubImages{byID: map[int64]domain.Image{9: {ID: 9}}}

	search, _ := NewSearch(SearchConfig{}, SearchDeps{
		Embedder: &hashEmbedder{}, Index: index, Images: images,
		Models: testModels, Log: quietLogger(),
	})

	result, err := search.ByImage(context.Background(), []byte("query"))
	if err != nil {
		t.Fatalf("검색 실패: %v", err)
	}
	// copy 컬렉션에는 결과가 없으므로 비어야 합니다. semantic을 봤다면 1건이 옵니다.
	if len(result.Hits) != 0 {
		t.Fatalf("copy 모델을 써야 하는데 semantic 결과 %d건이 왔습니다", len(result.Hits))
	}
}

func TestSearchRejectsEmptyImage(t *testing.T) {
	search, _ := NewSearch(SearchConfig{}, SearchDeps{
		Embedder: &hashEmbedder{}, Index: &fakeIndex{},
		Images: &stubImages{}, Models: testModels, Log: quietLogger(),
	})
	if _, err := search.ByImage(context.Background(), nil); err == nil {
		t.Fatal("빈 이미지는 거부해야 합니다")
	}
}

func TestSearchLimitsResults(t *testing.T) {
	matches := make([]domain.VectorMatch, 0, 20)
	byID := map[int64]domain.Image{}
	for i := int64(1); i <= 20; i++ {
		matches = append(matches, domain.VectorMatch{ImageID: i, Score: float32(1) / float32(i)})
		byID[i] = domain.Image{ID: i, SourcePostID: i}
	}

	search, _ := NewSearch(SearchConfig{Limit: 5, Candidates: 20}, SearchDeps{
		Embedder: &hashEmbedder{},
		Index:    &fakeIndex{matches: map[string][]domain.VectorMatch{"copy": matches}},
		Images:   &stubImages{byID: byID}, Models: testModels, Log: quietLogger(),
	})

	result, err := search.ByImage(context.Background(), []byte("query"))
	if err != nil {
		t.Fatalf("검색 실패: %v", err)
	}
	if len(result.Hits) != 5 {
		t.Fatalf("결과가 %d건입니다. 5건으로 잘라야 합니다", len(result.Hits))
	}
}

func TestHammingDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		bad  bool
	}{
		{"0000", "0000", 0, false},
		{"0000", "0001", 1, false},
		{"0000", "000f", 4, false},
		{"ffff", "0000", 16, false},
		{"00", "0000", 0, true},
		{"zz", "00", 0, true},
		{"", "00", 0, true},
	}
	for _, tc := range cases {
		got, err := domain.HammingDistance(tc.a, tc.b)
		if tc.bad {
			if err == nil {
				t.Errorf("%q와 %q는 오류가 나야 합니다", tc.a, tc.b)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q와 %q에서 오류: %v", tc.a, tc.b, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q와 %q의 거리가 %d입니다. %d를 기대했습니다", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestSameImageThreshold(t *testing.T) {
	if got := domain.SameImageThreshold("f0f0f0f0f0f0f0f0"); got != 5 {
		t.Fatalf("64비트 해시 기준값이 %d입니다. 5를 기대했습니다", got)
	}
	if got := domain.SameImageThreshold("ff"); got != 1 {
		t.Fatalf("짧은 해시 기준값이 %d입니다. 최소 1이어야 합니다", got)
	}
}
