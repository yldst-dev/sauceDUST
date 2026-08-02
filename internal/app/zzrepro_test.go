package app

import (
	"context"
	"testing"
	"time"

	"saucedust/internal/domain"
)

// 찾은 것 1번 재현: MaxImages로 잘라 낸 구간이 completed로 적히는지.
func TestReproLimitCutHole(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxImages = 5
	cfg.BaseRangeSize = 20

	lease := &fakeLease{frontier: 41}
	source := &rangeSource{latest: 40}
	source.fakeSource.failURL = map[string]error{}

	crawler, h := newCrawler(t, cfg, source, lease)
	h.images.existing[21] = 210
	h.images.existing[22] = 220
	h.images.existing[23] = 230

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	crawler.Run(ctx)

	got := lease.snapshot()
	t.Logf("saved=%d finished=%v released=%v handedOut=%v",
		crawler.Saved(), got.finished, got.releasedRanges, got.handedOut)
}

// 찾은 것 3번 재현: 기존 시험의 준비에서 finished/released가 실제로 무엇인지.
func TestReproExistingLimitTestState(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxImages = 5

	lease := &fakeLease{frontier: 500}
	source := &rangeSource{latest: 499}
	source.fakeSource.failURL = map[string]error{}

	crawler, _ := newCrawler(t, cfg, source, lease)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	crawler.Run(ctx)

	got := lease.snapshot()
	t.Logf("saved=%d finished=%v released=%v handedOut=%d",
		crawler.Saved(), got.finished, got.releasedRanges, len(got.handedOut))
	if len(got.finished) == 0 {
		t.Log("finished 가 비어 있어 for 루프가 한 번도 돌지 않습니다")
	}
	_ = domain.RangeCompleted
}
