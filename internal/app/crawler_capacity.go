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

// countEvery는 저장소에 몇 장인지 다시 물어보는 주기입니다.
//
// 매번 물으면 구간마다 세는 질의가 붙습니다. 1,000만 행을 세는 것은
// 공짜가 아닙니다. 그렇다고 한 번만 물으면 다른 노드가 채워 넣는 것을
// 못 봅니다. 초당 5장이 상한이니 30초면 150장, 상한 근처에서도 넘칠
// 만한 양이 아닙니다.
const countEvery = 30 * time.Second

// atCapacity는 더 받을 수 있는지 봅니다. 찼으면 크롤러를 멈춥니다.
//
// 세지 못하면 막지 않습니다. 저장소가 잠깐 흔들렸다고 수집을 세우는 것은
// 지나칩니다. 다음 주기에 다시 봅니다.
func (c *Crawler) atCapacity(ctx context.Context) bool {
	if c.cfg.MaxIndexed <= 0 || c.counter == nil {
		return false
	}
	if c.full.Load() {
		return true
	}

	count, ok := c.indexedCount(ctx)
	if !ok {
		return false
	}
	if count < c.cfg.MaxIndexed {
		return false
	}

	// 여러 작업자가 동시에 여기 닿으므로 한 번만 알립니다.
	if c.full.CompareAndSwap(false, true) {
		c.log.Warn("색인이 담을 수 있는 만큼 찼습니다. 수집을 멈춥니다",
			slog.Int64("담긴 장수", count),
			slog.Int64("상한", c.cfg.MaxIndexed),
			slog.String("늘리려면", "메모리를 늘리고 SAUCEDUST_MAX_INDEXED를 올리십시오"))
		if c.stop != nil {
			c.stop()
		}
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
