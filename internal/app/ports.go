// Package app은 유스케이스와 그것이 바깥에 요구하는 계약을 담습니다.
// 구현은 internal/adapter 아래에 있고 의존 방향은 언제나 안쪽을 향합니다.
package app

import (
	"context"
	"time"

	"saucedust/internal/domain"
)

// 이 파일은 유스케이스가 바깥 세계에 요구하는 계약만 담습니다.
// 구현은 internal/adapter 아래에 있고, 의존 방향은 언제나 안쪽을 향합니다.

type NodeRepository interface {
	RegisterNode(ctx context.Context, n domain.Node) error
	Heartbeat(ctx context.Context, nodeID string, netMode domain.NetMode, concurrency int) error
	MarkNodeStopped(ctx context.Context, nodeID string) error
	ListNodes(ctx context.Context, timeout time.Duration) ([]domain.Node, error)
	ReclaimOwnLeases(ctx context.Context, nodeID string) (int64, error)
	ReclaimDeadNodeLeases(ctx context.Context, timeout time.Duration) (int64, error)
	RecordMetrics(ctx context.Context, m domain.NodeMetrics) error
	PruneMetrics(ctx context.Context, keep time.Duration) error
}

type ModelRepository interface {
	UpsertModel(ctx context.Context, m domain.EmbeddingModel) error
	ActiveModels(ctx context.Context) ([]domain.EmbeddingModel, error)
	VerifyNodeModels(ctx context.Context, nodeID string, reported []domain.EmbeddingModel) error
}

type LeaseRepository interface {
	AcquireBackfillRange(ctx context.Context, req domain.LeaseRequest) (*domain.CrawlRange, error)
	FinishRange(ctx context.Context, r *domain.CrawlRange, status domain.RangeStatus, saved int, cause error) error
	InitCatchupState(ctx context.Context, site, scope string, latestID int64) error
	HighWatermark(ctx context.Context, site, scope string) (int64, error)
	AdvanceWatermark(ctx context.Context, site, scope string, id int64) error
	EnqueueRetries(ctx context.Context, site, scope string, postIDs []int64, delay time.Duration) error
	LeaseRetries(ctx context.Context, site, scope, nodeID string, limit int, floor int64) ([]domain.PostRetry, error)
	// ReleaseRange와 ReleaseRetry는 시도 횟수를 쓰지 않고 되돌립니다.
	// 색인이 차서 못 넣은 것은 그 구간의 잘못이 아니므로, 실패로 적어
	// 시도 횟수를 깎으면 나중에 자리가 생겨도 다시 잡히지 않습니다.
	// RenewRange는 아직 일하는 중이라고 알려 임대가 만료되지 않게 합니다.
	RenewRange(ctx context.Context, r *domain.CrawlRange) error
	ReleaseRange(ctx context.Context, r *domain.CrawlRange) error
	ReleaseRetry(ctx context.Context, item domain.PostRetry) error
	FinishRetry(ctx context.Context, item domain.PostRetry, status domain.RetryStatus, cause error) error
	RescheduleRetry(ctx context.Context, item domain.PostRetry, delay time.Duration, cause error) error
}

type ImageRepository interface {
	UpsertImage(ctx context.Context, img *domain.Image) (int64, error)
	// UpsertImages는 여러 건을 한 번의 왕복으로 저장하고 부여된 id를 순서대로 돌려줍니다.
	// 건마다 왕복하면 128건에 128번이 되므로 적재 경로는 이쪽을 씁니다.
	UpsertImages(ctx context.Context, imgs []*domain.Image) ([]int64, error)
	ExistingPostIDs(ctx context.Context, site string, postIDs []int64, modelIDs []string) (map[int64]int64, error)
	ImageByID(ctx context.Context, id int64) (*domain.Image, error)
	ImageBySource(ctx context.Context, site string, postID int64) (*domain.Image, error)
	ImagesByIDs(ctx context.Context, ids []int64) (map[int64]domain.Image, error)
	CountImages(ctx context.Context) (int64, error)
	// ThumbsMissingVector는 축소본은 있는데 이 모델의 벡터가 없는 이미지를
	// 커서 방식으로 냅니다. 모델을 바꿨을 때 다시 계산할 대상입니다.
	//
	// 두 번째 반환값은 다음에 넘길 자리입니다. 0이면 끝입니다.
	// 첫 번째가 비어 있어도 0이 아니면 계속 넘겨야 합니다. 그 묶음이 전부
	// 이미 계산된 것이었을 뿐 뒤에 남아 있을 수 있습니다.
	ThumbsMissingVector(ctx context.Context, modelID string, afterID int64, limit int) ([]domain.ThumbRef, int64, error)
	CountThumbsMissingVector(ctx context.Context, modelID string) (int64, error)
}

