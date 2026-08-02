package integration

import (
	"context"
	"fmt"
	"testing"

	"saucedust/internal/domain"
)

func benchBatch(n int, vectorSize int) []domain.IndexedImage {
	out := make([]domain.IndexedImage, 0, n)
	for i := 0; i < n; i++ {
		values := make([]float32, vectorSize)
		for j := range values {
			values[j] = float32(i+j) / float32(vectorSize)
		}
		domain.Normalize(values)

		out = append(out, domain.IndexedImage{
			Image: domain.Image{
				SourceSite: "danbooru", SourcePostID: int64(1_000_000 + i),
				CanonicalURL: fmt.Sprintf("https://danbooru.example/posts/%d", i),
				PHash:        "f0f0f0f0f0f0f0f0", DHash: "0f0f0f0f0f0f0f0f",
				Rating: "g", Width: 1000, Height: 1400,
				Tags:       []string{"1girl", "solo", "blue_archive"},
				ArtistTags: []string{"someone"},
			},
			Vectors: []domain.Vector{
				{ModelID: "copy-model", Values: values},
				{ModelID: "semantic-model", Values: append([]float32(nil), values...)},
			},
			Thumb: make([]byte, 20_000),
		})
	}
	return out
}

// 적재 경로가 배치 하나에 왕복을 몇 번 하는지, 얼마나 걸리는지 잽니다.
func BenchmarkIngestSubmit(b *testing.B) {
	for _, size := range []int{8, 32, 128} {
		b.Run(fmt.Sprintf("batch-%d", size), func(b *testing.B) {
			s := newBenchStack(b)
			ctx := context.Background()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				batch := benchBatch(size, 8)
				for j := range batch {
					batch[j].Image.SourcePostID = int64(i*size + j + 1)
				}
				b.StartTimer()

				if err := s.ingest.Submit(ctx, batch); err != nil {
					b.Fatalf("적재 실패: %v", err)
				}
			}
			b.ReportMetric(float64(size*b.N)/b.Elapsed().Seconds(), "이미지/초")
		})
	}
}

func newBenchStack(b *testing.B) *stack {
	b.Helper()
	return newStack(b, 100)
}
