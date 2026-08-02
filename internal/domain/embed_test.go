package domain

import (
	"errors"
	"math"
	"testing"
)

func activeModels() []EmbeddingModel {
	return []EmbeddingModel{
		{ID: "dinov2-base", Kind: ModelCopy, VectorSize: 4},
		{ID: "siglip-base", Kind: ModelSemantic, VectorSize: 2},
	}
}

func TestValidateVectorsAccepts(t *testing.T) {
	result := EmbedResult{Vectors: []Vector{
		{ModelID: "dinov2-base", Values: []float32{1, 0, 0, 0}},
		{ModelID: "siglip-base", Values: []float32{0, 1}},
	}}
	if err := result.Validate(activeModels()); err != nil {
		t.Fatalf("정상 벡터를 거부했습니다: %v", err)
	}
}

func TestValidateVectorsIgnoresExtras(t *testing.T) {
	// 워커에 모델을 더 올려 두고 아직 등록만 안 한 상태일 수 있습니다.
	// 활성 모델만 채워져 있으면 저장을 막을 이유가 없습니다.
	result := EmbedResult{Vectors: []Vector{
		{ModelID: "dinov2-base", Values: []float32{1, 0, 0, 0}},
		{ModelID: "siglip-base", Values: []float32{0, 1}},
		{ModelID: "실험중", Values: []float32{1, 2, 3}},
	}}
	if err := result.Validate(activeModels()); err != nil {
		t.Fatalf("남는 벡터 때문에 거부했습니다: %v", err)
	}
}

func TestValidateVectorsRejectsMissing(t *testing.T) {
	result := EmbedResult{Vectors: []Vector{
		{ModelID: "dinov2-base", Values: []float32{1, 0, 0, 0}},
	}}

	err := result.Validate(activeModels())
	if !errors.Is(err, ErrModelMismatch) {
		t.Fatalf("%v입니다. ErrModelMismatch를 기대했습니다", err)
	}
}

func TestValidateVectorsRejectsWrongSize(t *testing.T) {
	// 차원이 어긋나면 Qdrant가 받아 주더라도 검색 결과가 조용히 망가집니다.
	result := EmbedResult{Vectors: []Vector{
		{ModelID: "dinov2-base", Values: []float32{1, 0, 0}},
		{ModelID: "siglip-base", Values: []float32{0, 1}},
	}}

	err := result.Validate(activeModels())
	if !errors.Is(err, ErrModelMismatch) {
		t.Fatalf("%v입니다. ErrModelMismatch를 기대했습니다", err)
	}
}

// CUDA에서는 fp16으로 추론합니다. 65504를 넘으면 무한대가 되고 정규화하면
// 벡터 전체가 NaN이 됩니다. 저장 자체는 성공하므로 여기서 막지 않으면
// 어떤 질의에도 걸리지 않는 점이 조용히 쌓입니다.
func TestValidateVectorsRejectsNonFinite(t *testing.T) {
	tests := []struct {
		name  string
		value float32
	}{
		{"NaN", float32(math.NaN())},
		{"양의 무한대", float32(math.Inf(1))},
		{"음의 무한대", float32(math.Inf(-1))},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := EmbedResult{Vectors: []Vector{
				{ModelID: "dinov2-base", Values: []float32{1, tc.value, 0, 0}},
				{ModelID: "siglip-base", Values: []float32{0, 1}},
			}}

			err := result.Validate(activeModels())
			if !errors.Is(err, ErrBadVector) {
				t.Fatalf("%v입니다. ErrBadVector를 기대했습니다", err)
			}
		})
	}
}

func TestValidateVectorsAllowsEmptyActive(t *testing.T) {
	// 아직 모델을 등록하지 않은 노드입니다. 확인할 기준이 없으면 통과시킵니다.
	if err := ValidateVectors(nil, nil); err != nil {
		t.Fatalf("기준이 없는데 거부했습니다: %v", err)
	}
}

func TestEmbedResultVector(t *testing.T) {
	result := EmbedResult{Vectors: []Vector{
		{ModelID: "a", Values: []float32{1, 2}},
		{ModelID: "b", Values: []float32{3}},
	}}

	if values, ok := result.Vector("b"); !ok || len(values) != 1 || values[0] != 3 {
		t.Errorf("b를 찾지 못했습니다: %v %v", values, ok)
	}
	if _, ok := result.Vector("없음"); ok {
		t.Error("없는 모델을 찾았다고 합니다")
	}
}

func TestEmbedderInfoModel(t *testing.T) {
	info := EmbedderInfo{
		Device: DeviceCUDA,
		Models: []EmbeddingModel{{ID: "a", VectorSize: 768}},
	}

	model, ok := info.Model("a")
	if !ok || model.VectorSize != 768 {
		t.Errorf("a를 찾지 못했습니다: %+v %v", model, ok)
	}
	if _, ok := info.Model("b"); ok {
		t.Error("없는 모델을 찾았다고 합니다")
	}
}
