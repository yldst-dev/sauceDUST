package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"

	"saucedust/internal/domain"
)

// 다시 계산 경로가 쓰는 것만 갖춘 가짜 저장소입니다.
type reembedStore struct {
	mu      sync.Mutex
	refs    []domain.ThumbRef
	saved   []domain.StoredVector
	indexed []int64
	// 커서를 넘겨 가며 읽는지 확인하려고 셉니다.
	pages int
}

func (s *reembedStore) UpsertImage(context.Context, *domain.Image) (int64, error) {
	return 0, errors.New("쓰지 않습니다")
}
func (s *reembedStore) UpsertImages(context.Context, []*domain.Image) ([]int64, error) {
	return nil, errors.New("쓰지 않습니다")
}
func (s *reembedStore) ExistingPostIDs(context.Context, string, []int64) (map[int64]int64, error) {
	return nil, nil
}
func (s *reembedStore) ImageByID(context.Context, int64) (*domain.Image, error) {
	return nil, domain.ErrNotFound
}
func (s *reembedStore) ImageBySource(context.Context, string, int64) (*domain.Image, error) {
	return nil, domain.ErrNotFound
}
func (s *reembedStore) ImagesByIDs(_ context.Context, ids []int64) (map[int64]domain.Image, error) {
	out := make(map[int64]domain.Image, len(ids))
	for _, id := range ids {
		out[id] = domain.Image{ID: id, SourceSite: "danbooru", SourcePostID: id}
	}
	return out, nil
}
func (s *reembedStore) CountImages(context.Context) (int64, error) { return 0, nil }

func (s *reembedStore) CountThumbsMissingVector(context.Context, string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.refs)), nil
}

// 커서 뒤의 것만 돌려줍니다. 실제 SQL과 같은 규칙이라야 시험이 의미가 있습니다.
func (s *reembedStore) ThumbsMissingVector(_ context.Context, _ string,
	afterID int64, limit int) ([]domain.ThumbRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pages++

	var out []domain.ThumbRef
	for _, ref := range s.refs {
		if ref.ImageID <= afterID {
			continue
		}
		if s.hasVector(ref.ImageID) {
			continue
		}
		out = append(out, ref)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *reembedStore) hasVector(id int64) bool {
	for _, v := range s.saved {
		if v.ImageID == id {
			return true
		}
	}
	return false
}

func (s *reembedStore) SaveVectors(context.Context, int64, []domain.Vector) error { return nil }

func (s *reembedStore) SaveVectorBatch(_ context.Context, items []domain.StoredVector) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, items...)
	return nil
}

func (s *reembedStore) MarkVectorsIndexed(context.Context, int64, []string) error { return nil }

func (s *reembedStore) MarkIndexedBatch(_ context.Context, ids []int64, _ []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.indexed = append(s.indexed, ids...)
	return nil
}

func (s *reembedStore) PendingVectors(context.Context, string, int) ([]domain.StoredVector, error) {
	return nil, nil
}
func (s *reembedStore) VectorsMissing(context.Context, []int64, string) ([]int64, error) {
	return nil, nil
}

type reembedIndex struct {
	mu       sync.Mutex
	ensured  []string
	points   []domain.VectorPoint
	upsertMu error
}

func (i *reembedIndex) EnsureCollection(_ context.Context, m domain.EmbeddingModel) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ensured = append(i.ensured, m.Collection)
	return nil
}
func (i *reembedIndex) Upsert(_ context.Context, _ string, points []domain.VectorPoint) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.upsertMu != nil {
		return i.upsertMu
	}
	i.points = append(i.points, points...)
	return nil
}
func (i *reembedIndex) Search(context.Context, string, []float32, int) ([]domain.VectorMatch, error) {
	return nil, nil
}
func (i *reembedIndex) Count(context.Context, string) (int64, error) { return 0, nil }
func (i *reembedIndex) Ping(context.Context) error                   { return nil }

