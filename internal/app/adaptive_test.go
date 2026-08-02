package app

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newTestLimiter(clock Clock, start, min, max int) *Limiter {
	return NewLimiter(LimiterConfig{
		Start: start, Min: min, Max: max,
		Interval: 10 * time.Second, Enabled: true,
	}, clock)
}

func TestLimiterGrowsWhenHealthy(t *testing.T) {
	clock := newFakeClock()
	l := newTestLimiter(clock, 4, 1, 64)

	for round := 0; round < 3; round++ {
		for i := 0; i < 10; i++ {
			l.Report(OutcomeOK)
		}
		clock.Advance(11 * time.Second)
		l.Tick()
	}

	if got := l.Limit(); got != 7 {
		t.Fatalf("한도가 %d입니다. 4에서 3번 늘어 7이 되어야 합니다", got)
	}
}

func TestLimiterHalvesOnThrottle(t *testing.T) {
	clock := newFakeClock()
	l := newTestLimiter(clock, 16, 1, 64)

	for i := 0; i < 20; i++ {
		l.Report(OutcomeOK)
	}
	l.Report(OutcomeThrottled)

	clock.Advance(11 * time.Second)
	l.Tick()

	if got := l.Limit(); got != 8 {
		t.Fatalf("한도가 %d입니다. 16의 절반인 8을 기대했습니다", got)
	}
}

func TestLimiterHalvesOnTimeout(t *testing.T) {
	clock := newFakeClock()
	l := newTestLimiter(clock, 10, 1, 64)

	l.Report(OutcomeTimeout)
	clock.Advance(11 * time.Second)
	l.Tick()

	if got := l.Limit(); got != 5 {
		t.Fatalf("한도가 %d입니다. 5를 기대했습니다", got)
	}
}

// 흔한 오류가 조금 섞이는 것만으로 줄이면 안 됩니다.
func TestLimiterToleratesFewErrors(t *testing.T) {
	clock := newFakeClock()
	l := newTestLimiter(clock, 8, 1, 64)

	for i := 0; i < 20; i++ {
		l.Report(OutcomeOK)
	}
	l.Report(OutcomeError)

	clock.Advance(11 * time.Second)
	l.Tick()

	if got := l.Limit(); got != 9 {
		t.Fatalf("한도가 %d입니다. 오류가 적으면 늘어서 9가 되어야 합니다", got)
	}
}

func TestLimiterShrinksOnManyErrors(t *testing.T) {
	clock := newFakeClock()
	l := newTestLimiter(clock, 12, 1, 64)

	for i := 0; i < 5; i++ {
		l.Report(OutcomeOK)
	}
	for i := 0; i < 5; i++ {
		l.Report(OutcomeError)
	}

	clock.Advance(11 * time.Second)
	l.Tick()

	if got := l.Limit(); got != 6 {
		t.Fatalf("한도가 %d입니다. 오류 비율이 높으면 6으로 줄어야 합니다", got)
	}
}

func TestLimiterRespectsBounds(t *testing.T) {
	clock := newFakeClock()
	l := newTestLimiter(clock, 2, 2, 3)

	for round := 0; round < 5; round++ {
		l.Report(OutcomeOK)
		clock.Advance(11 * time.Second)
		l.Tick()
	}
	if got := l.Limit(); got != 3 {
		t.Fatalf("한도가 %d입니다. 최대 3을 넘으면 안 됩니다", got)
	}

	for round := 0; round < 5; round++ {
		l.Report(OutcomeThrottled)
		clock.Advance(11 * time.Second)
		l.Tick()
	}
	if got := l.Limit(); got != 2 {
		t.Fatalf("한도가 %d입니다. 최소 2 아래로 내려가면 안 됩니다", got)
	}
}

func TestLimiterFixedWhenDisabled(t *testing.T) {
	clock := newFakeClock()
	l := NewLimiter(LimiterConfig{Start: 5, Min: 1, Max: 64,
		Interval: 10 * time.Second, Enabled: false}, clock)

	for i := 0; i < 30; i++ {
		l.Report(OutcomeOK)
	}
	clock.Advance(time.Minute)
	l.Tick()

	if got := l.Limit(); got != 5 {
		t.Fatalf("자동 조절이 꺼져 있는데 한도가 %d로 바뀌었습니다", got)
	}
}

// 실제로 동시에 들어갈 수 있는 수가 한도를 넘으면 안 됩니다.
func TestLimiterEnforcesConcurrency(t *testing.T) {
	l := NewLimiter(LimiterConfig{Start: 3, Min: 1, Max: 3,
		Interval: time.Hour, Enabled: false}, newFakeClock())

	ctx := context.Background()
	var (
		mu      sync.Mutex
		running int
		peak    int
		wg      sync.WaitGroup
	)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := l.Acquire(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			defer release()

			mu.Lock()
			running++
			if running > peak {
				peak = running
			}
			mu.Unlock()

			time.Sleep(time.Millisecond)

			mu.Lock()
			running--
			mu.Unlock()
		}()
	}
	wg.Wait()

	if peak > 3 {
		t.Fatalf("동시 실행이 최대 %d개까지 갔습니다. 3을 넘으면 안 됩니다", peak)
	}
	if peak < 2 {
		t.Fatalf("동시 실행이 %d개뿐입니다. 병렬로 돌지 않았습니다", peak)
	}
}

func TestLimiterAcquireRespectsContext(t *testing.T) {
	l := NewLimiter(LimiterConfig{Start: 1, Min: 1, Max: 1,
		Interval: time.Hour, Enabled: false}, newFakeClock())

	release, err := l.Acquire(context.Background())
	if err != nil {
		t.Fatalf("첫 획득 실패: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := l.Acquire(ctx); err == nil {
		t.Fatal("자리가 없으면 컨텍스트 만료로 실패해야 합니다")
	}
}

func TestRangeSizeScalesWithSpeed(t *testing.T) {
	const base = 10_000

	cases := []struct {
		name      string
		perSecond float64
		want      int64
	}{
		{"측정 전", 0, base},
		{"아주 느린 노드", 0.01, 100},
		{"보통 노드", 2, 7200},
		{"빠른 노드", 100, base * 4},
	}
	for _, tc := range cases {
		if got := RangeSizeFor(tc.perSecond, base); got != tc.want {
			t.Errorf("%s: %d를 얻었습니다. %d를 기대했습니다", tc.name, got, tc.want)
		}
	}
}
