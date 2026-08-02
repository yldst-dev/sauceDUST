package domain

import "testing"

func TestHammingDistance(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{"같은 해시", "f0f0f0f0f0f0f0f0", "f0f0f0f0f0f0f0f0", 0},
		{"한 비트", "0000000000000000", "0000000000000001", 1},
		{"네 비트", "0000000000000000", "000000000000000f", 4},
		{"전부 다름", "0000000000000000", "ffffffffffffffff", 64},
		{"대소문자 섞임", "ABCDEF0123456789", "abcdef0123456789", 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HammingDistance(tc.a, tc.b)
			if err != nil {
				t.Fatalf("오류가 났습니다: %v", err)
			}
			if got != tc.want {
				t.Errorf("%d입니다. %d를 기대했습니다", got, tc.want)
			}
		})
	}
}

func TestHammingDistanceIsSymmetric(t *testing.T) {
	a, b := "0f1e2d3c4b5a6978", "f0e1d2c3b4a58796"

	forward, err := HammingDistance(a, b)
	if err != nil {
		t.Fatal(err)
	}
	backward, err := HammingDistance(b, a)
	if err != nil {
		t.Fatal(err)
	}
	if forward != backward {
		t.Errorf("방향에 따라 %d와 %d로 다릅니다", forward, backward)
	}
}

func TestHammingDistanceRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		a, b string
	}{
		{"왼쪽이 비었음", "", "f0f0f0f0f0f0f0f0"},
		{"오른쪽이 비었음", "f0f0f0f0f0f0f0f0", ""},
		{"둘 다 비었음", "", ""},
		{"길이가 다름", "f0f0", "f0f0f0f0"},
		{"16진수가 아님", "zzzzzzzzzzzzzzzz", "f0f0f0f0f0f0f0f0"},
		{"홀수 길이", "f0f", "0f0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := HammingDistance(tc.a, tc.b); err == nil {
				t.Error("오류가 나야 합니다")
			}
		})
	}
}

func TestSameImageThreshold(t *testing.T) {
	tests := []struct {
		name string
		hash string
		want int
	}{
		{"64비트", "f0f0f0f0f0f0f0f0", 5},
		{"128비트", "f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0", 10},
		{"짧은 해시도 최소 1", "f0", 1},
		{"빈 해시도 최소 1", "", 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameImageThreshold(tc.hash); got != tc.want {
				t.Errorf("%d입니다. %d를 기대했습니다", got, tc.want)
			}
		})
	}
}

func TestHashBits(t *testing.T) {
	if got := HashBits("f0f0f0f0f0f0f0f0"); got != 64 {
		t.Errorf("%d비트입니다. 64를 기대했습니다", got)
	}
	if got := HashBits(""); got != 0 {
		t.Errorf("빈 해시가 %d비트입니다", got)
	}
}

// 실측에서 원본을 절반 크기로 줄이고 JPEG 품질 40으로 다시 저장했을 때
// 거리가 2였습니다. 임계값 5는 그런 변형을 잡아내되 다른 그림은 거르는 선입니다.
func TestThresholdAcceptsDegradedCopy(t *testing.T) {
	original := "8f373714acfcf4d0"
	degraded := "8f373714acfcf4d2"

	distance, err := HammingDistance(original, degraded)
	if err != nil {
		t.Fatal(err)
	}
	if distance > SameImageThreshold(original) {
		t.Errorf("거리 %d가 임계값 %d를 넘습니다",
			distance, SameImageThreshold(original))
	}
}
