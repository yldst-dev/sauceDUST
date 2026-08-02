package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"saucedust/internal/domain"
)

type SearchConfig struct {
	// Candidates는 재정렬 전에 벡터 검색으로 뽑을 후보 수입니다.
	Candidates int
	// Limit은 최종으로 돌려줄 개수입니다.
	Limit int
}

// Search는 업로드한 이미지의 출처를 찾습니다.
//
// 벡터 검색만으로는 "비슷한 그림"이 먼저 나올 수 있으므로,
// 후보를 넉넉히 뽑은 뒤 지각 해시로 다시 정렬합니다.
// 해시가 거의 같은 후보가 있으면 그것이 원본이므로 맨 앞으로 올립니다.
type Search struct {
	cfg      SearchConfig
	embedder Embedder
	index    VectorIndex
	images   ImageRepository
	cache    QueryCacheRepository
	models   []domain.EmbeddingModel
	log      *slog.Logger
}

type SearchDeps struct {
	Embedder Embedder
	Index    VectorIndex
	Images   ImageRepository
	Cache    QueryCacheRepository
	Models   []domain.EmbeddingModel
	Log      *slog.Logger
}

type SearchResult struct {
	Hits []Hit
	// ExactMatch는 원본이라고 확신할 수 있는 결과를 찾았는지입니다.
	ExactMatch bool
	Device     domain.Device
}

type Hit struct {
	Image domain.Image
	// Score는 벡터 유사도입니다. 1에 가까울수록 비슷합니다.
	Score float32
	// HashDistance는 지각 해시가 몇 비트 다른지입니다. -1이면 비교하지 못했습니다.
	HashDistance int
	// Exact는 사실상 같은 그림으로 판단했는지입니다.
	Exact   bool
	ModelID string
}

func NewSearch(cfg SearchConfig, deps SearchDeps) (*Search, error) {
	switch {
	case deps.Embedder == nil:
		return nil, errors.New("임베딩 클라이언트가 없습니다")
	case deps.Index == nil:
		return nil, errors.New("벡터 색인이 없습니다")
	case deps.Images == nil:
		return nil, errors.New("이미지 저장소가 없습니다")
	case len(deps.Models) == 0:
		return nil, errors.New("활성 임베딩 모델이 없습니다")
	}
	if cfg.Limit <= 0 {
		cfg.Limit = 5
	}
	if cfg.Candidates < cfg.Limit {
		cfg.Candidates = cfg.Limit * 4
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	return &Search{
		cfg: cfg, embedder: deps.Embedder, index: deps.Index,
		images: deps.Images, cache: deps.Cache, models: deps.Models, log: deps.Log,
	}, nil
}

func (s *Search) ByImage(ctx context.Context, image []byte) (*SearchResult, error) {
	if len(image) == 0 {
		return nil, errors.New("이미지가 비어 있습니다")
	}

	primary, ok := s.primaryModel()
	if !ok {
		return nil, errors.New("검색에 쓸 모델을 찾지 못했습니다")
	}

	sum := sha256.Sum256(image)
	sha := hex.EncodeToString(sum[:])

	vector, queryHash, device, err := s.queryVector(ctx, sha, primary, image)
	if err != nil {
		return nil, err
	}

	matches, err := s.index.Search(ctx, primary.Collection, vector, s.cfg.Candidates)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return &SearchResult{Device: device}, nil
	}

	ids := make([]int64, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m.ImageID)
	}
	images, err := s.images.ImagesByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := &SearchResult{Device: device}
	for _, m := range matches {
		img, ok := images[m.ImageID]
		if !ok {
			continue
		}
		hit := Hit{Image: img, Score: m.Score, HashDistance: -1, ModelID: primary.ID}

		if queryHash != "" && img.PHash != "" {
			if distance, err := domain.HammingDistance(queryHash, img.PHash); err == nil {
				hit.HashDistance = distance
				hit.Exact = distance <= domain.SameImageThreshold(queryHash)
			}
		}
		result.Hits = append(result.Hits, hit)
	}

	rerank(result.Hits)
	if len(result.Hits) > s.cfg.Limit {
		result.Hits = result.Hits[:s.cfg.Limit]
	}
	result.ExactMatch = len(result.Hits) > 0 && result.Hits[0].Exact
	return result, nil
}

// queryVector는 캐시를 먼저 보고, 없으면 워커에 한 번 물어봅니다.
// 캐시에는 벡터만 있고 해시는 없으므로, 캐시가 맞으면 해시 재정렬은 건너뜁니다.
func (s *Search) queryVector(ctx context.Context, sha string, model domain.EmbeddingModel, image []byte) ([]float32, string, domain.Device, error) {
	if s.cache != nil {
		if cached, ok, err := s.cache.CachedQuery(ctx, sha, model.ID); err != nil {
			s.log.Warn("질의 캐시 조회 실패", slog.String("error", err.Error()))
		} else if ok {
			return cached, "", domain.DeviceUnknown, nil
		}
	}

	result, err := s.embedder.EmbedQuery(ctx, image)
	if err != nil {
		return nil, "", domain.DeviceUnknown, fmt.Errorf("질의 이미지 임베딩에 실패했습니다: %w", err)
	}
	vector, ok := result.Vector(model.ID)
	if !ok {
		return nil, "", domain.DeviceUnknown,
			fmt.Errorf("%w: 워커가 모델 %s의 벡터를 주지 않았습니다", domain.ErrModelMismatch, model.ID)
	}
	if len(vector) != model.VectorSize {
		return nil, "", domain.DeviceUnknown,
			fmt.Errorf("%w: 질의 벡터 차원이 %d입니다. 기준은 %d입니다",
				domain.ErrModelMismatch, len(vector), model.VectorSize)
	}

	if s.cache != nil {
		if err := s.cache.SaveQuery(ctx, sha, model.ID, vector); err != nil {
			s.log.Warn("질의 캐시 저장 실패", slog.String("error", err.Error()))
		}
	}
	return vector, result.Hashes.PHash, domain.DeviceUnknown, nil
}

// primaryModel은 원본 찾기용 모델을 고릅니다.
// 복제본 탐지용이 있으면 그것을, 없으면 의미 검색용을 씁니다.
func (s *Search) primaryModel() (domain.EmbeddingModel, bool) {
	for _, m := range s.models {
		if m.Kind == domain.ModelCopy {
			return m, true
		}
	}
	if len(s.models) > 0 {
		return s.models[0], true
	}
	return domain.EmbeddingModel{}, false
}

// rerank는 해시가 사실상 같은 후보를 맨 앞으로 올립니다.
// 같은 등급 안에서는 해시 거리, 그다음 벡터 점수 순입니다.
func rerank(hits []Hit) {
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.Exact != b.Exact {
			return a.Exact
		}
		if a.Exact && b.Exact && a.HashDistance != b.HashDistance {
			return a.HashDistance < b.HashDistance
		}
		return a.Score > b.Score
	})
}
