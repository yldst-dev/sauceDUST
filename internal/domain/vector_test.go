package domain

import (
	"math"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	// 이 값들이 그대로 돌아와야 Qdrant가 사라져도 PostgreSQL만으로 다시 세울 수 있습니다.
	want := []float32{0, 1, -1, 0.5, -0.5, 3.4028235e38, 1.1754944e-38}

	got, err := DecodeVector(EncodeVector(want))
	if err != nil {
		t.Fatalf("복원에 실패했습니다: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("길이가 %d입니다. %d를 기대했습니다", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%d번째가 %v입니다. %v를 기대했습니다", i, got[i], want[i])
		}
	}
}

func TestEncodeVectorIsLittleEndian(t *testing.T) {
	// 바이트 순서가 바뀌면 이미 저장한 벡터를 전부 다시 계산해야 합니다.
	// 값 하나를 바이트까지 못 박아 둡니다.
	got := EncodeVector([]float32{1})
	want := []byte{0x00, 0x00, 0x80, 0x3f}

	if len(got) != len(want) {
		t.Fatalf("%d바이트입니다. %d바이트를 기대했습니다", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("바이트가 %x입니다. %x를 기대했습니다", got, want)
		}
	}
}

func TestEncodeVectorEmpty(t *testing.T) {
	if got := EncodeVector(nil); len(got) != 0 {
		t.Errorf("빈 벡터가 %d바이트가 되었습니다", len(got))
	}
	got, err := DecodeVector(nil)
	if err != nil {
		t.Fatalf("빈 바이트에서 오류가 났습니다: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("빈 바이트가 %d개 값이 되었습니다", len(got))
	}
}

func TestDecodeVectorRejectsRagged(t *testing.T) {
	// 4의 배수가 아니면 중간이 잘린 것입니다. 남은 부분만 읽어 넘기면
	// 차원이 맞지 않는 벡터가 조용히 저장됩니다.
	for _, n := range []int{1, 2, 3, 5, 7} {
		if _, err := DecodeVector(make([]byte, n)); err == nil {
			t.Errorf("%d바이트를 받아들였습니다", n)
		}
	}
}

func TestNormalizeMakesUnitLength(t *testing.T) {
	values := []float32{3, 4}
	Normalize(values)

	var sum float64
	for _, v := range values {
		sum += float64(v) * float64(v)
	}
	if math.Abs(sum-1) > 1e-6 {
		t.Errorf("길이의 제곱이 %v입니다. 1을 기대했습니다", sum)
	}
	if math.Abs(float64(values[0])-0.6) > 1e-6 || math.Abs(float64(values[1])-0.8) > 1e-6 {
		t.Errorf("정규화 결과가 %v입니다. [0.6 0.8]을 기대했습니다", values)
	}
}

func TestNormalizeLeavesZeroVectorAlone(t *testing.T) {
	// 0으로 나누면 NaN이 됩니다. 그대로 두는 편이 낫습니다.
	values := []float32{0, 0, 0}
	Normalize(values)
	for i, v := range values {
		if v != 0 {
			t.Errorf("%d번째가 %v로 바뀌었습니다", i, v)
		}
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	values := []float32{0.1, 0.2, 0.3, 0.4}
	Normalize(values)
	first := append([]float32(nil), values...)
	Normalize(values)

	for i := range first {
		if math.Abs(float64(first[i]-values[i])) > 1e-6 {
			t.Errorf("두 번 정규화하니 %d번째가 %v에서 %v로 바뀌었습니다",
				i, first[i], values[i])
		}
	}
}

func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float32
	}{
		{"같은 벡터", []float32{1, 2, 3}, []float32{1, 2, 3}, 1},
		{"크기만 다른 벡터", []float32{1, 2, 3}, []float32{2, 4, 6}, 1},
		{"직교", []float32{1, 0}, []float32{0, 1}, 0},
		{"반대", []float32{1, 0}, []float32{-1, 0}, -1},
		{"영벡터", []float32{0, 0}, []float32{1, 1}, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CosineSimilarity(tc.a, tc.b)
			if err != nil {
				t.Fatalf("오류가 났습니다: %v", err)
			}
			if math.Abs(float64(got-tc.want)) > 1e-6 {
				t.Errorf("%v입니다. %v를 기대했습니다", got, tc.want)
			}
		})
	}
}

func TestCosineSimilarityRejectsMismatch(t *testing.T) {
	if _, err := CosineSimilarity([]float32{1, 2}, []float32{1, 2, 3}); err == nil {
		t.Error("차원이 다른 벡터를 받아들였습니다")
	}
}
