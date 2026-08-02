package netpath

import (
	"bytes"
	"crypto/tls"
	"net"
	"testing"
	"time"
)

// captureConn은 ClientHello 바이트만 받아 두고 핸드셰이크는 실패시킵니다.
type captureConn struct {
	net.Conn
	writes [][]byte
}

func (c *captureConn) Write(b []byte) (int, error) {
	c.writes = append(c.writes, append([]byte(nil), b...))
	return len(b), nil
}

func (c *captureConn) Read([]byte) (int, error)         { return 0, net.ErrClosed }
func (c *captureConn) Close() error                     { return nil }
func (c *captureConn) SetDeadline(time.Time) error      { return nil }
func (c *captureConn) SetReadDeadline(time.Time) error  { return nil }
func (c *captureConn) SetWriteDeadline(time.Time) error { return nil }

func realClientHello(t *testing.T, serverName string) []byte {
	t.Helper()

	capture := &captureConn{}
	conn := tls.Client(capture, &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12})
	_ = conn.Handshake()

	if len(capture.writes) == 0 {
		t.Fatal("ClientHello를 얻지 못했습니다")
	}
	return capture.writes[0]
}

func TestSNIRangeFindsHostname(t *testing.T) {
	const name = "danbooru.donmai.us"
	hello := realClientHello(t, name)

	start, end, err := sniRange(hello)
	if err != nil {
		t.Fatalf("도메인 이름을 찾지 못했습니다: %v", err)
	}
	if got := string(hello[start:end]); got != name {
		t.Fatalf("찾은 이름이 %q입니다. %q를 기대했습니다", got, name)
	}
}

// 첫 조각은 TLS 레코드 머리말까지만이어야 합니다.
// 그래야 검사 장비가 첫 조각만으로는 레코드 안을 들여다볼 수 없습니다.
func TestFirstFragmentStopsAtRecordHeader(t *testing.T) {
	hello := realClientHello(t, "danbooru.donmai.us")

	points := splitPoints(hello, 3)
	if len(points) == 0 {
		t.Fatal("분할 지점을 찾지 못했습니다")
	}
	if points[0] != tlsRecordHeader {
		t.Fatalf("첫 분할이 %d입니다. 레코드 머리말 %d를 기대했습니다",
			points[0], tlsRecordHeader)
	}
}

// 도메인 이름 안에서도 끊어야 이름이 한 조각에 온전히 담기지 않습니다.
func TestSplitPointsFallInsideHostname(t *testing.T) {
	const name = "danbooru.donmai.us"
	hello := realClientHello(t, name)

	start, end, err := sniRange(hello)
	if err != nil {
		t.Fatalf("도메인 이름을 찾지 못했습니다: %v", err)
	}

	var inside int
	for _, at := range splitPoints(hello, 3) {
		if at > start && at < end {
			inside++
		}
	}
	if inside < 2 {
		t.Fatalf("이름 안 분할이 %d개입니다. 2개 이상을 기대했습니다", inside)
	}
}

// 조각내기를 해도 상대가 받는 바이트 전체는 원본과 같아야 합니다.
func TestFragmentConnPreservesBytes(t *testing.T) {
	const name = "danbooru.donmai.us"
	hello := realClientHello(t, name)

	capture := &captureConn{}
	frag := &fragmentConn{Conn: capture, parts: 3}

	n, err := frag.Write(hello)
	if err != nil {
		t.Fatalf("쓰기 실패: %v", err)
	}
	if n != len(hello) {
		t.Fatalf("쓴 길이가 %d입니다. %d를 기대했습니다", n, len(hello))
	}
	if len(capture.writes) < 3 {
		t.Fatalf("조각이 %d개입니다. 3개 이상을 기대했습니다", len(capture.writes))
	}

	joined := bytes.Join(capture.writes, nil)
	if !bytes.Equal(joined, hello) {
		t.Fatal("조각을 합친 결과가 원본과 다릅니다")
	}

	// 어느 한 조각에도 도메인 이름이 통째로 들어 있으면 안 됩니다.
	for i, piece := range capture.writes {
		if bytes.Contains(piece, []byte(name)) {
			t.Fatalf("%d번째 조각에 도메인 이름이 그대로 들어 있습니다", i)
		}
	}
}

