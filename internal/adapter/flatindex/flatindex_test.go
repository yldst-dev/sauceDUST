package flatindex

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"saucedust/internal/domain"
)

const dim = 64

// fakeVectors는 원본 벡터를 들고 있는 가짜 저장소입니다.
type fakeVectors struct {
	mu      sync.Mutex
	byID    map[int64][]float32
	calls   int
	lastIDs []int64
	err     error
}

func newFakeVectors() *fakeVectors {
	return &fakeVectors{byID: map[int64][]float32{}}
}

func (f *fakeVectors) put(id int64, v []float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[id] = v
}

func (f *fakeVectors) forget(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byID, id)
}

func (f *fakeVectors) VectorsByIDs(_ context.Context, _ string, ids []int64) (map[int64][]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	f.calls++
	f.lastIDs = append([]int64(nil), ids...)
	out := make(map[int64][]float32, len(ids))
	for _, id := range ids {
		if v, ok := f.byID[id]; ok {
			out[id] = v
		}
	}
	return out, nil
}

func testModel() domain.EmbeddingModel {
	return domain.EmbeddingModel{
		ID: "siglip-b16", VectorSize: dim, Distance: "cosine", Collection: "copy",
	}
}

func newStore(t *testing.T, src Vectors) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(Options{Dir: dir, Source: src})
	if err != nil {
		t.Fatalf("색인을 열지 못했습니다: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, dir
}

// randVec은 흉내 낼 수 있는 무작위 벡터를 냅니다.
func randVec(seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(r.NormFloat64())
	}
	return v
}

// fill은 벡터 n개를 넣고 아이디와 벡터를 함께 돌려줍니다.
func fill(t *testing.T, s *Store, src *fakeVectors, n int) [][]float32 {
	t.Helper()
	pts := make([]domain.VectorPoint, 0, n)
	vecs := make([][]float32, 0, n)
	for i := 0; i < n; i++ {
		v := randVec(int64(i) + 1)
		id := int64(i) + 1
		pts = append(pts, domain.VectorPoint{ImageID: id, Vector: v})
		src.put(id, v)
		vecs = append(vecs, v)
	}
	if err := s.Upsert(context.Background(), "copy", pts); err != nil {
		t.Fatalf("넣지 못했습니다: %v", err)
	}
	return vecs
}

func TestSearchFindsTheExactVector(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	ctx := context.Background()
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatalf("컬렉션을 만들지 못했습니다: %v", err)
	}
	vecs := fill(t, s, src, 300)

	for _, want := range []int{0, 42, 299} {
		got, err := s.Search(ctx, "copy", vecs[want], 5)
		if err != nil {
			t.Fatalf("검색 실패: %v", err)
		}
		if len(got) == 0 {
			t.Fatalf("%d번을 찾을 때 결과가 비었습니다", want)
		}
		if got[0].ImageID != int64(want)+1 {
			t.Errorf("%d번을 넣었는데 1등이 %d입니다", want+1, got[0].ImageID)
		}
		if got[0].Score < 0.999 {
			t.Errorf("같은 벡터인데 점수가 %f입니다", got[0].Score)
		}
	}
}

func TestSearchHonoursLimit(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	ctx := context.Background()
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}
	vecs := fill(t, s, src, 200)

	for _, limit := range []int{1, 3, 10} {
		got, err := s.Search(ctx, "copy", vecs[7], limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != limit {
			t.Errorf("%d개를 달랬는데 %d개가 왔습니다", limit, len(got))
		}
	}
}

// 점수가 내림차순이어야 합니다. 오름차순이면 가장 안 닮은 것이 1등이 됩니다.
func TestSearchSortsByScoreDescending(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	ctx := context.Background()
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}
	fill(t, s, src, 200)

	got, err := s.Search(ctx, "copy", randVec(9999), 10)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Score < got[i].Score {
			t.Fatalf("%d번째 점수 %f가 그 앞 %f보다 큽니다",
				i, got[i].Score, got[i-1].Score)
		}
	}
}

// 원본이 사라진 아이디는 결과에 넣으면 안 됩니다.
// 없는 그림을 답으로 내게 됩니다.
func TestSearchDropsIDsWithoutOriginal(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	ctx := context.Background()
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}
	vecs := fill(t, s, src, 100)
	src.forget(8)

	got, err := s.Search(ctx, "copy", vecs[7], 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		if m.ImageID == 8 {
			t.Fatal("원본이 없는 8번이 결과에 들어 있습니다")
		}
	}
}

