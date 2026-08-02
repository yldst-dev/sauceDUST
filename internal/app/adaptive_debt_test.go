package app

import (
	"context"
	"testing"
)

// 한도를 줄이는 그 순간에 자리가 전부 차 있으면 줄일 자리가 없습니다.
//
// 그때 반납을 그냥 받아 주면 축소가 헛일이 됩니다. 슬롯 통 크기가 Max라
// 반납이 언제나 성공하고, 쓰던 자리가 전부 되돌아옵니다. 429가 나는 때는
// 대개 자리가 다 찬 때라 하필 그때 안 듣습니다.
func TestShrinkWhileBusyActuallyShrinks(t *testing.T) {
	l := NewLimiter(LimiterConfig{Start: 8, Min: 1, Max: 16}, nil)
	ctx := context.Background()

	// 여덟 자리를 전부 잡습니다.
	releases := make([]func(), 0, 8)
	for i := 0; i < 8; i++ {
		release, err := l.Acquire(ctx)
		if err != nil {
			t.Fatalf("%d번째 자리를 잡지 못했습니다: %v", i, err)
		}
		releases = append(releases, release)
	}

	// 자리가 하나도 남지 않은 상태에서 절반으로 줄입니다.
	l.SetLimit(4)
	if got := l.Limit(); got != 4 {
		t.Fatalf("한도가 %d입니다", got)
	}
	if got := l.Debt(); got != 4 {
		t.Fatalf("갚을 자리가 %d개입니다. 4개를 기대했습니다", got)
	}

	// 전부 반납해도 실제로 쓸 수 있는 자리는 넷이어야 합니다.
	for _, release := range releases {
		release()
	}
	if got := l.Debt(); got != 0 {
		t.Errorf("반납이 끝났는데 빚이 %d개 남았습니다", got)
	}

	if got := len(l.slots); got != 4 {
		t.Errorf("쓸 수 있는 자리가 %d개입니다. 4개여야 합니다", got)
	}
}

// 늘릴 때는 빚부터 없애야 합니다. 빚을 남기면 늘린 만큼 안 늘어납니다.
func TestGrowingClearsDebtFirst(t *testing.T) {
	l := NewLimiter(LimiterConfig{Start: 8, Min: 1, Max: 16}, nil)
	ctx := context.Background()

	releases := make([]func(), 0, 8)
	for i := 0; i < 8; i++ {
		release, _ := l.Acquire(ctx)
		releases = append(releases, release)
	}

	l.SetLimit(4)
	l.SetLimit(8)
	if got := l.Debt(); got != 0 {
		t.Errorf("다시 늘렸는데 빚이 %d개 남았습니다", got)
	}

	for _, release := range releases {
		release()
	}

	if got := len(l.slots); got != 8 {
		t.Errorf("다시 늘린 뒤 쓸 수 있는 자리가 %d개입니다. 8개여야 합니다", got)
	}
}