type reembedThumbs struct {
	mu      sync.Mutex
	present map[string][]byte
	reads   int
}

func (t *reembedThumbs) Put(context.Context, string, int64, []byte) (string, error) {
	return "", errors.New("쓰지 않습니다")
}
func (t *reembedThumbs) Get(_ context.Context, path string) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.reads++
	data, ok := t.present[path]
	if !ok {
		return nil, errors.New("없습니다")
	}
	return data, nil
}
func (t *reembedThumbs) Has(_ context.Context, path string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.present[path]
	return ok
}

type reembedEmbedder struct {
	mu     sync.Mutex
	calls  int
	size   int
	modelI string
	fail   error
	// value가 0이 아니면 그 값으로 채운 벡터를 냅니다.
	value float32
}

func (e *reembedEmbedder) Describe(context.Context) (domain.EmbedderInfo, error) {
	return domain.EmbedderInfo{}, nil
}
func (e *reembedEmbedder) Health(context.Context) error { return nil }
func (e *reembedEmbedder) Embed(context.Context, []byte) (domain.EmbedResult, error) {
	return domain.EmbedResult{}, errors.New("다시 계산에는 EmbedQuery를 써야 합니다")
}
func (e *reembedEmbedder) EmbedQuery(_ context.Context, _ []byte) (domain.EmbedResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.fail != nil {
		return domain.EmbedResult{}, e.fail
	}
	value := e.value
	if value == 0 {
		value = 1
	}
	values := make([]float32, e.size)
	for i := range values {
		values[i] = value
	}
	return domain.EmbedResult{
		Vectors: []domain.Vector{{ModelID: e.modelI, Values: values}},
	}, nil
}