// 조각 수를 늘리면 실제로 더 많이 나뉘어야 합니다.
func TestFragmentPartsIncreasesPieces(t *testing.T) {
	hello := realClientHello(t, "danbooru.donmai.us")

	counts := map[int]int{}
	for _, parts := range []int{2, 4, 6} {
		capture := &captureConn{}
		frag := &fragmentConn{Conn: capture, parts: parts}
		if _, err := frag.Write(hello); err != nil {
			t.Fatalf("쓰기 실패: %v", err)
		}
		counts[parts] = len(capture.writes)
	}

	if counts[4] <= counts[2] || counts[6] <= counts[4] {
		t.Fatalf("조각 수가 늘지 않았습니다: %v", counts)
	}
}

// 두 번째 쓰기부터는 손대지 않아야 합니다.
func TestFragmentConnOnlySplitsFirstWrite(t *testing.T) {
	capture := &captureConn{}
	frag := &fragmentConn{Conn: capture, parts: 3}

	if _, err := frag.Write(realClientHello(t, "example.com")); err != nil {
		t.Fatalf("첫 쓰기 실패: %v", err)
	}
	before := len(capture.writes)

	if _, err := frag.Write([]byte("application data payload")); err != nil {
		t.Fatalf("두 번째 쓰기 실패: %v", err)
	}
	if got := len(capture.writes) - before; got != 1 {
		t.Fatalf("두 번째 쓰기가 %d조각으로 나뉘었습니다. 1을 기대했습니다", got)
	}
}

// 이름을 못 찾아도 레코드 머리말 뒤에서는 끊어야 합니다.
func TestSplitPointsWithoutSNI(t *testing.T) {
	// SNI 확장이 없는 ClientHello를 흉내 냅니다.
	buf := make([]byte, 200)
	buf[0] = 0x16
	buf[5] = 0x01

	points := splitPoints(buf, 3)
	if len(points) != 1 || points[0] != tlsRecordHeader {
		t.Fatalf("분할 지점이 %v입니다. [%d]를 기대했습니다", points, tlsRecordHeader)
	}
}

func TestSplitPointsIgnoresTinyInput(t *testing.T) {
	if points := splitPoints([]byte{0x16, 0x03}, 3); points != nil {
		t.Fatalf("너무 짧은 입력에서 %v가 나왔습니다", points)
	}
}

func TestSNIRangeRejectsGarbage(t *testing.T) {
	cases := map[string][]byte{
		"빈 입력":     {},
		"짧은 입력":    {0x16, 0x03},
		"핸드셰이크 아님": {0x17, 0x03, 0x01, 0x00, 0x10, 0x01},
		"길이 초과":    {0x16, 0x03, 0x01, 0xff, 0xff, 0x01, 0xff, 0xff, 0xff},
	}
	for name, input := range cases {
		if _, _, err := sniRange(input); err == nil {
			t.Errorf("%s에서 오류가 나야 합니다", name)
		}
	}
}

func TestParseECHParam(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{`1 . alpn="h3,h2" ech="AEX+DQBBAA" ipv4hint=1.2.3.4`, "AEX+DQBBAA", true},
		{`1 . ech=AEX+DQBBAA`, "AEX+DQBBAA", true},
		{`1 . alpn="h2"`, "", false},
		{`1 .`, "", false},
	}
	for _, tc := range cases {
		got, ok := parseECHParam(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%q에서 (%q, %v)를 얻었습니다. (%q, %v)를 기대했습니다",
				tc.in, got, ok, tc.want, tc.ok)
		}
	}
}
