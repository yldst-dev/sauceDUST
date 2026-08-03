package flatindex

import (
	"math/bits"
	"math/rand"
	"sort"
	"testing"
)

// memReader는 시험용으로 메모리에 든 코드를 냅니다.
type memReader struct {
	data []byte
	copy bool
}

func (m *memReader) chunk(from, to int, buf []byte) ([]byte, error) {
	if !m.copy {
		return m.data[from:to], nil
	}
	n := copy(buf, m.data[from:to])
	return buf[:n], nil
}
func (m *memReader) zeroCopy() bool { return !m.copy }
func (m *memReader) close() error   { return nil }

// reference는 전부 재서 정렬한 참값입니다.
func reference(data, query []byte, count, codeBytes, want int) []candidate {
	all := make([]candidate, count)
	for i := 0; i < count; i++ {
		var d int
		for b := 0; b < codeBytes; b++ {
			d += bits.OnesCount8(data[i*codeBytes+b] ^ query[b])
		}
		all[i] = candidate{slot: i, dist: uint16(d)}
	}
	sort.Slice(all, func(a, b int) bool {
		if all[a].dist != all[b].dist {
			return all[a].dist < all[b].dist
		}
		return all[a].slot < all[b].slot
	})
	if len(all) > want {
		all = all[:want]
	}
	return all
}

// 잘라내기 때문에 정답을 놓치지 않아야 합니다.
// 문턱을 잘못 두면 조용히 덜 찾습니다.
func TestScanMatchesReference(t *testing.T) {
	const codeBytes = 96
	rng := rand.New(rand.NewSource(31))

	for _, count := range []int{1, 5, 39, 40, 41, 1000, 200000} {
		for _, want := range []int{1, 10, 40} {
			data := make([]byte, count*codeBytes)
			rng.Read(data)
			query := make([]byte, codeBytes)
			rng.Read(query)

			for _, copyMode := range []bool{false, true} {
				r := &memReader{data: data, copy: copyMode}
				got, err := scan(r, query, count, codeBytes, want)
				if err != nil {
					t.Fatalf("점 %d개 상위 %d개: %v", count, want, err)
				}
				exp := reference(data, query, count, codeBytes, want)

				if len(got) != len(exp) {
					t.Fatalf("점 %d개 상위 %d개: %d건이 왔는데 %d건이어야 합니다",
						count, want, len(got), len(exp))
				}
				// 거리가 같은 것끼리는 순서가 갈릴 수 있으므로 거리만 봅니다.
				for i := range got {
					if got[i].dist != exp[i].dist {
						t.Fatalf("점 %d개 상위 %d개: %d번째 거리가 %d인데 %d여야 합니다",
							count, want, i, got[i].dist, exp[i].dist)
					}
				}
			}
		}
	}
}

// 8바이트씩 세는 빠른 길과 한 바이트씩 세는 길이 같아야 합니다.
func TestHammingMatchesByteByByte(t *testing.T) {
	rng := rand.New(rand.NewSource(41))
	for _, n := range []int{1, 7, 8, 9, 64, 96, 99} {
		a := make([]byte, n)
		b := make([]byte, n)
		for round := 0; round < 20; round++ {
			rng.Read(a)
			rng.Read(b)

			var want int
			for i := 0; i < n; i++ {
				want += bits.OnesCount8(a[i] ^ b[i])
			}
			if got := hamming(a, b); int(got) != want {
				t.Fatalf("길이 %d에서 %d인데 %d여야 합니다", n, got, want)
			}
		}
	}
}

// 같은 코드끼리는 거리가 0이어야 합니다.
func TestHammingOfIdenticalCodesIsZero(t *testing.T) {
	rng := rand.New(rand.NewSource(43))
	a := make([]byte, 96)
	rng.Read(a)
	if d := hamming(a, a); d != 0 {
		t.Fatalf("같은 코드인데 거리가 %d입니다", d)
	}
}

// 부호가 양수인 자리만 비트가 서야 합니다.
func TestEncodeSetsBitsForPositiveValues(t *testing.T) {
	v := []float32{1, -1, 0, 2, -0.5, 3, -3, 0.1, 5}
	code := encode(v, (len(v)+7)/8)

	for i, x := range v {
		set := code[i/8]&(1<<(i%8)) != 0
		if want := x > 0; set != want {
			t.Errorf("%d번 값이 %v인데 비트가 %v입니다", i, x, set)
		}
	}
}

// 차원이 8의 배수가 아니어도 남는 비트는 0이어야 합니다.
func TestEncodePadsTailBitsWithZero(t *testing.T) {
	v := make([]float32, 12)
	for i := range v {
		v[i] = 1
	}
	code := encode(v, 2)
	if code[0] != 0xFF {
		t.Errorf("앞 여덟 자리가 %08b입니다", code[0])
	}
	if code[1] != 0x0F {
		t.Errorf("뒤 네 자리와 남는 비트가 %08b입니다. 00001111이어야 합니다", code[1])
	}
}
