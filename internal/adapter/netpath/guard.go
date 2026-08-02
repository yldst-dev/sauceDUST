package netpath

import (
	"errors"
	"fmt"
	"net"
	"syscall"
)

// ErrBlockedAddress는 내부 주소로 나가려는 요청을 막았을 때 냅니다.
var ErrBlockedAddress = errors.New("내부 주소로는 나가지 않습니다")

// 크롤러는 수집 대상이 알려준 주소를 그대로 받아옵니다.
// 그 응답이 조작되면 우리 노드가 내부망을 찌르는 도구가 됩니다.
// PostgreSQL, Qdrant, 임베딩 워커가 전부 사설 주소에 있으므로 실제 위험입니다.
//
// 이름 조회 결과를 보고 막습니다. 도메인만 보면 DNS를 나중에 바꿔치기하는
// 수법을 놓칩니다.

// dialGuard는 연결 직전에 실제 주소를 확인합니다.
// net.Dialer.Control은 소켓을 만든 뒤 연결하기 전에 불리므로 이 검사에 맞습니다.
func dialGuard(allowPrivate bool) func(network, address string, _ syscall.RawConn) error {
	if allowPrivate {
		return nil
	}
	return func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("%w: 주소를 해석하지 못했습니다 (%s)", ErrBlockedAddress, address)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("%w: IP가 아닙니다 (%s)", ErrBlockedAddress, host)
		}
		if !PublicIP(ip) {
			return fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
		}
		return nil
	}
}

// PublicIP는 바깥으로 나가도 되는 주소인지 알려줍니다.
func PublicIP(ip net.IP) bool {
	switch {
	case ip == nil,
		ip.IsLoopback(),
		ip.IsPrivate(),
		ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsInterfaceLocalMulticast(),
		ip.IsMulticast(),
		ip.IsUnspecified():
		return false
	}

	// 클라우드 메타데이터 주소는 링크로컬이라 위에서 걸리지만,
	// 이 값이 자주 노려지므로 따로 한 번 더 막습니다.
	if ip.Equal(net.IPv4(169, 254, 169, 254)) {
		return false
	}

	// IPv4 사설 대역 밖의 예약 대역들입니다.
	for _, block := range reservedBlocks {
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

// reservedBlocks는 공인 인터넷에 쓰이지 않는 대역입니다.
var reservedBlocks = func() []*net.IPNet {
	raw := []string{
		"0.0.0.0/8",       // 현재 네트워크
		"100.64.0.0/10",   // 통신사 내부 공유 주소
		"192.0.0.0/24",    // 프로토콜 지정용
		"192.0.2.0/24",    // 문서용
		"198.18.0.0/15",   // 성능 시험용
		"198.51.100.0/24", // 문서용
		"203.0.113.0/24",  // 문서용
		"240.0.0.0/4",     // 예약
		"::/128",          // 미지정
		"64:ff9b::/96",    // IPv4 변환
		"100::/64",        // 폐기 대상
		"2001:db8::/32",   // 문서용
		"fc00::/7",        // 유니크 로컬
	}
	out := make([]*net.IPNet, 0, len(raw))
	for _, cidr := range raw {
		if _, block, err := net.ParseCIDR(cidr); err == nil {
			out = append(out, block)
		}
	}
	return out
}()
