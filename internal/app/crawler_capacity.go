package app

import (
	"context"
	"log/slog"
	"time"
)

// 색인이 찼는지 봅니다.
//
// 넘기면 Qdrant가 OOM으로 죽고, 다시 띄워도 기존 컬렉션을 여는 것 자체가
// 한도를 넘어 또 죽습니다. 되살릴 방법이 지우는 것밖에 없으므로 차기
// 전에 멈춰야 합니다.
//
// 멈추는 것은 수집뿐입니다. 프로세스를 끝내면 안 됩니다. 중앙 노드에서는
// 같은 프로세스가 검색과 대시보드를 함께 맡고 있어서, 크롤러가 반환하면
// runAll이 공용 취소를 걸어 검색까지 내려갑니다. 색인이 찬 것은 검색을
// 멈출 이유가 아니라 오히려 검색만 남기고 수집을 접을 이유입니다.

// countEvery는 저장소에 몇 장인지 다시 물어보는 주기입니다.
//
// 매번 물으면 구간마다 세는 질의가 붙습니다. 1,000만 행을 세는 것은
// 공짜가 아닙니다. 그렇다고 한 번만 물으면 다른 노드가 채워 넣는 것도,
// 운영자가 상한을 올린 것도 못 봅니다.
const countEvery = 30 * time.Second

// atCapacity는 더 받을 수 있는지 봅니다.
//
// 세지 못하면 막지 않습니다. 저장소가 잠깐 흔들렸다고 수집을 세우는 것은
// 지나칩니다. 다음 주기에 다시 봅니다. 정확한 상한은 중앙의 적재 경계에서
// 지키므로 여기서 새는 몫은 그쪽이 받아냅니다.
func (c *Crawler) atCapacity(ctx context.Context) bool {
	if c.cfg.MaxIndexed <= 0 || c.counter == nil {
		return false
	}

	count, ok := c.indexedCount(ctx)
	if !ok {
		return false
	}
	if count < c.cfg.MaxIndexed {
		// 상한을 올렸거나 지웠으면 다시 알릴 수 있어야 합니다.
		c.warnedFull.Store(false)
		return false
	}

	// 작업자마다 한 번씩 찍으면 로그가 같은 줄로 덮입니다.
	if c.warnedFull.CompareAndSwap(false, true) {
		c.log.Warn("색인이 담을 수 있는 만큼 찼습니다. 수집만 멈추고 검색은 계속합니다",
			slog.Int64("담긴 장수", count),
			slog.Int64("상한", c.cfg.MaxIndexed),
			slog.String("늘리려면", "메모리를 늘리고 SAUCEDUST_MAX_INDEXED를 올리십시오"))
	}
	return true
}

// indexedCount는 저장소에 든 장수를 냅니다. 잠깐 기억해 두고 씁니다.
func (c *Crawler) indexedCount(ctx context.Context) (int64, bool) {
	now := time.Now()
	if at := c.countedAt.Load(); at > 0 && now.Sub(time.Unix(0, at)) < countEvery {
		return c.lastCount.Load(), true
	}

	count, err := c.counter.CountImages(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("담긴 장수를 세지 못했습니다", slog.String("error", err.Error()))
		}
		return 0, false
	}

	c.lastCount.Store(count)
	c.countedAt.Store(now.UnixNano())
	return count, true
}
