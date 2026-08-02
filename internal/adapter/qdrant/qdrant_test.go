package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"saucedust/internal/domain"
)

type fakeQdrant struct {
	collections map[string]int
	created     []map[string]any
	upserted    []map[string]any
}

func newFake() *fakeQdrant {
	return &fakeQdrant{collections: map[string]int{}}
}

func (f *fakeQdrant) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	switch {
	case strings.HasSuffix(path, "/exists"):
		name := strings.TrimSuffix(strings.TrimPrefix(path, "/collections/"), "/exists")
		_, ok := f.collections[name]
		writeJSON(w, map[string]any{"result": map[string]any{"exists": ok}})

	case strings.HasSuffix(path, "/points/query"):
		writeJSON(w, map[string]any{"result": map[string]any{"points": []map[string]any{
			{"id": 42, "score": 0.97},
			{"id": 7, "score": 0.81},
		}}})

	case strings.HasSuffix(path, "/points/count"):
		writeJSON(w, map[string]any{"result": map[string]any{"count": 1234}})

	case strings.HasSuffix(path, "/points") && r.Method == http.MethodPut:
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.upserted = append(f.upserted, body)
		writeJSON(w, map[string]any{"status": "ok"})

	case r.Method == http.MethodPut:
		name := strings.TrimPrefix(path, "/collections/")
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.created = append(f.created, body)
		size := 0
		if vectors, ok := body["vectors"].(map[string]any); ok {
			if v, ok := vectors["size"].(float64); ok {
				size = int(v)
			}
		}
		f.collections[name] = size
		writeJSON(w, map[string]any{"status": "ok"})

	case r.Method == http.MethodGet && strings.HasPrefix(path, "/collections/"):
		name := strings.TrimPrefix(path, "/collections/")
		size, ok := f.collections[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"result": map[string]any{
			"config": map[string]any{"params": map[string]any{
				"vectors": map[string]any{"size": size, "distance": "Cosine"},
			}},
		}})

	default:
		writeJSON(w, map[string]any{"result": []any{}})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("content-type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func newClient(t *testing.T, fake *fakeQdrant, quantize bool) *Client {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	c, err := New(Options{BaseURL: server.URL, Quantize: quantize})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}
	return c
}

var testModel = domain.EmbeddingModel{
	ID: "dinov2-vitb14", Kind: domain.ModelCopy,
	VectorSize: 768, Distance: "cosine", Collection: "dinov2",
}

func TestEnsureCollectionCreatesWithQuantization(t *testing.T) {
	fake := newFake()
	client := newClient(t, fake, true)

	if err := client.EnsureCollection(context.Background(), testModel); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}
	if len(fake.created) != 1 {
		t.Fatalf("생성 요청이 %d건입니다", len(fake.created))
	}

	body := fake.created[0]
	if _, ok := body["quantization_config"]; !ok {
		t.Fatal("압축 설정이 빠졌습니다")
	}
	vectors := body["vectors"].(map[string]any)
	if int(vectors["size"].(float64)) != 768 {
		t.Fatalf("차원이 %v입니다", vectors["size"])
	}
	if vectors["distance"] != "Cosine" {
		t.Fatalf("거리 방식이 %v입니다", vectors["distance"])
	}
}

func TestEnsureCollectionSkipsQuantizationWhenOff(t *testing.T) {
	fake := newFake()
	client := newClient(t, fake, false)

	if err := client.EnsureCollection(context.Background(), testModel); err != nil {
		t.Fatalf("컬렉션 생성 실패: %v", err)
	}
	if _, ok := fake.created[0]["quantization_config"]; ok {
		t.Fatal("압축을 끄면 설정이 없어야 합니다")
	}
}

func TestEnsureCollectionIsIdempotent(t *testing.T) {
	fake := newFake()
	client := newClient(t, fake, true)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := client.EnsureCollection(ctx, testModel); err != nil {
			t.Fatalf("%d번째 호출 실패: %v", i, err)
		}
	}
	if len(fake.created) != 1 {
		t.Fatalf("생성 요청이 %d건입니다. 1건만 나가야 합니다", len(fake.created))
	}
}

