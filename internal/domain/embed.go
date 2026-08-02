package domain

import (
	"fmt"
	"math"
)

// EmbedderInfo는 워커가 실제로 올린 모델과 장치를 보고합니다.
// 노드마다 다른 모델을 쓰면 벡터를 비교할 수 없으므로 시작 시 반드시 확인합니다.
type EmbedderInfo struct {
	Device Device
	Models []EmbeddingModel
}

func (i EmbedderInfo) Model(id string) (EmbeddingModel, bool) {
	for _, m := range i.Models {
		if m.ID == id {
			return m, true
		}
	}
	return EmbeddingModel{}, false
}

// EmbedResult는 이미지 한 장에 대한 모든 모델의 벡터와 지각 해시입니다.
// 해시를 워커에서 함께 계산하는 이유는 디코드를 한 번만 하기 위해서입니다.
type EmbedResult struct {
	Vectors []Vector
	Hashes  Hashes
	Thumb   []byte
}

type Hashes struct {
	PHash  string
	DHash  string
	Width  int
	Height int
}

func (r EmbedResult) Vector(modelID string) ([]float32, bool) {
	for _, v := range r.Vectors {
		if v.ModelID == modelID {
			return v.Values, true
		}
	}
	return nil, false
}

// Validate는 등록된 활성 모델의 벡터가 전부 왔는지, 차원이 맞는지 확인합니다.
func (r EmbedResult) Validate(active []EmbeddingModel) error {
	return ValidateVectors(r.Vectors, active)
}

// ValidateVectors는 벡터 묶음이 활성 모델 구성과 맞는지 봅니다.
// 노드가 다른 모델을 쓰거나 차원이 어긋나면 검색이 조용히 망가지므로
// 저장 전에 여기서 막습니다.
func ValidateVectors(vectors []Vector, active []EmbeddingModel) error {
	byID := make(map[string][]float32, len(vectors))
	for _, v := range vectors {
		byID[v.ModelID] = v.Values
	}

	for _, m := range active {
		values, ok := byID[m.ID]
		if !ok {
			return fmt.Errorf("%w: 모델 %s의 벡터가 없습니다", ErrModelMismatch, m.ID)
		}
		if len(values) != m.VectorSize {
			return fmt.Errorf("%w: 모델 %s의 벡터 차원이 %d입니다. 기준은 %d입니다",
				ErrModelMismatch, m.ID, len(values), m.VectorSize)
		}
		if i, ok := firstNonFinite(values); ok {
			return fmt.Errorf("%w: 모델 %s의 벡터 %d번째 값이 숫자가 아닙니다(%v)",
				ErrBadVector, m.ID, i, values[i])
		}
	}
	return nil
}

// firstNonFinite는 NaN이나 무한대가 처음 나온 자리를 알려줍니다.
//
// CUDA에서는 fp16으로 추론합니다. fp16은 65504를 넘으면 무한대가 되고,
// 그 상태로 정규화하면 벡터 전체가 NaN이 됩니다. 그대로 저장하면 Qdrant에
// 어떤 질의에도 걸리지 않는 죽은 점이 쌓이는데, 오류가 나지 않으니
// 나중에 세어 보기 전까지 알아채지 못합니다.
func firstNonFinite(values []float32) (int, bool) {
	for i, v := range values {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return i, true
		}
	}
	return 0, false
}
