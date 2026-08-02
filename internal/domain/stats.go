package domain

// FleetSummary는 최근 구간 동안 노드들이 실제로 얼마나 처리했는지 요약합니다.
// 누적 카운터의 차이를 시간으로 나눠 초당 처리량을 냅니다.
type FleetSummary struct {
	Nodes          map[string]NodeThroughput
	TotalPerSecond float64
}

type NodeThroughput struct {
	NodeID     string
	Downloaded int64
	Embedded   int64
	Saved      int64
	Failed     int64
	PerSecond  float64
	CPUPct     float32
	MemMB      float32
}

// CrawlSummary는 수집 진행 상황입니다.
type CrawlSummary struct {
	RangesCompleted int64
	RangesRunning   int64
	RangesFailed    int64
	RangesEmpty     int64
	RetryPending    int64
	RetryDead       int64
	HighWatermark   int64
	BackfillBefore  int64
	VectorsByModel  map[string]int64
}

// RangeStats는 수집 구간의 상태별 집계입니다.
// Exhausted가 0이 아니면 그만큼의 ID 대역이 영영 들어오지 않습니다.
type RangeStats struct {
	Total      int64
	Completed  int64
	Running    int64
	Empty      int64
	Failed     int64
	Exhausted  int64
	Saved      int64
	MissingIDs int64
}

// Complete는 더 손볼 것이 없는 상태인지 알려줍니다.
func (s RangeStats) Complete() bool {
	return s.Total > 0 && s.Failed == 0 && s.Running == 0
}

// IDGap은 아직 구간이 만들어지지 않은 ID 대역입니다.
type IDGap struct {
	From int64
	To   int64
}

func (g IDGap) Size() int64 { return g.To - g.From + 1 }
