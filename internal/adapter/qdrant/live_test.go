package qdrant

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"testing"
	"time"

	"saucedust/internal/domain"
)

// 이 시험은 실제 Qdrant에 붙습니다. 가짜 서버로는 프로토콜이 실제로 맞는지
// 알 수 없습니다. 검색은 이 시스템의 절반이므로 실물로 한 번은 확인해야 합니다.
//
//	docker run -d -p 6333:6333 qdrant/qdrant
//	SAUCEDUST_TEST_QDRANT_URL=http://localhost:6333 go test ./internal/adapter/qdrant/
func liveClient(t *testing.T, quantize bool) (*Client, string) {
	t.Helper()

	url := os.Getenv("SAUCEDUST_TEST_QDRANT_URL")
	if url == "" {
		t.Skip("SAUCEDUST_TEST_QDRANT_URL이 없어 건너뜁니다")
	}

	client, err := New(Options{BaseURL: url, Quantize: quantize, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}

	ctx := context.Background()
	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Qdrant에 닿지 못했습니다: %v", err)
	}

	collection := fmt.Sprintf("it_%d_%d", time.Now().UnixNano(), rand.Int31())
	t.Cleanup(func() {
		if err := client.DeleteCollection(context.Background(), collection); err != nil {
			t.Logf("컬렉션 정리 실패: %v", err)
		}
	})
	return client, collection
}

func liveModel(collection string, size int) domain.EmbeddingModel {
	return domain.EmbeddingModel{
		ID: "live-model", Kind: domain.ModelCopy, VectorSize: size,
		Distance: "cosine", Collection: collection, InputSize: 224, Active: true,
	}
}

// 특정 방향을 가리키는 단위 벡터를 만듭니다. 검색 결과를 예측할 수 있게 합니다.
func unitVector(size, axis int) []float32 {
	out := make([]float32, size)
	out[axis%size] = 1
	return out
}

func tiltedVector(size, axis int, tilt float64) []float32 {
	out := make([]float32, size)
	out[axis%size] = float32(math.Cos(tilt))
	out[(axis+1)%size] = float32(math.Sin(tilt))
	domain.Normalize(out)
	return out
}

func TestLiveEnsureCollectionCreatesWithQuantization(t *testing.T) {
	client, collection := liveClient(t, true)
	ctx := context.Background()
	model := liveModel(collection, 64)

	if err := client.EnsureCollection(ctx, model); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}

	// 두 번 불러도 문제가 없어야 합니다. 노드마다 시작할 때 부릅니다.
	if err := client.EnsureCollection(ctx, model); err != nil {
		t.Fatalf("두 번째 호출 실패: %v", err)
	}

	n, err := client.Count(ctx, collection)
	if err != nil {
		t.Fatalf("개수 조회 실패: %v", err)
	}
	if n != 0 {
		t.Fatalf("새 컬렉션에 %d개가 있습니다", n)
	}
}

// 차원이 다른 컬렉션을 그대로 쓰면 검색이 조용히 망가집니다.
func TestLiveRejectsDimensionMismatch(t *testing.T) {
	client, collection := liveClient(t, true)
	ctx := context.Background()

	if err := client.EnsureCollection(ctx, liveModel(collection, 64)); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}

	err := client.EnsureCollection(ctx, liveModel(collection, 128))
	if !errors.Is(err, domain.ErrModelMismatch) {
		t.Fatalf("차원 불일치를 잡아야 하는데 결과가 %v입니다", err)
	}
}