// 같은 그림이 두 자리에 들어 있어도 결과에 한 번만 나와야 합니다.
// 안 걸러 내면 같은 그림이 상위를 다 채웁니다.
func TestSearchDedupsRepeatedImageID(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	ctx := context.Background()
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}
	vecs := fill(t, s, src, 50)

	// 7번을 세 번 더 넣습니다.
	dup := []domain.VectorPoint{
		{ImageID: 7, Vector: vecs[6]},
		{ImageID: 7, Vector: vecs[6]},
		{ImageID: 7, Vector: vecs[6]},
	}
	if err := s.Upsert(ctx, "copy", dup); err != nil {
		t.Fatal(err)
	}

	got, err := s.Search(ctx, "copy", vecs[6], 5)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, m := range got {
		if m.ImageID == 7 {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("7번이 %d번 나왔습니다. 한 번이어야 합니다", seen)
	}
}

func TestCountMatchesWhatWentIn(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	ctx := context.Background()
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}

	n, err := s.Count(ctx, "copy")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("빈 컬렉션인데 %d개입니다", n)
	}

	fill(t, s, src, 37)
	if n, err = s.Count(ctx, "copy"); err != nil {
		t.Fatal(err)
	}
	if n != 37 {
		t.Fatalf("37개를 넣었는데 %d개입니다", n)
	}
}

func TestSearchOnEmptyCollection(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	ctx := context.Background()
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}
	got, err := s.Search(ctx, "copy", randVec(1), 5)
	if err != nil {
		t.Fatalf("빈 컬렉션 검색이 실패했습니다: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("빈 컬렉션인데 %d건이 왔습니다", len(got))
	}
}

