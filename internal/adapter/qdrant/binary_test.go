package qdrant

import (
	"context"
	"encoding/json"
	"math"
	"math/rand"
	"net/http"
	"testing"

	"saucedust/internal/domain"
)

// 새 구성으로 만든 컬렉션이 세 가지를 다 갖춰야 합니다.
// 하나라도 빠지면 담을 장수 계산이 실제와 몇 배로 어긋납니다.
func TestNewCollectionUsesTheLowMemoryLayout(t *testing.T) {
	client, collection := liveClient(t, true)
	ctx := context.Background()

	if err := client.EnsureCollection(ctx, liveModel(collection, 768)); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}

	got := layoutOf(t, client, collection)
	if !got.vectorsOnDisk {
		t.Error("원본 벡터가 디스크에 있지 않습니다")
	}
	if !got.hnswOnDisk {
		t.Error("그래프가 디스크에 있지 않습니다. 장당 비용의 대부분입니다")
	}
	if !got.binary {
		t.Error("이진 양자화가 아닙니다")
	}
}

// 예전 구성으로 만든 컬렉션도 옮겨야 합니다.
// 옮기지 않으면 같은 계산을 쓰면서 실제로는 세 배를 씁니다.
func TestOldCollectionMigratesToLowMemoryLayout(t *testing.T) {
	client, collection := liveClient(t, true)
	ctx := context.Background()

	body := map[string]any{
		"vectors": map[string]any{"size": 768, "distance": "Cosine"},
		"quantization_config": map[string]any{
			"scalar": map[string]any{"type": "int8", "always_ram": true},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := rawCall(ctx, client, http.MethodPut, "/collections/"+collection, raw); err != nil {
		t.Fatalf("예전 방식 생성 실패: %v", err)
	}
	if before := layoutOf(t, client, collection); before.binary || before.hnswOnDisk {
		t.Fatal("준비가 잘못됐습니다. 이미 새 구성입니다")
	}

	if err := client.EnsureCollection(ctx, liveModel(collection, 768)); err != nil {
		t.Fatalf("기존 컬렉션 확인 실패: %v", err)
	}

	got := layoutOf(t, client, collection)
	if !got.vectorsOnDisk || !got.hnswOnDisk || !got.binary {
		t.Errorf("옮겨지지 않았습니다: %+v", got)
	}
}

// 이진으로 줄여도 찾아내야 합니다. 재점수를 안 붙이면 여기서 떨어집니다.
func TestBinaryStillFindsTheSameVector(t *testing.T) {
	client, collection := liveClient(t, true)
	ctx := context.Background()

	model := liveModel(collection, 768)
	if err := client.EnsureCollection(ctx, model); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}

	r := rand.New(rand.NewSource(7))
	const n = 500
	points := make([]domain.VectorPoint, 0, n)
	stored := make([][]float32, n)
	for i := 0; i < n; i++ {
		v := make([]float32, 768)
		var sum float64
		for d := range v {
			v[d] = float32(r.NormFloat64())
			sum += float64(v[d]) * float64(v[d])
		}
		inv := float32(1 / math.Sqrt(sum))
		for d := range v {
			v[d] *= inv
		}
		stored[i] = v
		points = append(points, domain.VectorPoint{ImageID: int64(i), Vector: v})
	}
	if err := client.Upsert(ctx, collection, points); err != nil {
		t.Fatalf("적재 실패: %v", err)
	}

	// 살짝 흔든 벡터로 찾습니다. 원본과 정확히 같으면 너무 쉽습니다.
	hit := 0
	for i := 0; i < 100; i++ {
		q := make([]float32, 768)
		copy(q, stored[i])
		for d := 0; d < 40; d++ {
			q[r.Intn(768)] += float32(r.NormFloat64()) * 0.05
		}
		out, err := client.Search(ctx, collection, q, 1)
		if err != nil {
			t.Fatalf("검색 실패: %v", err)
		}
		if len(out) > 0 && out[0].ImageID == int64(i) {
			hit++
		}
	}
	if hit < 95 {
		t.Errorf("100건 중 %d건만 1등으로 찾았습니다. 재점수가 안 붙은 것 같습니다", hit)
	}
}

type layout struct {
	vectorsOnDisk bool
	hnswOnDisk    bool
	binary        bool
}

func layoutOf(t *testing.T, client *Client, collection string) layout {
	t.Helper()
	var p struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors struct {
						OnDisk *bool `json:"on_disk"`
					} `json:"vectors"`
				} `json:"params"`
				HNSW struct {
					OnDisk *bool `json:"on_disk"`
				} `json:"hnsw_config"`
				Quantization *struct {
					Binary *struct{} `json:"binary"`
				} `json:"quantization_config"`
			} `json:"config"`
		} `json:"result"`
	}
	if err := client.call(context.Background(), http.MethodGet, "/collections/"+collection, nil, &p); err != nil {
		t.Fatalf("컬렉션 조회 실패: %v", err)
	}
	c := p.Result.Config
	return layout{
		vectorsOnDisk: c.Params.Vectors.OnDisk != nil && *c.Params.Vectors.OnDisk,
		hnswOnDisk:    c.HNSW.OnDisk != nil && *c.HNSW.OnDisk,
		binary:        c.Quantization != nil && c.Quantization.Binary != nil,
	}
}