func newReembedFixture(t *testing.T, count int) (*Reembed, *reembedStore,
	*reembedIndex, *reembedThumbs, *reembedEmbedder, domain.EmbeddingModel) {
	t.Helper()

	model := domain.EmbeddingModel{
		ID: "dinov2-vitl14", Kind: domain.ModelCopy,
		VectorSize: 4, Collection: "dinov2-vitl14", Active: true,
	}

	store := &reembedStore{}
	thumbs := &reembedThumbs{present: map[string][]byte{}}
	for i := 1; i <= count; i++ {
		path := fmt.Sprintf("danbooru/00/00/%d.jpg", i)
		store.refs = append(store.refs, domain.ThumbRef{
			ImageID: int64(i), SourceSite: "danbooru",
			SourcePostID: int64(i), ThumbPath: path,
		})
		thumbs.present[path] = []byte{0xff, 0xd8, byte(i)}
	}

	index := &reembedIndex{}
	embedder := &reembedEmbedder{size: model.VectorSize, modelI: model.ID}

	reembed, err := NewReembed(ReembedDeps{
		Images: store, Vector: store, Index: index, Thumbs: thumbs,
		Embedder: embedder, Workers: 4, BatchSize: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	return reembed, store, index, thumbs, embedder, model
}

func TestReembedFillsEveryImage(t *testing.T) {
	reembed, store, index, thumbs, embedder, model := newReembedFixture(t, 25)

	result, err := reembed.Run(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}

	if result.Done != 25 || result.Missing != 0 || result.Failed != 0 {
		t.Fatalf("%+v입니다. 25건 전부 끝나야 합니다", result)
	}
	if len(store.saved) != 25 {
		t.Errorf("PostgreSQL에 %d건 저장했습니다", len(store.saved))
	}
	if len(index.points) != 25 {
		t.Errorf("Qdrant에 %d건 넣었습니다", len(index.points))
	}
	if len(store.indexed) != 25 {
		t.Errorf("색인 완료 표시가 %d건입니다", len(store.indexed))
	}
	if thumbs.reads != 25 {
		t.Errorf("축소본을 %d번 읽었습니다", thumbs.reads)
	}
	if embedder.calls != 25 {
		t.Errorf("워커를 %d번 불렀습니다", embedder.calls)
	}
	// 컬렉션을 먼저 만들어야 넣을 수 있습니다.
	if len(index.ensured) == 0 {
		t.Error("컬렉션을 만들지 않았습니다")
	}
}

// 원본을 다시 내려받지 않는 것이 이 기능의 존재 이유입니다.
// 축소본만 읽고 네트워크는 건드리지 않아야 합니다.
func TestReembedUsesThumbsNotOriginals(t *testing.T) {
	reembed, _, _, thumbs, embedder, model := newReembedFixture(t, 10)

	if _, err := reembed.Run(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	if thumbs.reads != 10 {
		t.Errorf("축소본을 %d번만 읽었습니다. 10번이어야 합니다", thumbs.reads)
	}
	// EmbedQuery를 써야 합니다. Embed는 축소본을 또 만들어 헛일을 합니다.
	if embedder.calls != 10 {
		t.Errorf("워커 호출이 %d번입니다", embedder.calls)
	}
}

// 커서로 넘겨야 합니다. 매번 처음부터 읽으면 같은 것을 되풀이해 끝나지 않습니다.
func TestReembedAdvancesCursor(t *testing.T) {
	reembed, store, _, _, _, model := newReembedFixture(t, 25)

	if _, err := reembed.Run(context.Background(), model); err != nil {
		t.Fatal(err)
	}
	// 10개씩 25건이면 3번 읽고 4번째에 빈 결과가 옵니다.
	if store.pages > 5 {
		t.Errorf("저장소를 %d번 읽었습니다. 커서가 나아가지 않는 것 같습니다", store.pages)
	}

	seen := map[int64]int{}
	for _, v := range store.saved {
		seen[v.ImageID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("이미지 %d를 %d번 저장했습니다", id, n)
		}
	}
}

// 중간에 멈춰도 다음에 이어서 해야 합니다. 며칠 걸리는 작업입니다.
func TestReembedResumes(t *testing.T) {
	reembed, store, _, _, _, model := newReembedFixture(t, 25)

	// 첫 열 건만 미리 채워 둡니다.
	for i := 1; i <= 10; i++ {
		store.saved = append(store.saved, domain.StoredVector{
			ImageID: int64(i), ModelID: model.ID, Values: []float32{1, 0, 0, 0},
		})
	}

	result, err := reembed.Run(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if result.Done != 15 {
		t.Errorf("%d건 했습니다. 남은 15건만 해야 합니다", result.Done)
	}
}

// 축소본 파일이 없어진 것과 계산이 실패한 것은 다릅니다.
// 전자는 다시 내려받아야 하고 후자는 다시 시도하면 됩니다.
func TestReembedSeparatesMissingFromFailed(t *testing.T) {
	reembed, _, _, thumbs, _, model := newReembedFixture(t, 10)

	delete(thumbs.present, "danbooru/00/00/3.jpg")
	delete(thumbs.present, "danbooru/00/00/7.jpg")

	result, err := reembed.Run(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if result.Missing != 2 {
		t.Errorf("축소본 없음이 %d건입니다. 2건이어야 합니다", result.Missing)
	}
	if result.Done != 8 {
		t.Errorf("%d건 했습니다. 나머지 8건은 되어야 합니다", result.Done)
	}
	if result.Failed != 0 {
		t.Errorf("실패가 %d건입니다. 파일 없음은 실패로 세면 안 됩니다", result.Failed)
	}
}

// 한 건이 실패해도 나머지는 계속해야 합니다.
func TestReembedKeepsGoingAfterEmbedFailure(t *testing.T) {
	reembed, _, _, _, embedder, model := newReembedFixture(t, 10)
	embedder.fail = errors.New("워커가 죽었습니다")

	result, err := reembed.Run(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 10 || result.Done != 0 {
		t.Errorf("%+v입니다. 전부 실패로 세야 합니다", result)
	}
}

// NaN 벡터를 저장하면 어떤 질의에도 걸리지 않는 죽은 점이 됩니다.
func TestReembedRejectsBrokenVector(t *testing.T) {
	reembed, store, index, _, embedder, model := newReembedFixture(t, 5)
	embedder.value = float32(math.NaN())

	result, err := reembed.Run(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if result.Done != 0 || result.Failed != 5 {
		t.Errorf("%+v입니다. NaN을 걸러야 합니다", result)
	}
	if len(store.saved) != 0 || len(index.points) != 0 {
		t.Error("NaN 벡터를 저장했습니다")
	}
}

// 차원이 다르면 Qdrant가 받아 주더라도 검색이 조용히 망가집니다.
func TestReembedRejectsWrongDimension(t *testing.T) {
	reembed, store, _, _, embedder, model := newReembedFixture(t, 5)
	embedder.size = 8

	result, err := reembed.Run(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 5 || len(store.saved) != 0 {
		t.Errorf("%+v, 저장 %d건입니다. 차원이 다르면 막아야 합니다",
			result, len(store.saved))
	}
}

// 워커가 다른 모델만 올려 두었을 수 있습니다.
func TestReembedRejectsWrongModel(t *testing.T) {
	reembed, store, _, _, embedder, model := newReembedFixture(t, 5)
	embedder.modelI = "다른모델"

	result, err := reembed.Run(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 5 || len(store.saved) != 0 {
		t.Errorf("%+v입니다. 다른 모델의 벡터를 받아들이면 안 됩니다", result)
	}
}

// Qdrant가 실패해도 PostgreSQL에는 남아야 합니다. 그래야 rebuild로 되살립니다.
func TestReembedSavesToPostgresBeforeQdrant(t *testing.T) {
	reembed, store, index, _, _, model := newReembedFixture(t, 10)
	index.upsertMu = errors.New("Qdrant가 죽었습니다")

	if _, err := reembed.Run(context.Background(), model); err == nil {
		t.Fatal("Qdrant 실패를 알려야 합니다")
	}
	if len(store.saved) == 0 {
		t.Error("PostgreSQL에 아무것도 남지 않았습니다. rebuild로 되살릴 수 없습니다")
	}
	if len(store.indexed) != 0 {
		t.Error("넣지도 않았는데 색인 완료로 표시했습니다")
	}
}

func TestReembedStopsOnCancel(t *testing.T) {
	reembed, _, _, _, _, model := newReembedFixture(t, 1000)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := reembed.Run(ctx, model); err == nil {
		t.Error("취소를 따르지 않았습니다")
	}
}

func TestReembedNoWorkIsFine(t *testing.T) {
	reembed, _, _, _, embedder, model := newReembedFixture(t, 0)

	result, err := reembed.Run(context.Background(), model)
	if err != nil {
		t.Fatal(err)
	}
	if result.Done != 0 || embedder.calls != 0 {
		t.Errorf("할 일이 없는데 %+v, 호출 %d번입니다", result, embedder.calls)
	}
}

func TestNewReembedChecksDeps(t *testing.T) {
	full := ReembedDeps{
		Images: &reembedStore{}, Vector: &reembedStore{},
		Index: &reembedIndex{}, Thumbs: &reembedThumbs{},
		Embedder: &reembedEmbedder{},
	}
	if _, err := NewReembed(full); err != nil {
		t.Fatalf("다 갖췄는데 거부했습니다: %v", err)
	}

	// 축소본 저장소가 없으면 이 기능은 아무것도 할 수 없습니다.
	noThumbs := full
	noThumbs.Thumbs = nil
	if _, err := NewReembed(noThumbs); err == nil {
		t.Error("축소본 저장소 없이 만들어졌습니다")
	}

	noEmbedder := full
	noEmbedder.Embedder = nil
	if _, err := NewReembed(noEmbedder); err == nil {
		t.Error("워커 없이 만들어졌습니다")
	}
}
