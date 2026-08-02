package netpath

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"saucedust/internal/domain"
)

func TestPublicIPRejectsInternalRanges(t *testing.T) {
	blocked := map[string]string{
		"127.0.0.1":       "루프백",
		"::1":             "IPv6 루프백",
		"10.0.0.5":        "사설 A",
		"172.16.0.1":      "사설 B",
		"192.168.1.1":     "사설 C",
		"169.254.169.254": "클라우드 메타데이터",
		"169.254.0.1":     "링크로컬",
		"0.0.0.0":         "미지정",
		"100.64.0.1":      "통신사 공유",
		"198.18.0.1":      "성능 시험용",
		"240.0.0.1":       "예약",
		"fc00::1":         "유니크 로컬",
		"fe80::1":         "IPv6 링크로컬",
		"224.0.0.1":       "멀티캐스트",
	}
	for raw, why := range blocked {
		if PublicIP(net.ParseIP(raw)) {
			t.Errorf("%s(%s)를 통과시켰습니다", raw, why)
		}
	}
}

func TestPublicIPAllowsRealAddresses(t *testing.T) {
	allowed := []string{
		"1.1.1.1", "8.8.8.8", "104.18.0.1", "2606:4700::1111",
	}
	for _, raw := range allowed {
		if !PublicIP(net.ParseIP(raw)) {
			t.Errorf("%s를 막았습니다", raw)
		}
	}
}

func TestPublicIPRejectsNil(t *testing.T) {
	if PublicIP(nil) {
		t.Fatal("빈 주소를 통과시켰습니다")
	}
}

func guardedChain(t *testing.T, allowPrivate bool) *Chain {
	t.Helper()
	chain, err := New(Options{
		Order:        []domain.NetMode{domain.NetDirect},
		AllowPrivate: allowPrivate,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("생성 실패: %v", err)
	}
	return chain
}

// 수집 대상이 알려준 주소를 그대로 받아오므로, 그 응답이 조작되면
// 우리 노드가 내부망을 찌르는 도구가 됩니다. 실제로 막히는지 확인합니다.
func TestChainBlocksLoopbackTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("내부 주소에 실제로 요청이 닿았습니다")
	}))
	defer server.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("요청 생성 실패: %v", err)
	}

	if _, err := guardedChain(t, false).Do(req); err == nil {
		t.Fatal("루프백 주소로 나갔습니다")
	}
}

// 사내 미러를 쓰는 경우를 위해 열 수 있어야 합니다.
func TestChainAllowsPrivateWhenPermitted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	resp, err := guardedChain(t, true).Do(req)
	if err != nil {
		t.Fatalf("허용했는데도 막혔습니다: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("응답이 %d입니다", resp.StatusCode)
	}
}

func TestDialGuardIsNilWhenPermitted(t *testing.T) {
	if dialGuard(true) != nil {
		t.Fatal("허용 상태에서는 검사 자체를 붙이지 말아야 합니다")
	}
	if dialGuard(false) == nil {
		t.Fatal("막는 상태에서는 검사가 있어야 합니다")
	}
}

func TestDialGuardRejectsMalformedAddress(t *testing.T) {
	guard := dialGuard(false)

	for _, address := range []string{"주소아님", "127.0.0.1", ""} {
		if err := guard("tcp", address, nil); err == nil {
			t.Errorf("%q를 통과시켰습니다", address)
		} else if !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("%q에서 예상 밖 오류: %v", address, err)
		}
	}
}

func TestDialGuardAllowsPublicAddress(t *testing.T) {
	if err := dialGuard(false)("tcp", "1.1.1.1:443", nil); err != nil {
		t.Fatalf("공인 주소를 막았습니다: %v", err)
	}
}
