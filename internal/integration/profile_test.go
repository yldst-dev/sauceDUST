package integration

import (
	"context"
	"testing"
	"time"

	"saucedust/internal/app"
	"saucedust/internal/domain"
)

// 적재 단계별 비용을 나눠 잽니다. 어디를 더 손볼지 정하기 위한 측정입니다.
func TestProfileIngestStages(t *testing.T) {
	if testing.Short() {
		t.Skip("짧은 실행에서는 건너뜁니다")
	}

	ctx := context.Background()
	s := newStack(t, 100)
	const size = 128

	measure := func(label string, fn func()) time.Duration {
		fn()
		started := time.Now()
		const rounds = 5
		for i := 0; i < rounds; i++ {
			fn()
		}
		took := time.Since(started) / rounds
		t.Logf("%-28s %7.1f ms  (%.0f 이미지/초)",
			label, float64(took.Microseconds())/1000,
			float64(size)/took.Seconds())
		return took
	}

	var counter int64
	next := func(withThumb bool) []domain.IndexedImage {
		batch := benchBatch(size, 8)
		for i := range batch {
			counter++
			batch[i].Image.SourcePostID = counter
			if !withThumb {
				batch[i].Thumb = nil
			}
		}
		return batch
	}

	measure("전체", func() {
		if err := s.ingest.Submit(ctx, next(true)); err != nil {
			t.Fatal(err)
		}
	})

	measure("축소본 없이", func() {
		if err := s.ingest.Submit(ctx, next(false)); err != nil {
			t.Fatal(err)
		}
	})

	noIndex, err := app.NewIngest(app.IngestDeps{
		Images: s.store, Vector: s.store, Index: nopIndex{},
		Thumbs: nil, Models: models, Log: quiet(),
	})
	if err != nil {
		t.Fatal(err)
	}
	measure("축소본, 색인 없이 (DB만)", func() {
		if err := noIndex.Submit(ctx, next(false)); err != nil {
			t.Fatal(err)
		}
	})

	onlyThumb, err := app.NewIngest(app.IngestDeps{
		Images: nopImages{}, Vector: nopVectors{}, Index: nopIndex{},
		Thumbs: s.thumbs, Models: models, Log: quiet(),
	})
	if err != nil {
		t.Fatal(err)
	}
	measure("축소본 쓰기만", func() {
		if err := onlyThumb.Submit(ctx, next(true)); err != nil {
			t.Fatal(err)
		}
	})
}

type nopIndex struct{}

func (nopIndex) EnsureCollection(context.Context, domain.EmbeddingModel) error { return nil }
func (nopIndex) Upsert(context.Context, string, []domain.VectorPoint) error    { return nil }
func (nopIndex) Count(context.Context, string) (int64, error)                  { return 0, nil }
func (nopIndex) Ping(context.Context) error                                    { return nil }

func (nopIndex) Search(context.Context, string, []float32, int) ([]domain.VectorMatch, error) {
	return nil, nil
}

type nopImages struct{}

func (nopImages) UpsertImage(context.Context, *domain.Image) (int64, error) { return 1, nil }

func (nopImages) UpsertImages(_ context.Context, imgs []*domain.Image) ([]int64, error) {
	ids := make([]int64, len(imgs))
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	return ids, nil
}

func (nopImages) ExistingPostIDs(context.Context, string, []int64, []string) (map[int64]int64, error) {
	return nil, nil
}
func (nopImages) ImageByID(context.Context, int64) (*domain.Image, error) { return nil, nil }

func (nopImages) ImageBySource(context.Context, string, int64) (*domain.Image, error) {
	return nil, nil
}

func (nopImages) ImagesByIDs(context.Context, []int64) (map[int64]domain.Image, error) {
	return nil, nil
}
func (nopImages) CountImages(context.Context) (int64, error) { return 0, nil }

func (nopImages) ThumbsMissingVector(context.Context, string, int64, int) ([]domain.ThumbRef, int64, error) {
	return nil, 0, nil
}

func (nopImages) CountThumbsMissingVector(context.Context, string) (int64, error) {
	return 0, nil
}

type nopVectors struct{}

func (nopVectors) SaveVectors(context.Context, int64, []domain.Vector) error    { return nil }
func (nopVectors) SaveVectorBatch(context.Context, []domain.StoredVector) error { return nil }
func (nopVectors) MarkVectorsIndexed(context.Context, int64, []string) error    { return nil }
func (nopVectors) MarkIndexedBatch(context.Context, []int64, []string) error    { return nil }

func (nopVectors) PendingVectors(context.Context, string, int) ([]domain.StoredVector, error) {
	return nil, nil
}

func (nopVectors) VectorsMissing(context.Context, []int64, string) ([]int64, error) {
	return nil, nil
}

func (nopVectors) VectorsByIDs(context.Context, string, []int64) (map[int64][]float32, error) {
	return nil, nil
}