// 차원이 다른 컬렉션을 그대로 쓰면 검색이 조용히 망가집니다. 시작을 막아야 합니다.
func TestEnsureCollectionRejectsDimensionMismatch(t *testing.T) {
	fake := newFake()
	fake.collections["dinov2"] = 512

	client := newClient(t, fake, true)
	err := client.EnsureCollection(context.Background(), testModel)
	if !errors.Is(err, domain.ErrModelMismatch) {
		t.Fatalf("차원 불일치를 잡아야 하는데 결과가 %v입니다", err)
	}
}

func TestUpsertSendsPoints(t *testing.T) {
	fake := newFake()
	client := newClient(t, fake, true)

	points := []domain.VectorPoint{
		{ImageID: 1, Vector: []float32{0.1, 0.2}, Payload: map[string]any{"rating": "g"}},
		{ImageID: 2, Vector: []float32{0.3, 0.4}},
	}
	if err := client.Upsert(context.Background(), "dinov2", points); err != nil {
		t.Fatalf("저장 실패: %v", err)
	}
	if len(fake.upserted) != 1 {
		t.Fatalf("요청이 %d건입니다", len(fake.upserted))
	}
	got := fake.upserted[0]["points"].([]any)
	if len(got) != 2 {
		t.Fatalf("점이 %d개입니다", len(got))
	}
}

func TestUpsertRejectsEmptyVector(t *testing.T) {
	client := newClient(t, newFake(), true)
	err := client.Upsert(context.Background(), "dinov2",
		[]domain.VectorPoint{{ImageID: 1}})
	if err == nil {
		t.Fatal("빈 벡터는 거부해야 합니다")
	}
}

func TestUpsertEmptyIsNoop(t *testing.T) {
	fake := newFake()
	client := newClient(t, fake, true)

	if err := client.Upsert(context.Background(), "dinov2", nil); err != nil {
		t.Fatalf("빈 목록은 그냥 넘어가야 합니다: %v", err)
	}
	if len(fake.upserted) != 0 {
		t.Fatal("빈 목록에 요청을 보내면 안 됩니다")
	}
}

func TestSearchParsesResults(t *testing.T) {
	client := newClient(t, newFake(), true)

	hits, err := client.Search(context.Background(), "dinov2", []float32{0.1, 0.2}, 5)
	if err != nil {
		t.Fatalf("검색 실패: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("결과가 %d건입니다", len(hits))
	}
	if hits[0].ImageID != 42 || hits[0].Score < 0.96 {
		t.Fatalf("첫 결과가 %+v입니다", hits[0])
	}
}

func TestSearchRejectsEmptyVector(t *testing.T) {
	client := newClient(t, newFake(), true)
	if _, err := client.Search(context.Background(), "dinov2", nil, 5); err == nil {
		t.Fatal("빈 벡터 검색은 거부해야 합니다")
	}
}

func TestCount(t *testing.T) {
	client := newClient(t, newFake(), true)

	n, err := client.Count(context.Background(), "dinov2")
	if err != nil {
		t.Fatalf("개수 조회 실패: %v", err)
	}
	if n != 1234 {
		t.Fatalf("개수가 %d입니다", n)
	}
}

func TestCallSurfacesServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"status":{"error":"내부 오류"}}`))
	}))
	defer server.Close()

	client, _ := New(Options{BaseURL: server.URL})
	if err := client.Ping(context.Background()); err == nil {
		t.Fatal("500 응답은 오류로 올라와야 합니다")
	}
}

func TestAPIKeyHeader(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("api-key")
		writeJSON(w, map[string]any{"result": []any{}})
	}))
	defer server.Close()

	client, _ := New(Options{BaseURL: server.URL, APIKey: "secret-token"})
	if err := client.Ping(context.Background()); err != nil {
		t.Fatalf("호출 실패: %v", err)
	}
	if seen != "secret-token" {
		t.Fatalf("전달된 키가 %q입니다", seen)
	}
}

func TestDistanceName(t *testing.T) {
	cases := map[string]string{
		"": "Cosine", "cosine": "Cosine", "COSINE": "Cosine",
		"dot": "Dot", "euclid": "Euclid", "l2": "Euclid", "몰라요": "Cosine",
	}
	for in, want := range cases {
		if got := distanceName(in); got != want {
			t.Errorf("%q에서 %q를 얻었습니다. %q를 기대했습니다", in, got, want)
		}
	}
}