// 차원이 다르면 벡터가 섞여 검색이 조용히 망가집니다. 막아야 합니다.
func TestReopenWithDifferentDimensionIsRefused(t *testing.T) {
	src := newFakeVectors()
	dir := t.TempDir()
	ctx := context.Background()

	s, err := New(Options{Dir: dir, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := New(Options{Dir: dir, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	m := testModel()
	m.VectorSize = dim * 2
	err = s2.EnsureCollection(ctx, m)
	if !errors.Is(err, domain.ErrModelMismatch) {
		t.Fatalf("차원이 달라도 열렸습니다: %v", err)
	}
}

func TestReopenWithDifferentModelIsRefused(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	ctx := context.Background()
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}

	m := testModel()
	m.ID = "clip-b32"
	if err := s.EnsureCollection(ctx, m); !errors.Is(err, domain.ErrModelMismatch) {
		t.Fatalf("모델이 달라도 열렸습니다: %v", err)
	}
}

// 부호만 남기는 방식이라 길이로 견주는 거리는 담기지 않습니다.
// 조용히 틀리느니 시작을 막아야 합니다.
func TestEuclidDistanceIsRefused(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	m := testModel()
	m.Distance = "euclid"
	if err := s.EnsureCollection(context.Background(), m); err == nil {
		t.Fatal("유클리드 거리가 그대로 통과했습니다")
	}
}

func TestUpsertRefusesWrongDimension(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	ctx := context.Background()
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}

	err := s.Upsert(ctx, "copy", []domain.VectorPoint{
		{ImageID: 1, Vector: make([]float32, dim+1)},
	})
	if !errors.Is(err, domain.ErrModelMismatch) {
		t.Fatalf("차원이 다른데 들어갔습니다: %v", err)
	}
	if n, _ := s.Count(ctx, "copy"); n != 0 {
		t.Fatalf("거절했는데 %d개가 들어 있습니다", n)
	}
}

// 넣던 중에 꺼져서 코드만 늘어난 파일은 잘라 내야 합니다.
// 그대로 두면 자리마다 남의 코드와 아이디가 짝지어집니다.
func TestTornTailIsTrimmedOnOpen(t *testing.T) {
	src := newFakeVectors()
	dir := t.TempDir()
	ctx := context.Background()

	s, err := New(Options{Dir: dir, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}
	vecs := fill(t, s, src, 20)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// 코드 파일에만 한 자리를 더 붙입니다.
	codes := filepath.Join(dir, "copy", codesName)
	f, err := os.OpenFile(codes, os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(make([]byte, dim/8)); err != nil {
		t.Fatal(err)
	}
	f.Close()

	s2, err := New(Options{Dir: dir, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatalf("잘린 꼬리 때문에 열지 못했습니다: %v", err)
	}
	n, err := s2.Count(ctx, "copy")
	if err != nil {
		t.Fatal(err)
	}
	if n != 20 {
		t.Fatalf("자리가 %d개입니다. 20개여야 합니다", n)
	}
	// 잘라 낸 뒤에도 검색이 맞아야 합니다.
	got, err := s2.Search(ctx, "copy", vecs[5], 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].ImageID != 6 {
		t.Fatalf("자른 뒤 1등이 %v입니다", got)
	}
}

// 다시 열었을 때 앞서 넣은 것이 그대로 있어야 합니다.
func TestDataSurvivesReopen(t *testing.T) {
	src := newFakeVectors()
	dir := t.TempDir()
	ctx := context.Background()

	s, err := New(Options{Dir: dir, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}
	vecs := fill(t, s, src, 64)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := New(Options{Dir: dir, Source: src})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if err := s2.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}
	if n, _ := s2.Count(ctx, "copy"); n != 64 {
		t.Fatalf("다시 여니 %d개입니다", n)
	}
	got, err := s2.Search(ctx, "copy", vecs[30], 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ImageID != 31 {
		t.Fatalf("다시 연 뒤 1등이 %v입니다", got)
	}
}

// 붙이는 중에 읽어도 죽지 않아야 합니다. 파일이 커지면 걸어 둔 자리를
// 다시 잡는데, 그때 읽고 있던 슬라이스가 살아 있으면 죽습니다.
func TestConcurrentUpsertAndSearch(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	ctx := context.Background()
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}
	fill(t, s, src, 50)

	var wg sync.WaitGroup
	errs := make(chan error, 32)

	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				id := int64(1000 + w*100 + i)
				v := randVec(id)
				src.put(id, v)
				if err := s.Upsert(ctx, "copy",
					[]domain.VectorPoint{{ImageID: id, Vector: v}}); err != nil {
					errs <- fmt.Errorf("넣기 실패: %w", err)
					return
				}
			}
		}(w)
	}
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(r int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if _, err := s.Search(ctx, "copy", randVec(int64(r*40+i)), 5); err != nil {
					errs <- fmt.Errorf("검색 실패: %w", err)
					return
				}
			}
		}(r)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	if n, _ := s.Count(ctx, "copy"); n != 150 {
		t.Fatalf("자리가 %d개입니다. 150개여야 합니다", n)
	}
}

// 열지 않은 컬렉션에 넣거나 찾으면 조용히 넘어가면 안 됩니다.
func TestUnknownCollectionIsAnError(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	ctx := context.Background()

	if err := s.Upsert(ctx, "없음",
		[]domain.VectorPoint{{ImageID: 1, Vector: randVec(1)}}); err == nil {
		t.Error("없는 컬렉션에 넣었는데 통과했습니다")
	}
	if _, err := s.Search(ctx, "없음", randVec(1), 3); err == nil {
		t.Error("없는 컬렉션을 찾았는데 통과했습니다")
	}
	if _, err := s.Count(ctx, "없음"); err == nil {
		t.Error("없는 컬렉션을 셌는데 통과했습니다")
	}
}

func TestNewRefusesMissingSource(t *testing.T) {
	if _, err := New(Options{Dir: t.TempDir()}); err == nil {
		t.Fatal("원본 조회 없이 열렸습니다. 재점수를 못 하면 정확도가 떨어집니다")
	}
}

// 질의 차원이 컬렉션과 다르면 막아야 합니다.
func TestSearchRefusesWrongQueryDimension(t *testing.T) {
	src := newFakeVectors()
	s, _ := newStore(t, src)
	ctx := context.Background()
	if err := s.EnsureCollection(ctx, testModel()); err != nil {
		t.Fatal(err)
	}
	fill(t, s, src, 10)

	_, err := s.Search(ctx, "copy", make([]float32, dim+3), 3)
	if !errors.Is(err, domain.ErrModelMismatch) {
		t.Fatalf("차원이 다른 질의가 통과했습니다: %v", err)
	}
}
