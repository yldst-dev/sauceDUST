package domain

import (
	"encoding/binary"
	"fmt"
	"math"
)

type StoredVector struct {
	ImageID int64
	ModelID string
	Values  []float32
}

type VectorPoint struct {
	ImageID int64
	Vector  []float32
	Payload map[string]any
}

type VectorMatch struct {
	ImageID int64
	Score   float32
}

// EncodeVector는 벡터를 little endian float32 바이트로 바꿉니다.
// PostgreSQL bytea 백업용이며, Qdrant가 사라져도 여기서 재구축합니다.
func EncodeVector(values []float32) []byte {
	buf := make([]byte, len(values)*4)
	for i, v := range values {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

func DecodeVector(buf []byte) ([]float32, error) {
	if len(buf)%4 != 0 {
		return nil, fmt.Errorf("벡터 바이트 길이가 4의 배수가 아닙니다: %d", len(buf))
	}
	out := make([]float32, len(buf)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return out, nil
}

// Normalize는 코사인 거리 검색을 위해 벡터를 단위 길이로 맞춥니다.
// 모델이 이미 정규화해서 주더라도 장치별 미세 오차를 없애기 위해 한 번 더 적용합니다.
func Normalize(values []float32) {
	var sum float64
	for _, v := range values {
		sum += float64(v) * float64(v)
	}
	// 길이가 0이면 방향이 없어 맞출 수가 없습니다.
	// 무한대나 NaN이면 이미 망가진 벡터입니다. 여기서 0으로 만들어 버리면
	// 멀쩡한 영벡터처럼 보이게 되므로 그대로 두고 ValidateVectors가 잡게 합니다.
	if sum == 0 || math.IsInf(sum, 0) || math.IsNaN(sum) {
		return
	}

	// 나눗셈과 곱셈을 float64로 하고 마지막에만 float32로 돌립니다.
	// 값이 아주 작으면 1을 길이로 나눈 값이 float32 범위를 넘습니다.
	// 그 상태로 float32에 담으면 무한대가 되고, 곱하는 순간 벡터 전체가
	// 무한대가 됩니다. 결과는 길이 1이라 float32에 언제나 들어갑니다.
	inv := 1 / math.Sqrt(sum)
	for i := range values {
		values[i] = float32(float64(values[i]) * inv)
	}
}

func CosineSimilarity(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("벡터 차원이 다릅니다: %d != %d", len(a), len(b))
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0, nil
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb))), nil
}
