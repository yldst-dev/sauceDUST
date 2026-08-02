package danbooru

import (
	"context"
	"sync"
	"time"
)

// rateLimiter는 초당 요청 수를 제한합니다.
//
// 실측에서 동시 다운로드를 32로 올렸더니 128건 중 29건이 429로 돌아왔고,
// 이후 동시 1에서도 한동안 429가 이어졌습니다. 상대는 무료로 공개된
// 서비스이고 IP 단위로 제한을 겁니다. 동시 수를 늘려서 얻을 것이 없고,
// 계속 밀어붙이면 차단당합니다.
//
// 그래서 동시 수와 별개로 초당 요청 수 자체에 상한을 둡니다.
// 잠깐 몰리는 것은 허용하되 평균은 정해진 속도를 넘지 않게 합니다.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	burst    time.Duration
	next     time.Time
	clock    func() time.Time
}

func newRateLimiter(perSecond float64, burst int) *rateLimiter {
	if perSecond <= 0 {
		return nil
	}
	if burst < 1 {
		burst = 1
	}
	interval := time.Duration(float64(time.Second) / perSecond)
	return &rateLimiter{
		interval: interval,
		// burst는 즉시 내보낼 수 있는 요청 수입니다. 첫 건은 언제나 즉시
		// 나가므로 여유분은 그보다 하나 적습니다.
		burst: time.Duration(burst-1) * interval,
		clock: time.Now,
	}
}

// wait는 다음 요청을 보내도 되는 시각까지 기다립니다.
func (r *rateLimiter) wait(ctx context.Context) error {
	if r == nil {
		return nil
	}

	r.mu.Lock()
	now := r.clock()
	// 한동안 쉬었다면 그만큼 몰아서 보낼 수 있게 해 주되, burst를 넘지 않습니다.
	if earliest := now.Add(-r.burst); r.next.Before(earliest) {
		r.next = earliest
	}
	slot := r.next
	r.next = r.next.Add(r.interval)
	r.mu.Unlock()

	delay := slot.Sub(now)
	if delay <= 0 {
		return nil
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// penalize는 429를 받았을 때 다음 요청을 그만큼 뒤로 미룹니다.
// 상대가 속도를 줄이라고 알려줬으면 즉시 반영해야 합니다.
func (r *rateLimiter) penalize(d time.Duration) {
	if r == nil || d <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	until := r.clock().Add(d)
	if r.next.Before(until) {
		r.next = until
	}
}
