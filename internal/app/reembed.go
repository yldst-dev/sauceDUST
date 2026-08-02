package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"saucedust/internal/domain"
)

// Reembed는 보관해 둔 축소본으로 벡터를 다시 만듭니다.
//
// 모델을 바꾸면 쌓인 벡터가 전부 쓸모없어집니다. 원본을 저장하지 않으므로
// 이것이 없으면 Danbooru를 처음부터 다시 훑어야 합니다. 1,190만 장이면
// 초당 5회 제한에서 28일입니다. 축소본에서 다시 계산하면 네트워크를
// 쓰지 않으므로 GPU 속도만큼 빠릅니다.
//
// 축소본을 남기는 이유가 이것입니다. 이 경로가 없으면 300기가바이트를
// 아무 데도 쓰지 않고 들고 있는 셈입니다.
type Reembed struct {
	images    ImageRepository
	vector    VectorRepository
	index     VectorIndex
	thumbs    ThumbStore
	embedder  Embedder
	log       *slog.Logger
	workers   int
	batchSize int
}

type ReembedDeps struct {
	Images   ImageRepository
	Vector   VectorRepository
	Index    VectorIndex
	Thumbs   ThumbStore
	Embedder Embedder
	Log      *slog.Logger
	// Workers는 워커에 동시에 보낼 요청 수입니다.
	// 워커가 안에서 배치로 묶으므로 하나씩 보내면 GPU가 놉니다.
	Workers   int
	BatchSize int
}

func NewReembed(deps ReembedDeps) (*Reembed, error) {
	switch {
	case deps.Images == nil:
		return nil, errors.New("이미지 저장소가 없습니다")
	case deps.Vector == nil:
		return nil, errors.New("벡터 저장소가 없습니다")
	case deps.Index == nil:
		return nil, errors.New("벡터 색인이 없습니다")
	case deps.Thumbs == nil:
		return nil, errors.New("축소본 저장소가 없습니다")
	case deps.Embedder == nil:
		return nil, errors.New("임베딩 워커가 없습니다")
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Workers <= 0 {
		deps.Workers = 8
	}
	if deps.BatchSize <= 0 {
		deps.BatchSize = 256
	}
	return &Reembed{
		images: deps.Images, vector: deps.Vector, index: deps.Index,
		thumbs: deps.Thumbs, embedder: deps.Embedder, log: deps.Log,
		workers: deps.Workers, batchSize: deps.BatchSize,
	}, nil
}

// ReembedResult는 어디까지 했는지 알려 줍니다.
type ReembedResult struct {
	Done int
	// Missing은 축소본 파일이 없어진 건수입니다. 이것은 다시 내려받아야 합니다.
	Missing int
	Failed  int
}

// Run은 이 모델의 벡터가 없는 이미지를 모두 채웁니다.
//
// 중간에 멈춰도 괜찮습니다. 다음에 다시 부르면 아직 벡터가 없는 것부터
// 이어서 합니다. 며칠 걸릴 수 있는 작업이라 이어서 할 수 있어야 합니다.
func (r *Reembed) Run(ctx context.Context, model domain.EmbeddingModel) (ReembedResult, error) {
	var out ReembedResult

	if err := r.index.EnsureCollection(ctx, model); err != nil {
		return out, err
	}

	total, err := r.images.CountThumbsMissingVector(ctx, model.ID)
	if err != nil {
		return out, err
	}
	if total == 0 {
		r.log.Info("다시 계산할 것이 없습니다", slog.String("model", model.ID))
		return out, nil
	}
	r.log.Info("축소본으로 벡터를 다시 만듭니다",
		slog.String("model", model.ID), slog.Int64("남은 건수", total))

	started := time.Now()
	var cursor int64

	for {
		if err := ctx.Err(); err != nil {
			return out, err
		}

		refs, err := r.images.ThumbsMissingVector(ctx, model.ID, cursor, r.batchSize)
		if err != nil {
			return out, err
		}
		if len(refs) == 0 {
			break
		}
		cursor = refs[len(refs)-1].ImageID

		done, missing, failed, err := r.batch(ctx, model, refs)
		out.Done += done
		out.Missing += missing
		out.Failed += failed
		if err != nil {
			return out, err
		}

		r.log.Info("진행 중",
			slog.Int("끝냄", out.Done), slog.Int64("전체", total),
			slog.Int("축소본 없음", out.Missing), slog.Int("실패", out.Failed),
			slog.Duration("걸린 시간", time.Since(started).Round(time.Second)))
	}

	r.log.Info("다시 계산을 마쳤습니다",
		slog.String("model", model.ID), slog.Int("끝냄", out.Done),
		slog.Int("축소본 없음", out.Missing), slog.Int("실패", out.Failed),
		slog.Duration("걸린 시간", time.Since(started).Round(time.Second)))
	return out, nil
}

type reembedItem struct {
	ref    domain.ThumbRef
	values []float32
}

// batch는 묶음 하나를 처리합니다.
// 임베딩은 여럿을 동시에 보내고, 저장은 한 번에 몰아서 씁니다.
func (r *Reembed) batch(ctx context.Context, model domain.EmbeddingModel,
	refs []domain.ThumbRef) (done, missing, failed int, err error) {

	var (
		mu    sync.Mutex
		items []reembedItem
		wg    sync.WaitGroup
	)
	slots := make(chan struct{}, r.workers)

	for _, ref := range refs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		slots <- struct{}{}

		go func(ref domain.ThumbRef) {
			defer wg.Done()
			defer func() { <-slots }()

			values, outcome := r.one(ctx, model, ref)

			mu.Lock()
			defer mu.Unlock()
			switch outcome {
			case outcomeOK:
				items = append(items, reembedItem{ref: ref, values: values})
			case outcomeMissing:
				missing++
			default:
				failed++
			}
		}(ref)
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return 0, missing, failed, err
	}
	if len(items) == 0 {
		return 0, missing, failed, nil
	}

	stored := make([]domain.StoredVector, 0, len(items))
	points := make([]domain.VectorPoint, 0, len(items))
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		stored = append(stored, domain.StoredVector{
			ImageID: item.ref.ImageID, ModelID: model.ID, Values: item.values,
		})
		ids = append(ids, item.ref.ImageID)
	}

	// PostgreSQL을 먼저 씁니다. Qdrant가 실패해도 rebuild로 되살릴 수 있습니다.
	// 반대 순서였다면 복구할 수 없는 벡터가 Qdrant에만 남습니다.
	if err := r.vector.SaveVectorBatch(ctx, stored); err != nil {
		return 0, missing, failed, err
	}

	images, err := r.images.ImagesByIDs(ctx, ids)
	if err != nil {
		return 0, missing, failed, err
	}
	for _, item := range items {
		points = append(points, domain.VectorPoint{
			ImageID: item.ref.ImageID,
			Vector:  item.values,
			Payload: payloadFor(images[item.ref.ImageID], item.ref.ImageID),
		})
	}
	if err := r.index.Upsert(ctx, model.Collection, points); err != nil {
		return 0, missing, failed, err
	}
	if err := r.vector.MarkIndexedBatch(ctx, ids, []string{model.ID}); err != nil {
		return 0, missing, failed, err
	}
	return len(items), missing, failed, nil
}

