package app

// 색인이 담을 수 있는 장수를 셈합니다.
//
// Qdrant는 줄인 벡터와 그래프를 메모리에 붙들고 있어야 하고, 그 양은
// 장수에 정비례합니다. 넘기면 느려지는 것이 아니라 OOM으로 죽고, 다시
// 띄워도 기존 컬렉션을 여는 것 자체가 한도를 넘어 또 죽습니다.
//
// 그래서 다 모은 뒤에 줄이는 방법이 없습니다. 차기 전에 멈춰야 합니다.

// 30만 점을 넣어 두고 컨테이너 상한을 낮춰 가며 잰 값입니다.
//
// 이진 양자화에 그래프까지 디스크로 내린 구성입니다. 768차원에서 하한이
// 250MB였고 빈 Qdrant가 152MB이므로 장당 334바이트입니다. 그중 벡터가
// 차원의 8분의 1인 96바이트이고 나머지 238바이트가 그래프와 부대 비용
// 입니다. 512차원은 벡터만 64바이트로 줄어듭니다.
//
// 앞선 구성(int8에 그래프를 램에)은 장당 1,046바이트였습니다. 이 차이가
// 8GB에서 410만 장과 1,190만 장을 가릅니다.
//
// 측정점이 768차원 하나뿐이라 512차원은 벡터 몫만 줄여 셈한 값입니다.
const (
	bitsPerByte  = 8
	bytesPerNode = 238

	// 빈 Qdrant가 쓰는 몫입니다. 장수와 무관합니다.
	qdrantBaseBytes = 160 << 20
)

// IndexBytesPerImage는 이 모델 구성에서 한 장이 차지하는 상주 메모리입니다.
// 모델마다 따로 컬렉션을 두므로 전부 더합니다.
func IndexBytesPerImage(vectorSizes []int) int64 {
	var total int64
	for _, size := range vectorSizes {
		if size <= 0 {
			continue
		}
		total += int64(size/bitsPerByte) + bytesPerNode
	}
	return total
}

// ImageCapacity는 Qdrant에 내어 줄 수 있는 메모리로 담을 장수를 냅니다.
//
// 벼랑 끝에서 돌리지 않도록 안전 여유를 뺍니다. OOM은 서서히 오는 것이
// 아니라 한 번에 죽는 형태라 되돌릴 여지를 남겨야 합니다.
func ImageCapacity(qdrantBytes int64, vectorSizes []int) int64 {
	per := IndexBytesPerImage(vectorSizes)
	if per <= 0 {
		return 0
	}
	usable := qdrantBytes - qdrantBaseBytes
	if usable <= 0 {
		return 0
	}
	return int64(float64(usable) * capacitySafety / float64(per))
}

const capacitySafety = 0.8
