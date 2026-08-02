package domain

import (
	"encoding/hex"
	"fmt"
	"math/bits"
)

// HammingDistance는 두 지각 해시가 몇 비트나 다른지 셉니다.
// 값이 작을수록 같은 그림일 가능성이 높습니다. 64비트 해시에서 5 이하면
// 크기나 압축률만 다른 사실상 같은 이미지로 봅니다.
func HammingDistance(a, b string) (int, error) {
	if a == "" || b == "" {
		return 0, fmt.Errorf("해시가 비어 있습니다")
	}
	if len(a) != len(b) {
		return 0, fmt.Errorf("해시 길이가 다릅니다: %d != %d", len(a), len(b))
	}

	left, err := hex.DecodeString(a)
	if err != nil {
		return 0, fmt.Errorf("해시를 해석하지 못했습니다: %w", err)
	}
	right, err := hex.DecodeString(b)
	if err != nil {
		return 0, fmt.Errorf("해시를 해석하지 못했습니다: %w", err)
	}

	var distance int
	for i := range left {
		distance += bits.OnesCount8(left[i] ^ right[i])
	}
	return distance, nil
}

// HashBits는 해시 문자열이 나타내는 전체 비트 수입니다.
func HashBits(hash string) int { return len(hash) * 4 }

// SameImageThreshold는 같은 그림으로 판단하는 해밍 거리 상한입니다.
// 64비트 기준 5비트, 즉 8퍼센트 미만 차이까지 허용합니다.
func SameImageThreshold(hash string) int {
	threshold := HashBits(hash) * 5 / 64
	if threshold < 1 {
		return 1
	}
	return threshold
}
