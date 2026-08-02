package netpath

import (
	"encoding/binary"
	"errors"
	"net"
	"sort"
	"time"
)

var errNoSNI = errors.New("ClientHello에서 도메인 이름을 찾지 못했습니다")

// tlsRecordHeader는 TLS 레코드 머리말 길이입니다.
// 이 뒤에서 한 번 끊으면 검사 장비가 첫 조각만 보고는 레코드 안을 볼 수 없습니다.
const tlsRecordHeader = 5

// sniRange는 TLS ClientHello 안에서 도메인 이름 문자열이 차지하는 구간을 찾습니다.
// 조각을 그 안에서 끊으면 검사 장비가 도메인을 알아보지 못합니다.
func sniRange(buf []byte) (start, end int, err error) {
	r := reader{buf: buf}

	if r.u8() != 0x16 {
		return 0, 0, errNoSNI
	}
	r.skip(2)
	r.skip(2)

	if r.u8() != 0x01 {
		return 0, 0, errNoSNI
	}
	r.skip(3)
	r.skip(2)
	r.skip(32)

	r.skip(int(r.u8()))
	r.skip(int(r.u16()))
	r.skip(int(r.u8()))

	extTotal := int(r.u16())
	if r.err != nil {
		return 0, 0, errNoSNI
	}

	limit := r.pos + extTotal
	for r.pos < limit && r.err == nil {
		extType := r.u16()
		extLen := int(r.u16())
		if extType != 0x0000 {
			r.skip(extLen)
			continue
		}

		r.skip(2)
		if r.u8() != 0x00 {
			return 0, 0, errNoSNI
		}
		nameLen := int(r.u16())
		if r.err != nil || nameLen <= 0 || r.pos+nameLen > len(buf) {
			return 0, 0, errNoSNI
		}
		return r.pos, r.pos + nameLen, nil
	}
	return 0, 0, errNoSNI
}

type reader struct {
	buf []byte
	pos int
	err error
}

func (r *reader) u8() byte {
	if r.err != nil || r.pos+1 > len(r.buf) {
		r.err = errNoSNI
		return 0
	}
	v := r.buf[r.pos]
	r.pos++
	return v
}

func (r *reader) u16() uint16 {
	if r.err != nil || r.pos+2 > len(r.buf) {
		r.err = errNoSNI
		return 0
	}
	v := binary.BigEndian.Uint16(r.buf[r.pos:])
	r.pos += 2
	return v
}

func (r *reader) skip(n int) {
	if r.err != nil || n < 0 || r.pos+n > len(r.buf) {
		r.err = errNoSNI
		return
	}
	r.pos += n
}

// fragmentConn은 첫 번째 쓰기, 즉 TLS ClientHello만 여러 조각으로 나눠 보냅니다.
// 그 뒤의 통신은 그대로 흘려보냅니다.
//
// 조각을 둘로만 나누면 조각을 다시 합쳐서 보는 검사 장비에는 통하지 않습니다.
// 그래서 레코드 머리말 뒤에서 한 번, 도메인 이름 안에서 여러 번 끊습니다.
// 사이에 아주 짧은 지연을 두면 각 조각이 별도 TCP 세그먼트로 나갈 확률이 높아집니다.
type fragmentConn struct {
	net.Conn
	split bool
	delay time.Duration
	parts int
}

func (c *fragmentConn) Write(b []byte) (int, error) {
	if c.split {
		return c.Conn.Write(b)
	}
	c.split = true

	points := splitPoints(b, c.parts)
	if len(points) == 0 {
		return c.Conn.Write(b)
	}

	var written int
	prev := 0
	for _, at := range append(points, len(b)) {
		if at <= prev {
			continue
		}
		n, err := c.Conn.Write(b[prev:at])
		written += n
		if err != nil {
			return written, err
		}
		if c.delay > 0 && at < len(b) {
			time.Sleep(c.delay)
		}
		prev = at
	}
	return written, nil
}

// splitPoints는 어디서 끊을지 정합니다.
//
// 레코드 머리말 바로 뒤에서 한 번 끊어 첫 조각을 아주 작게 만들고,
// 도메인 이름 안을 균등하게 나눕니다. 이름을 못 찾으면 머리말 뒤만 끊습니다.
func splitPoints(b []byte, parts int) []int {
	if len(b) < tlsRecordHeader*2 {
		return nil
	}
	if parts < 2 {
		parts = 2
	}

	seen := map[int]bool{}
	var points []int
	add := func(at int) {
		if at > 0 && at < len(b) && !seen[at] {
			seen[at] = true
			points = append(points, at)
		}
	}

	// 첫 조각은 레코드 머리말까지만 보냅니다.
	add(tlsRecordHeader)

	start, end, err := sniRange(b)
	if err == nil && end-start >= 2 {
		// 이름 안을 parts-1 조각으로 나눕니다.
		pieces := parts - 1
		if pieces > end-start {
			pieces = end - start
		}
		width := (end - start) / (pieces + 1)
		if width < 1 {
			width = 1
		}
		for i := 1; i <= pieces && start+i*width < end; i++ {
			add(start + i*width)
		}
	}

	sort.Ints(points)
	return points
}