// 저장한 벡터를 실제로 다시 찾아야 합니다. 이것이 검색의 전부입니다.
func TestLiveUpsertAndSearch(t *testing.T) {
	client, collection := liveClient(t, false)
	ctx := context.Background()

	const size = 64
	if err := client.EnsureCollection(ctx, liveModel(collection, size)); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}

	points := make([]domain.VectorPoint, 0, 20)
	for i := 0; i < 20; i++ {
		points = append(points, domain.VectorPoint{
			ImageID: int64(1000 + i),
			Vector:  unitVector(size, i),
			Payload: map[string]any{
				"image_id":       int64(1000 + i),
				"source_site":    "danbooru",
				"source_post_id": int64(9000 + i),
				"rating":         "g",
				"phash":          fmt.Sprintf("%016x", i),
			},
		})
	}
	if err := client.Upsert(ctx, collection, points); err != nil {
		t.Fatalf("저장 실패: %v", err)
	}

	n, err := client.Count(ctx, collection)
	if err != nil {
		t.Fatalf("개수 조회 실패: %v", err)
	}
	if n != 20 {
		t.Fatalf("저장된 점이 %d개입니다. 20개를 기대했습니다", n)
	}

	// 7번 축과 같은 방향으로 찾으면 1007이 1등이어야 합니다.
	hits, err := client.Search(ctx, collection, unitVector(size, 7), 5)
	if err != nil {
		t.Fatalf("검색 실패: %v", err)
	}
	if len(hits) != 5 {
		t.Fatalf("결과가 %d건입니다", len(hits))
	}
	if hits[0].ImageID != 1007 {
		t.Fatalf("1등이 %d입니다. 1007을 기대했습니다", hits[0].ImageID)
	}
	if hits[0].Score < 0.99 {
		t.Fatalf("같은 벡터인데 점수가 %f입니다", hits[0].Score)
	}
	// 코사인 거리이므로 직교하는 나머지는 0에 가까워야 합니다.
	if hits[1].Score > 0.1 {
		t.Fatalf("직교 벡터 점수가 %f입니다", hits[1].Score)
	}
}

// 조금 기울어진 벡터도 가장 가까운 것부터 나와야 합니다.
func TestLiveSearchOrdersByCloseness(t *testing.T) {
	client, collection := liveClient(t, false)
	ctx := context.Background()

	const size = 32
	if err := client.EnsureCollection(ctx, liveModel(collection, size)); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}

	// 0번 축에서 조금씩 벌어지는 벡터들을 넣습니다.
	var points []domain.VectorPoint
	for i, tilt := range []float64{0.05, 0.2, 0.5, 1.0, 1.5} {
		points = append(points, domain.VectorPoint{
			ImageID: int64(i + 1),
			Vector:  tiltedVector(size, 0, tilt),
			Payload: map[string]any{"image_id": int64(i + 1)},
		})
	}
	if err := client.Upsert(ctx, collection, points); err != nil {
		t.Fatalf("저장 실패: %v", err)
	}

	hits, err := client.Search(ctx, collection, unitVector(size, 0), 5)
	if err != nil {
		t.Fatalf("검색 실패: %v", err)
	}
	for i := range hits {
		if hits[i].ImageID != int64(i+1) {
			t.Fatalf("%d등이 %d입니다. %d를 기대했습니다", i+1, hits[i].ImageID, i+1)
		}
		if i > 0 && hits[i].Score > hits[i-1].Score {
			t.Fatalf("점수가 오름차순입니다: %v", hits)
		}
	}
}

// int8 압축을 켜도 검색 결과가 유지돼야 합니다.
// 용량을 4분의 1로 줄이는 대신 정확도를 잃으면 안 됩니다.
func TestLiveQuantizedSearchKeepsOrder(t *testing.T) {
	client, collection := liveClient(t, true)
	ctx := context.Background()

	const size = 128
	if err := client.EnsureCollection(ctx, liveModel(collection, size)); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}

	var points []domain.VectorPoint
	for i := 0; i < 50; i++ {
		points = append(points, domain.VectorPoint{
			ImageID: int64(i + 1),
			Vector:  tiltedVector(size, 0, float64(i)*0.03),
			Payload: map[string]any{"image_id": int64(i + 1)},
		})
	}
	if err := client.Upsert(ctx, collection, points); err != nil {
		t.Fatalf("저장 실패: %v", err)
	}

	hits, err := client.Search(ctx, collection, unitVector(size, 0), 5)
	if err != nil {
		t.Fatalf("검색 실패: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("결과가 없습니다")
	}
	if hits[0].ImageID != 1 {
		t.Fatalf("압축을 켜니 1등이 %d입니다. 1을 기대했습니다", hits[0].ImageID)
	}
}

