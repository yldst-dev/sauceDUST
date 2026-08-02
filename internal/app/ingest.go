package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync"

	"saucedust/internal/domain"
)

// Ingest는 완성된 결과를 저장소에 반영하는 유스케이스입니다.
// control 노드에서만 돕니다. worker 노드는 HTTP로 여기에 보냅니다.
//
// 저장 순서가 중요합니다.
//  1. 축소본 파일
//  2. PostgreSQL 메타데이터
//  3. PostgreSQL 벡터 백업
//  4. Qdrant 색인
//  5. 색인 완료 표시
//
// 4번이 실패해도 1~3번이 남아 있으면 나중에 Qdrant만 재구축할 수 있습니다.
// 반대 순서였다면 Qdrant에만 있고 복구 불가능한 벡터가 생깁니다.
// 축소본이 맨 앞인 이유는 경로가 출처로 정해져 미리 알 수 있기 때문입니다.
// 그래야 이미지 행을 한 번만 씁니다.
type Ingest struct {
	images ImageRepository
	vector VectorRepository
	index  VectorIndex
	thumbs ThumbStore
	models []domain.EmbeddingModel
	log    *slog.Logger

	// 담을 수 있는 장수와 지금 몇 장인지 세는 것입니다. 0이면 재지 않습니다.
	//
	// 작업 노드 쪽에도 같은 검사가 있지만 그것만으로는 부족합니다. 설정을
	// 빠뜨린 노드나 API를 직접 부르는 쪽은 그 검사를 지나치지 않습니다.
	// 여기가 자료가 들어오는 유일한 문이므로 여기서 지켜야 합니다.
	maxIndexed int64
	counter    IndexCounter
	ingestCounterState
}

type IngestDeps struct {
	Images ImageRepository
	Vector VectorRepository
	Index  VectorIndex
	Thumbs ThumbStore
	Models []domain.EmbeddingModel
	Log    *slog.Logger

	MaxIndexed int64
	Counter    IndexCounter
}

