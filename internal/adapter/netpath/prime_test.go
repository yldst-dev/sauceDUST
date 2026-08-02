package netpath

import (
	"io"
	"log/slog"
	"testing"

	"saucedust/internal/domain"
)

func testChain(t *testing.T, order ...domain.NetMode) *Chain {
	t.Helper()
	chain, err := New(Options{
		Order:  order,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("생성 실패: %v", err)
	}
	return chain
}

// 지난 실행에서 알아낸 경로를 채워 넣으면 그것부터 시도해야 합니다.
// 그러지 않으면 켤 때마다 막힌 경로에서 연결이 끊기길 기다립니다.
func TestPrimeSetsPreferredPath(t *testing.T) {
	chain := testChain(t, domain.NetDirect, domain.NetFragment)

	if got := chain.ModeFor("danbooru.donmai.us"); got != domain.NetDirect {
		t.Fatalf("기본값이 %q입니다. 첫 경로여야 합니다", got)
	}

	applied := chain.Prime(map[string]domain.NetMode{
		"danbooru.donmai.us": domain.NetFragment,
		"cdn.donmai.us":      domain.NetDirect,
	})
	if applied != 2 {
		t.Fatalf("%d개만 적용됐습니다", applied)
	}

	if got := chain.ModeFor("danbooru.donmai.us"); got != domain.NetFragment {
		t.Fatalf("기억한 경로가 %q입니다", got)
	}
	if got := chain.ModeFor("cdn.donmai.us"); got != domain.NetDirect {
		t.Fatalf("기억한 경로가 %q입니다", got)
	}
}

// 기억한 경로가 맨 앞에 오고 나머지가 뒤따라야 합니다.
func TestPrimedPathIsTriedFirst(t *testing.T) {
	chain := testChain(t, domain.NetDirect, domain.NetFragment)
	chain.Prime(map[string]domain.NetMode{"danbooru.donmai.us": domain.NetFragment})

	order := chain.candidates("danbooru.donmai.us")
	if len(order) != 2 || order[0] != domain.NetFragment {
		t.Fatalf("시도 순서가 %v입니다", order)
	}
}

// 준비되지 않은 경로는 조용히 무시해야 합니다.
// VPN 주소가 없으면 그 경로 자체가 만들어지지 않습니다.
func TestPrimeIgnoresUnavailablePath(t *testing.T) {
	chain := testChain(t, domain.NetDirect, domain.NetFragment)

	applied := chain.Prime(map[string]domain.NetMode{
		"danbooru.donmai.us": domain.NetProxy,
		"cdn.donmai.us":      domain.NetFragment,
	})
	if applied != 1 {
		t.Fatalf("%d개가 적용됐습니다. 1개여야 합니다", applied)
	}
	if got := chain.ModeFor("danbooru.donmai.us"); got != domain.NetDirect {
		t.Fatalf("쓸 수 없는 경로가 %q로 들어갔습니다", got)
	}
}

func TestPrimeEmptyIsHarmless(t *testing.T) {
	chain := testChain(t, domain.NetDirect)
	if applied := chain.Prime(nil); applied != 0 {
		t.Fatalf("빈 입력에서 %d개가 적용됐습니다", applied)
	}
}
