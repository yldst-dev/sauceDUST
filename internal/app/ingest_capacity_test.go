package app

import (
	"context"
	"errors"
	"testing"

	"saucedust/internal/domain"
)

// 수집하는 쪽 검사만으로는 부족합니다.
//
// 그쪽은 구간을 받기 전에 한 번 볼 뿐이라 구간 하나를 처리하는 동안
// 넘길 수 있고, 설정을 빠뜨린 노드나 API를 직접 부르는 쪽은 아예
// 검사하지 않습니다. 자료가 들어오는 문은 여기 하나뿐입니다.
func TestIngestRefusesWhenFull(t *testing.T) {
	repo := &countingRepo{}
	repo.count.Store(100)

	in := newIngestWithLimit(t, 100, repo)

	err := in.Submit(context.Background(), []domain.IndexedImage{sampleIndexed()})
	if !errors.Is(err, domain.ErrIndexFull) {
		t.Fatalf("찬 상태인데 받았습니다: %v", err)
	}
}

func TestIngestAcceptsBelowTheLimit(t *testing.T) {
	repo := &countingRepo{}
	repo.count.Store(10)

	in := newIngestWithLimit(t, 100, repo)

	if err := in.Submit(context.Background(), []domain.IndexedImage{sampleIndexed()}); err != nil {
		t.Fatalf("여유가 있는데 거절했습니다: %v", err)
	}
}

// 세지 못했다고 들어온 결과를 버리면 그 노드가 이미 내려받아 계산한 것이
// 통째로 사라집니다. 저장소가 잠깐 흔들린 것일 수 있습니다.
func TestIngestAcceptsWhenCountFails(t *testing.T) {
	repo := &countingRepo{err: errors.New("연결이 끊겼습니다")}

	in := newIngestWithLimit(t, 1, repo)

	if err := in.Submit(context.Background(), []domain.IndexedImage{sampleIndexed()}); err != nil {
		t.Fatalf("세지 못했다고 버렸습니다: %v", err)
	}
}

// 상한을 두지 않았으면 세지도 말아야 합니다.
func TestIngestWithoutLimitNeverCounts(t *testing.T) {
	repo := &countingRepo{}
	repo.count.Store(1_000_000)

	in := newIngestWithLimit(t, 0, repo)

	if err := in.Submit(context.Background(), []domain.IndexedImage{sampleIndexed()}); err != nil {
		t.Fatalf("상한이 없는데 거절했습니다: %v", err)
	}
	if repo.calls.Load() != 0 {
		t.Errorf("상한이 없는데 %d번 셌습니다", repo.calls.Load())
	}
}

func TestIngestLimitWithoutCounterIsRejected(t *testing.T) {
	h := newHarness(t, 2)
	_, err := NewIngest(IngestDeps{
		Images: h.images, Vector: &reembedStore{}, Index: &fakeIndex{},
		Models: testModels, Log: quietLogger(), MaxIndexed: 100,
	})
	if err == nil {
		t.Error("세는 것 없이 상한만 받았습니다")
	}
}

func newIngestWithLimit(t *testing.T, limit int64, counter IndexCounter) *Ingest {
	t.Helper()
	h := newHarness(t, 2)
	in, err := NewIngest(IngestDeps{
		Images: h.images, Vector: &reembedStore{}, Index: &fakeIndex{},
		Models: testModels, Log: quietLogger(),
		MaxIndexed: limit, Counter: counter,
	})
	if err != nil {
		t.Fatalf("적재기 생성 실패: %v", err)
	}
	return in
}

func sampleIndexed() domain.IndexedImage {
	vectors := make([]domain.Vector, 0, len(testModels))
	for _, m := range testModels {
		values := make([]float32, m.VectorSize)
		values[0] = 1
		vectors = append(vectors, domain.Vector{ModelID: m.ID, Values: values})
	}
	return domain.IndexedImage{
		Image: domain.Image{
			SourceSite: "danbooru", SourcePostID: 1,
			MD5: "abc", PHash: "0000000000000000", DHash: "0000000000000000",
		},
		Vectors: vectors,
	}
}
