package app

import (
	"context"
	"sync"
	"time"
)

// Outcome은 작업 하나의 결과입니다. 조절기는 이것만 보고 판단합니다.
type Outcome int

const (
	OutcomeOK Outcome = iota
	// OutcomeThrottled는 상대가 속도를 줄이라고 알려준 경우입니다. 429가 대표적입니다.
	OutcomeThrottled
	// OutcomeTimeout은 응답이 오지 않은 경우입니다. 대개 과부하 신호입니다.
	OutcomeTimeout
	// OutcomeError는 그 밖의 실패입니다. 속도와 무관할 수 있으므로 약하게 반영합니다.
	OutcomeError
)

type LimiterConfig struct {
	Start    int
	Min      int
	Max      int
	Interval time.Duration
	// Enabled가 false면 시작값에 고정합니다.
	Enabled bool
}

// Limiter는 동시 작업 수를 스스로 조절합니다.
// 잘 되면 하나씩 늘리고, 문제가 생기면 절반으로 줄입니다.
// 통신 혼잡 제어와 같은 방식이며, 노드마다 자기 하드웨어에 맞는 값을 찾아갑니다.
type Limiter struct {
	cfg LimiterConfig

	mu      sync.Mutex
	limit   int
	slots   chan struct{}
	window  windowStats
	lastAdj time.Time
	clock   Clock
}

type windowStats struct {
	ok        int
	throttled int
	timeout   int
	failed    int
}

func (w windowStats) total() int { return w.ok + w.throttled + w.timeout + w.failed }

func (w windowStats) shouldShrink() bool {
	if w.throttled > 0 || w.timeout > 0 {
		return true
	}
	// 오류가 전체의 4분의 1을 넘으면 과부하로 봅니다.
	return w.total() >= 8 && w.failed*4 > w.total()
}

func NewLimiter(cfg LimiterConfig, clock Clock) *Limiter {
	if clock == nil {
		clock = SystemClock
	}
	if cfg.Min < 1 {
		cfg.Min = 1
	}
	if cfg.Max < cfg.Min {
		cfg.Max = cfg.Min
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	start := cfg.Start
	if start < cfg.Min {
		start = cfg.Min
	}
	if start > cfg.Max {
		start = cfg.Max
	}

	l := &Limiter{
		cfg:     cfg,
		limit:   start,
		slots:   make(chan struct{}, cfg.Max),
		clock:   clock,
		lastAdj: clock.Now(),
	}
	for i := 0; i < start; i++ {
		l.slots <- struct{}{}
	}
	return l
}

func (l *Limiter) Limit() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limit
}

// Acquire는 자리가 날 때까지 기다립니다. 반환된 함수를 반드시 호출해야 자리가 반납됩니다.
func (l *Limiter) Acquire(ctx context.Context) (release func(), err error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-l.slots:
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			select {
			case l.slots <- struct{}{}:
			default:
				// 그 사이 한도가 줄었으면 자리를 없앱니다.
			}
		})
	}, nil
}

// Report는 작업 결과를 기록하고, 조절 주기가 지났으면 한도를 다시 계산합니다.
func (l *Limiter) Report(outcome Outcome) {
	l.mu.Lock()
	defer l.mu.Unlock()

	switch outcome {
	case OutcomeOK:
		l.window.ok++
	case OutcomeThrottled:
		l.window.throttled++
	case OutcomeTimeout:
		l.window.timeout++
	default:
		l.window.failed++
	}

	if !l.cfg.Enabled {
		return
	}
	now := l.clock.Now()
	if now.Sub(l.lastAdj) < l.cfg.Interval {
		return
	}
	l.adjustLocked(now)
}

// Tick은 작업이 없는 동안에도 주기가 지나면 한도를 재평가합니다.
func (l *Limiter) Tick() {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.cfg.Enabled {
		return
	}
	now := l.clock.Now()
	if now.Sub(l.lastAdj) < l.cfg.Interval {
		return
	}
	l.adjustLocked(now)
}

func (l *Limiter) adjustLocked(now time.Time) {
	defer func() {
		l.window = windowStats{}
		l.lastAdj = now
	}()

	if l.window.total() == 0 {
		return
	}

	target := l.limit
	if l.window.shouldShrink() {
		target = l.limit / 2
	} else if l.window.ok > 0 {
		target = l.limit + 1
	}
	l.setLocked(target)
}

func (l *Limiter) setLocked(target int) {
	if target < l.cfg.Min {
		target = l.cfg.Min
	}
	if target > l.cfg.Max {
		target = l.cfg.Max
	}
	if target == l.limit {
		return
	}

	if target > l.limit {
		for i := l.limit; i < target; i++ {
			select {
			case l.slots <- struct{}{}:
			default:
			}
		}
	} else {
		// 사용 중인 자리는 뺏지 않습니다. 반납될 때 Acquire의 release가 흡수합니다.
		for i := l.limit; i > target; i-- {
			select {
			case <-l.slots:
			default:
			}
		}
	}
	l.limit = target
}

// SetLimit은 대시보드에서 손으로 조절할 때 씁니다.
func (l *Limiter) SetLimit(target int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.setLocked(target)
}

// RangeSizeFor는 노드 처리 속도에 맞는 구간 크기를 정합니다.
// 느린 노드가 큰 구간을 오래 붙잡고 있으면 다른 노드가 놀게 되므로,
// 대략 한 시간 안에 끝낼 수 있는 크기로 맞춥니다.
func RangeSizeFor(perSecond float64, base int64) int64 {
	const targetSeconds = 3600
	if perSecond <= 0 {
		return base
	}
	size := int64(perSecond * targetSeconds)
	switch {
	case size < 100:
		return 100
	case size > base*4:
		return base * 4
	default:
		return size
	}
}
