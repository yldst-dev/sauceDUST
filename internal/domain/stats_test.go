package domain

import "testing"

func TestCrawlRangeSize(t *testing.T) {
	// 양끝을 포함하므로 1부터 10까지는 10개입니다.
	tests := []struct {
		lower, upper, want int64
	}{
		{1, 10, 10},
		{1, 1, 1},
		{0, 9999, 10000},
		{9000001, 9010000, 10000},
	}
	for _, tc := range tests {
		got := CrawlRange{LowerID: tc.lower, UpperID: tc.upper}.Size()
		if got != tc.want {
			t.Errorf("%d~%d가 %d입니다. %d를 기대했습니다",
				tc.lower, tc.upper, got, tc.want)
		}
	}
}

func TestIDGapSize(t *testing.T) {
	if got := (IDGap{From: 100, To: 199}).Size(); got != 100 {
		t.Errorf("%d입니다. 100을 기대했습니다", got)
	}
	if got := (IDGap{From: 5, To: 5}).Size(); got != 1 {
		t.Errorf("%d입니다. 1을 기대했습니다", got)
	}
}

func TestRangeStatsComplete(t *testing.T) {
	tests := []struct {
		name  string
		stats RangeStats
		want  bool
	}{
		{"전부 끝남", RangeStats{Total: 10, Completed: 10}, true},
		{"빈 구간이 섞여도 끝남", RangeStats{Total: 10, Completed: 7, Empty: 3}, true},
		{"아직 도는 중", RangeStats{Total: 10, Completed: 9, Running: 1}, false},
		{"실패가 남음", RangeStats{Total: 10, Completed: 9, Failed: 1}, false},
		{"구간이 없음", RangeStats{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.stats.Complete(); got != tc.want {
				t.Errorf("%v입니다. %v를 기대했습니다", got, tc.want)
			}
		})
	}
}

// 시도 상한을 넘긴 구간은 failed로 남으므로 Failed가 0이면 Exhausted도 0입니다.
// 이 관계가 깨지면 Complete가 참인데도 영영 안 들어오는 대역이 생깁니다.
func TestExhaustedImpliesFailed(t *testing.T) {
	stats := RangeStats{Total: 10, Completed: 9, Failed: 1, Exhausted: 1}
	if stats.Complete() {
		t.Error("소진된 구간이 남았는데 끝났다고 합니다")
	}
}

func TestRoleValid(t *testing.T) {
	for _, role := range []Role{RoleControl, RoleWorker} {
		if !role.Valid() {
			t.Errorf("%q를 거부했습니다", role)
		}
	}
	for _, role := range []Role{"", "master", "CONTROL", "worker "} {
		if Role(role).Valid() {
			t.Errorf("%q를 받아들였습니다", role)
		}
	}
}

func TestNetModeValid(t *testing.T) {
	for _, mode := range []NetMode{NetDirect, NetECH, NetFragment, NetProxy} {
		if !mode.Valid() {
			t.Errorf("%q를 거부했습니다", mode)
		}
	}
	// unknown은 아직 재 보지 않았다는 표시일 뿐, 고를 수 있는 경로가 아닙니다.
	for _, mode := range []NetMode{"", NetUnknown, "sni", "DIRECT", "proxy"} {
		if NetMode(mode).Valid() {
			t.Errorf("%q를 받아들였습니다", mode)
		}
	}
}
