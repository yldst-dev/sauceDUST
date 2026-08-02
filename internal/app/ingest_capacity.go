package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"saucedust/internal/domain"
)

// checkRoom은 더 받을 자리가 있는지 봅니다.
//
// 수집하는 쪽에도 같은 검사가 있지만 그쪽은 구간을 받기 전에 한 번 볼
// 뿐이라, 구간 하나를 처리하는 동안 넘길 수 있고 설정을 빠뜨린 노드는
// 아예 검사하지 않습니다. 여기가 자료가 들어오는 유일한 문입니다.
//
// 세지 못하면 받습니다. 저장소가 잠깐 흔들렸다고 들어온 결과를 버리면
// 그 노드가 이미 내려받아 계산한 것이 통째로 사라집니다.
func (in *Ingest) checkRoom(ctx context.Context, incoming int) error {
	if in.maxIndexed <= 0 || in.counter == nil {
		return nil
	}

	count, ok := in.roomCount(ctx)
	if !ok {
		return nil
	}

	// 이번 묶음까지 더해서 봅니다. 지금 수만 보면 상한 바로 아래에서
	// 묶음 하나가 통째로 들어가 크게 넘칩니다.
	//
	// 이것으로도 완전히 막히지는 않습니다. 여러 노드가 같은 순간에
	// 물어보면 다 같은 수를 보고 통과합니다. 넘칠 수 있는 양은
	// 동시에 들어오는 묶음들의 합이고, 담을 장수를 낼 때 여유를
	// 20퍼센트 빼 두므로 그 안에서 받아냅니다.
	if count+int64(incoming) <= in.maxIndexed {
		in.warnedFull.Store(false)
		return nil
	}

	if in.warnedFull.CompareAndSwap(false, true) {
		in.log.Warn("색인이 차서 더 받지 않습니다",
			slog.Int64("담긴 장수", count),
			slog.Int("들어온 묶음", incoming),
			slog.Int64("상한", in.maxIndexed))
	}
	return fmt.Errorf("%w: %d장 담김, %d장 더 받으면 상한 %d를 넘습니다",
		domain.ErrIndexFull, count, incoming, in.maxIndexed)
}

// roomCount는 담긴 장수를 냅니다. 잠깐 기억해 두고 씁니다.
//
// 묶음마다 1,000만 행을 세면 적재가 그것 때문에 느려집니다. 기억해 두는
// 사이에 조금 넘칠 수 있는데, 담을 장수를 낼 때 여유를 20퍼센트 빼 두므로
// 그 안에서 받아냅니다.
func (in *Ingest) roomCount(ctx context.Context) (int64, bool) {
	now := time.Now()
	if at := in.countedAt.Load(); at > 0 && now.Sub(time.Unix(0, at)) < ingestCountEvery {
		return in.lastCount.Load(), true
	}

	count, err := in.counter.CountImages(ctx)
	if err != nil {
		if ctx.Err() == nil {
			in.log.Warn("담긴 장수를 세지 못해 그대로 받습니다",
				slog.String("error", err.Error()))
		}
		return 0, false
	}

	in.lastCount.Store(count)
	in.countedAt.Store(now.UnixNano())
	return count, true
}

// 적재는 수집보다 자주 일어나므로 더 짧게 잡습니다.
const ingestCountEvery = 5 * time.Second

type ingestCounterState struct {
	warnedFull atomic.Bool
	lastCount  atomic.Int64
	countedAt  atomic.Int64
}