type VectorRepository interface {
	SaveVectors(ctx context.Context, imageID int64, vectors []domain.Vector) error
	// SaveVectorBatch는 여러 이미지의 벡터를 한 번의 왕복으로 저장합니다.
	SaveVectorBatch(ctx context.Context, items []domain.StoredVector) error
	MarkVectorsIndexed(ctx context.Context, imageID int64, modelIDs []string) error
	// MarkIndexedBatch는 여러 이미지의 색인 완료를 한 번에 표시합니다.
	MarkIndexedBatch(ctx context.Context, imageIDs []int64, modelIDs []string) error
	PendingVectors(ctx context.Context, modelID string, limit int) ([]domain.StoredVector, error)
	VectorsMissing(ctx context.Context, imageIDs []int64, modelID string) ([]int64, error)
}

// QueryCacheRepository는 같은 이미지를 다시 검색할 때 워커를 거치지 않게 합니다.
//
// 벡터와 지각 해시를 함께 담습니다. 해시를 빼면 캐시가 맞았을 때 재정렬을
// 못 해서, 같은 이미지인데 두 번째 검색부터 답이 달라집니다.
type QueryCacheRepository interface {
	CachedQuery(ctx context.Context, sha, modelID string) (vector []float32, phash string, ok bool, err error)
	SaveQuery(ctx context.Context, sha, modelID string, vector []float32, phash string) error
}

// VectorIndex는 검색용 벡터 저장소입니다. Qdrant가 기본 구현입니다.
type VectorIndex interface {
	EnsureCollection(ctx context.Context, m domain.EmbeddingModel) error
	Upsert(ctx context.Context, collection string, points []domain.VectorPoint) error
	Search(ctx context.Context, collection string, vector []float32, limit int) ([]domain.VectorMatch, error)
	Count(ctx context.Context, collection string) (int64, error)
	Ping(ctx context.Context) error
}

// SourceClient는 수집 대상 사이트입니다. 지금은 Danbooru 하나입니다.
type SourceClient interface {
	Site() string
	LatestPostID(ctx context.Context) (int64, error)
	PostsInRange(ctx context.Context, lowerID, upperID int64, tags string) ([]domain.SourcePost, error)
	PostsAfter(ctx context.Context, afterID int64, tags string, limit int) ([]domain.SourcePost, error)
	PostByID(ctx context.Context, postID int64) (*domain.SourcePost, error)
	Download(ctx context.Context, url string, maxBytes int64) ([]byte, error)
}

// Embedder는 이미지 바이트를 받아 등록된 모델별 벡터와 해시를 돌려줍니다.
// 해시를 함께 받는 이유는 워커가 이미 디코드한 이미지를 재활용하기 위해서입니다.
type Embedder interface {
	Describe(ctx context.Context) (domain.EmbedderInfo, error)
	// Embed는 인덱싱용입니다. 보관할 축소본까지 함께 받습니다.
	Embed(ctx context.Context, image []byte) (domain.EmbedResult, error)
	// EmbedQuery는 검색 질의용입니다. 질의 이미지는 보관하지 않으므로
	// 축소본을 만들지 않습니다. 그만큼 응답이 빨라집니다.
	EmbedQuery(ctx context.Context, image []byte) (domain.EmbedResult, error)
	Health(ctx context.Context) error
}

// ThumbStore는 재계산용 축소본을 보관합니다.
// 경로가 출처로 정해지므로 저장 전에 알 수 있습니다.
type ThumbStore interface {
	Put(ctx context.Context, site string, postID int64, jpeg []byte) (string, error)
	Get(ctx context.Context, path string) ([]byte, error)
	Has(ctx context.Context, path string) bool
}

// VectorSink는 작업 노드가 결과를 내보내는 곳입니다.
// control 노드에서는 저장소에 바로 쓰고, worker 노드에서는 중앙 API로 보냅니다.
type VectorSink interface {
	Submit(ctx context.Context, batch []domain.IndexedImage) error
}

type Clock interface {
	Now() time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

var SystemClock Clock = systemClock{}