type reembedOutcome int

const (
	outcomeOK reembedOutcome = iota
	outcomeMissing
	outcomeFailed
)

func (r *Reembed) one(ctx context.Context, model domain.EmbeddingModel,
	ref domain.ThumbRef) ([]float32, reembedOutcome) {

	jpeg, err := r.thumbs.Get(ctx, ref.ThumbPath)
	if err != nil {
		// 파일이 없어졌으면 다시 내려받는 수밖에 없습니다.
		// 계산 실패와 구분해야 운영자가 무엇을 해야 할지 압니다.
		r.log.Debug("축소본이 없습니다",
			slog.Int64("image", ref.ImageID), slog.String("path", ref.ThumbPath))
		return nil, outcomeMissing
	}

	// 축소본을 또 만들 필요는 없습니다. 이미 있는 것을 쓰는 중입니다.
	result, err := r.embedder.EmbedQuery(ctx, jpeg)
	if err != nil {
		r.log.Warn("다시 계산하지 못했습니다",
			slog.Int64("image", ref.ImageID), slog.Any("err", err))
		return nil, outcomeFailed
	}

	values, ok := result.Vector(model.ID)
	if !ok {
		r.log.Warn("워커가 이 모델의 벡터를 주지 않았습니다",
			slog.String("model", model.ID), slog.Int64("image", ref.ImageID))
		return nil, outcomeFailed
	}
	if len(values) != model.VectorSize {
		r.log.Warn("벡터 차원이 다릅니다",
			slog.String("model", model.ID), slog.Int("받은 차원", len(values)),
			slog.Int("기준", model.VectorSize))
		return nil, outcomeFailed
	}
	if err := domain.ValidateVectors(
		[]domain.Vector{{ModelID: model.ID, Values: values}},
		[]domain.EmbeddingModel{model}); err != nil {
		r.log.Warn("벡터를 쓸 수 없습니다",
			slog.Int64("image", ref.ImageID), slog.Any("err", err))
		return nil, outcomeFailed
	}
	return values, outcomeOK
}

// Estimate는 시작 전에 얼마나 걸릴지 알려 줍니다.
func Estimate(remaining int64, perSecond float64) string {
	if perSecond <= 0 || remaining <= 0 {
		return "알 수 없음"
	}
	d := time.Duration(float64(remaining)/perSecond) * time.Second
	return fmt.Sprintf("%v", d.Round(time.Minute))
}