// 같은 이미지를 다시 저장하면 덮어써야 합니다. 재처리가 흔합니다.
func TestLiveUpsertIsIdempotent(t *testing.T) {
	client, collection := liveClient(t, false)
	ctx := context.Background()

	const size = 16
	if err := client.EnsureCollection(ctx, liveModel(collection, size)); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}

	point := domain.VectorPoint{
		ImageID: 42, Vector: unitVector(size, 0),
		Payload: map[string]any{"image_id": int64(42), "rating": "g"},
	}
	for i := 0; i < 3; i++ {
		if err := client.Upsert(ctx, collection, []domain.VectorPoint{point}); err != nil {
			t.Fatalf("%d번째 저장 실패: %v", i, err)
		}
	}

	n, err := client.Count(ctx, collection)
	if err != nil {
		t.Fatalf("개수 조회 실패: %v", err)
	}
	if n != 1 {
		t.Fatalf("점이 %d개입니다. 1개여야 합니다", n)
	}

	// 벡터를 바꿔 저장하면 새 값으로 찾혀야 합니다.
	point.Vector = unitVector(size, 5)
	if err := client.Upsert(ctx, collection, []domain.VectorPoint{point}); err != nil {
		t.Fatalf("갱신 실패: %v", err)
	}
	hits, err := client.Search(ctx, collection, unitVector(size, 5), 1)
	if err != nil {
		t.Fatalf("검색 실패: %v", err)
	}
	if len(hits) != 1 || hits[0].Score < 0.99 {
		t.Fatalf("갱신된 벡터를 찾지 못했습니다: %+v", hits)
	}
}

// 실제 적재 규모에 가까운 묶음도 한 번에 들어가야 합니다.
func TestLiveUpsertLargeBatch(t *testing.T) {
	client, collection := liveClient(t, true)
	ctx := context.Background()

	const (
		size  = 768
		count = 500
	)
	if err := client.EnsureCollection(ctx, liveModel(collection, size)); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}

	rng := rand.New(rand.NewSource(7))
	points := make([]domain.VectorPoint, 0, count)
	for i := 0; i < count; i++ {
		values := make([]float32, size)
		for j := range values {
			values[j] = rng.Float32()*2 - 1
		}
		domain.Normalize(values)
		points = append(points, domain.VectorPoint{
			ImageID: int64(i + 1), Vector: values,
			Payload: map[string]any{
				"image_id":       int64(i + 1),
				"source_site":    "danbooru",
				"source_post_id": int64(100000 + i),
				"canonical_url":  fmt.Sprintf("https://danbooru.example/posts/%d", i),
				"rating":         "g",
				"phash":          fmt.Sprintf("%016x", i),
				"dhash":          fmt.Sprintf("%016x", i*3),
			},
		})
	}

	started := time.Now()
	if err := client.Upsert(ctx, collection, points); err != nil {
		t.Fatalf("대량 저장 실패: %v", err)
	}
	t.Logf("768차원 %d건 저장에 %v", count, time.Since(started).Truncate(time.Millisecond))

	n, err := client.Count(ctx, collection)
	if err != nil {
		t.Fatalf("개수 조회 실패: %v", err)
	}
	if n != count {
		t.Fatalf("저장된 점이 %d개입니다. %d개를 기대했습니다", n, count)
	}

	// 넣은 벡터 중 하나로 찾으면 자기 자신이 1등이어야 합니다.
	hits, err := client.Search(ctx, collection, points[123].Vector, 5)
	if err != nil {
		t.Fatalf("검색 실패: %v", err)
	}
	if len(hits) == 0 || hits[0].ImageID != 124 {
		t.Fatalf("자기 자신을 못 찾았습니다: %+v", hits)
	}
}

func TestLiveSearchOnEmptyCollection(t *testing.T) {
	client, collection := liveClient(t, false)
	ctx := context.Background()

	if err := client.EnsureCollection(ctx, liveModel(collection, 16)); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}

	hits, err := client.Search(ctx, collection, unitVector(16, 0), 5)
	if err != nil {
		t.Fatalf("빈 컬렉션 검색이 실패했습니다: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("빈 컬렉션에서 %d건이 나왔습니다", len(hits))
	}
}
