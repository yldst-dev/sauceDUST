package app

import (
	"context"
	"testing"
	"time"
)

// 하한이 임대 요청까지 그대로 가야 합니다.
//
// 이 값은 원래 domain.LeaseRequest와 SQL에만 있고 아무도 채우지 않았습니다.
// 저장 계층은 받을 준비가 되어 있는데 설정에서 닿는 길이 없어, 수집 범위를
// 막을 방법이 사실상 없었습니다.
func TestBackfillFloorReachesTheLease(t *testing.T) {
	cfg := baseConfig()
	cfg.BackfillFloor = 150

	lease := runBriefly(t, cfg)
	if lease.floorSeen != 150 {
		t.Errorf("임대 요청이 받은 하한이 %d입니다. 150을 기대했습니다", lease.floorSeen)
	}
}

// 두지 않았으면 0이 가야 합니다. 저장 계층이 0을 1로 바꿔 끝까지 내려갑니다.
func TestBackfillFloorDefaultsToZero(t *testing.T) {
	lease := runBriefly(t, baseConfig())
	if lease.floorSeen != 0 {
		t.Errorf("하한을 두지 않았는데 %d가 갔습니다", lease.floorSeen)
	}
}

func runBriefly(t *testing.T, cfg CrawlerConfig) fakeLease {
	t.Helper()

	lease := &fakeLease{frontier: 200}
	source := &rangeSource{latest: 199}
	source.fakeSource.failURL = map[string]error{}

	crawler, _ := newCrawler(t, cfg, source, lease)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	crawler.Run(ctx)

	return lease.snapshot()
}

// 재시도 큐에도 하한이 가야 합니다.
//
// 하한을 올리기 전에 실패해 큐에 남은 게시물이 계속 다시 색인되면
// 하한을 둔 뜻이 없어집니다.
func TestBackfillFloorReachesTheRetryQueue(t *testing.T) {
	cfg := baseConfig()
	cfg.BackfillFloor = 150

	lease := runBriefly(t, cfg)
	if lease.retryFloorSeen != 150 {
		t.Errorf("재시도 임대가 받은 하한이 %d입니다", lease.retryFloorSeen)
	}
}