func NewIngest(deps IngestDeps) (*Ingest, error) {
	switch {
	case deps.Images == nil:
		return nil, errors.New("이미지 저장소가 없습니다")
	case deps.Vector == nil:
		return nil, errors.New("벡터 저장소가 없습니다")
	case deps.Index == nil:
		return nil, errors.New("벡터 색인이 없습니다")
	case len(deps.Models) == 0:
		return nil, errors.New("활성 임베딩 모델이 없습니다")
	case deps.MaxIndexed > 0 && deps.Counter == nil:
		return nil, errors.New("담을 장수를 정했는데 세는 것이 없습니다")
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	return &Ingest{
		images: deps.Images, vector: deps.Vector, index: deps.Index,
		thumbs: deps.Thumbs, models: deps.Models, log: deps.Log,
		maxIndexed: deps.MaxIndexed, counter: deps.Counter,
	}, nil
}

// Submit은 묶음 하나를 정해진 왕복 횟수로 처리합니다.
// 건마다 왕복하면 128건에 500번이 넘어가므로 전부 묶어서 보냅니다.
func (in *Ingest) Submit(ctx context.Context, batch []domain.IndexedImage) error {
	if len(batch) == 0 {
		return nil
	}
	if err := in.checkRoom(ctx); err != nil {
		return err
	}
	for i := range batch {
		if err := domain.ValidateVectors(batch[i].Vectors, in.models); err != nil {
			return err
		}
	}

	// 1. 축소본을 먼저 씁니다. 경로가 출처로 정해지므로 미리 알 수 있고,
	//    덕분에 이미지 행을 두 번 쓰지 않아도 됩니다.
	in.storeThumbs(ctx, batch)

	// 2. 메타데이터를 한 번의 왕복으로 저장합니다.
	images := make([]*domain.Image, len(batch))
	for i := range batch {
		images[i] = &batch[i].Image
	}
	ids, err := in.images.UpsertImages(ctx, images)
	if err != nil {
		return err
	}

	// 3. 벡터 백업도 한 번에 보냅니다. Qdrant가 사라져도 여기서 복구합니다.
	stored := make([]domain.StoredVector, 0, len(batch)*len(in.models))
	byCollection := map[string][]domain.VectorPoint{}
	for i := range batch {
		imageID := ids[i]
		payload := payloadFor(batch[i].Image, imageID)

		for _, v := range batch[i].Vectors {
			stored = append(stored, domain.StoredVector{
				ImageID: imageID, ModelID: v.ModelID, Values: v.Values,
			})
			model, ok := findModel(in.models, v.ModelID)
			if !ok {
				continue
			}
			byCollection[model.Collection] = append(byCollection[model.Collection],
				domain.VectorPoint{ImageID: imageID, Vector: v.Values, Payload: payload})
		}
	}
	if err := in.vector.SaveVectorBatch(ctx, stored); err != nil {
		return err
	}

	// 4. 검색 색인에 반영합니다. 컬렉션끼리 독립이므로 함께 보냅니다.
	//    여기서 실패해도 PostgreSQL에 벡터가 남아 있어 rebuild로 되살립니다.
	if err := in.upsertCollections(ctx, byCollection); err != nil {
		return err
	}

	// 5. 색인 완료를 한 번에 표시합니다.
	if err := in.vector.MarkIndexedBatch(ctx, ids, in.modelIDs()); err != nil {
		in.log.Warn("색인 완료 표시 실패", slog.String("error", err.Error()))
	}
	return nil
}

// storeThumbs는 축소본 파일을 병렬로 씁니다.
// 서로 독립적인 디스크 쓰기라 순서대로 할 이유가 없습니다.
// 실패해도 색인은 성공으로 둡니다. 축소본은 모델 교체용 편의 사본이지
// 검색에 필요한 데이터가 아닙니다.
func (in *Ingest) storeThumbs(ctx context.Context, batch []domain.IndexedImage) {
	if in.thumbs == nil {
		return
	}

	limit := runtime.GOMAXPROCS(0)
	if limit > 8 {
		limit = 8
	}
	slots := make(chan struct{}, limit)
	var wg sync.WaitGroup

	for i := range batch {
		if len(batch[i].Thumb) == 0 {
			continue
		}
		wg.Add(1)
		slots <- struct{}{}

		go func(item *domain.IndexedImage) {
			defer wg.Done()
			defer func() { <-slots }()

			path, err := in.thumbs.Put(ctx, item.Image.SourceSite, item.Image.SourcePostID, item.Thumb)
			if err != nil {
				in.log.Warn("축소본 저장 실패",
					slog.Int64("post", item.Image.SourcePostID),
					slog.String("error", err.Error()))
				return
			}
			item.Image.ThumbPath = path
		}(&batch[i])
	}
	wg.Wait()
}

func (in *Ingest) upsertCollections(ctx context.Context, byCollection map[string][]domain.VectorPoint) error {
	if len(byCollection) == 1 {
		for collection, points := range byCollection {
			if err := in.index.Upsert(ctx, collection, points); err != nil {
				return fmt.Errorf("컬렉션 %s 색인에 실패했습니다: %w", collection, err)
			}
		}
		return nil
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	for collection, points := range byCollection {
		wg.Add(1)
		go func(collection string, points []domain.VectorPoint) {
			defer wg.Done()
			if err := in.index.Upsert(ctx, collection, points); err != nil {
				mu.Lock()
				defer mu.Unlock()
				if first == nil {
					first = fmt.Errorf("컬렉션 %s 색인에 실패했습니다: %w", collection, err)
				}
			}
		}(collection, points)
	}
	wg.Wait()
	return first
}

func (in *Ingest) modelIDs() []string {
	out := make([]string, 0, len(in.models))
	for _, m := range in.models {
		out = append(out, m.ID)
	}
	return out
}

// RebuildIndex는 PostgreSQL에 남은 벡터로 Qdrant를 다시 채웁니다.
// Qdrant 저장소를 잃어버려도 다시 크롤링할 필요가 없게 하는 복구 경로입니다.
func (in *Ingest) RebuildIndex(ctx context.Context, batchSize int) (int, error) {
	if batchSize <= 0 {
		batchSize = 512
	}
	var total int

	for _, model := range in.models {
		if err := in.index.EnsureCollection(ctx, model); err != nil {
			return total, err
		}

		for {
			if ctx.Err() != nil {
				return total, ctx.Err()
			}

			pending, err := in.vector.PendingVectors(ctx, model.ID, batchSize)
			if err != nil {
				return total, err
			}
			if len(pending) == 0 {
				break
			}

			ids := make([]int64, 0, len(pending))
			for _, v := range pending {
				ids = append(ids, v.ImageID)
			}
			images, err := in.images.ImagesByIDs(ctx, ids)
			if err != nil {
				return total, err
			}

			points := make([]domain.VectorPoint, 0, len(pending))
			for _, v := range pending {
				points = append(points, domain.VectorPoint{
					ImageID: v.ImageID,
					Vector:  v.Values,
					Payload: payloadFor(images[v.ImageID], v.ImageID),
				})
			}
			if err := in.index.Upsert(ctx, model.Collection, points); err != nil {
				return total, err
			}
			if err := in.vector.MarkIndexedBatch(ctx, ids, []string{model.ID}); err != nil {
				return total, err
			}

			total += len(pending)
			in.log.Info("색인을 다시 채우는 중입니다",
				slog.String("model", model.ID), slog.Int("done", total))
		}
	}
	return total, nil
}

func findModel(models []domain.EmbeddingModel, id string) (domain.EmbeddingModel, bool) {
	for _, m := range models {
		if m.ID == id {
			return m, true
		}
	}
	return domain.EmbeddingModel{}, false
}

func payloadFor(img domain.Image, imageID int64) map[string]any {
	return map[string]any{
		"image_id":       imageID,
		"source_site":    img.SourceSite,
		"source_post_id": img.SourcePostID,
		"canonical_url":  img.CanonicalURL,
		"rating":         img.Rating,
		"phash":          img.PHash,
		"dhash":          img.DHash,
	}
}
