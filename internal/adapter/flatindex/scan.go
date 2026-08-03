package flatindex

import (
	"math/bits"
	"runtime"
	"sort"
	"sync"
)

// codeReader는 코드 파일을 읽는 방법입니다. 자리에 걸어 두는 곳과 읽어
// 오는 곳이 달라서 갈라 둡니다.
type codeReader interface {
	chunk(from, to int, buf []byte) ([]byte, error)
	zeroCopy() bool
	close() error
}

// candidate는 훑어서 나온 후보 하나입니다.
// slot은 파일에서의 자리이고 dist는 다른 비트 수입니다.
type candidate struct {
	slot int
	dist uint16
}

// scanChunkSlots는 한 일꾼이 한 번에 읽어 오는 슬롯 수입니다.
//
// 걸어 두지 못하는 곳에서 버퍼 크기를 정합니다. 768차원이면 96바이트씩
// 이므로 6MB쯤입니다. 너무 작으면 읽기 횟수가 늘고, 너무 크면 버퍼가
// 힙을 차지합니다.
const scanChunkSlots = 64 << 10

// scan은 코드 전체를 훑어 가장 가까운 want개를 냅니다.
//
// 그래프를 타지 않고 전부 봅니다. 느려 보이지만 이진 코드는 장당 96바이트
// 뿐이라 1,190만 장이 1GB 남짓이고, 두 코어로 40밀리초쯤에 끝납니다.
// 무엇보다 반드시 램에 있어야 하는 몫이 없습니다.
func scan(r codeReader, query []byte, count, codeBytes, want int) ([]candidate, error) {
	if count <= 0 || want <= 0 {
		return nil, nil
	}

	workers := runtime.GOMAXPROCS(0)
	if workers > count {
		workers = count
	}
	per := (count + workers - 1) / workers

	var (
		mu   sync.Mutex
		errs error
		all  []candidate
		wg   sync.WaitGroup
	)
	for w := 0; w < workers; w++ {
		lo := w * per
		hi := lo + per
		if hi > count {
			hi = count
		}
		if lo >= hi {
			continue
		}

		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			part, err := scanRange(r, query, lo, hi, codeBytes, want)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if errs == nil {
					errs = err
				}
				return
			}
			all = append(all, part...)
		}(lo, hi)
	}
	wg.Wait()

	if errs != nil {
		return nil, errs
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].dist != all[j].dist {
			return all[i].dist < all[j].dist
		}
		return all[i].slot < all[j].slot
	})
	if len(all) > want {
		all = all[:want]
	}
	return all, nil
}

// scanRange는 한 구간을 훑습니다.
//
// 가장 나쁜 후보의 거리를 문턱으로 두고 그보다 나쁜 것은 바로 버립니다.
// 상위 want개만 들고 있으므로 점이 아무리 많아도 힙이 늘지 않습니다.
func scanRange(r codeReader, query []byte, lo, hi, codeBytes, want int) ([]candidate, error) {
	best := make([]candidate, 0, want+1)
	worst := uint16(1<<16 - 1)

	var buf []byte
	if !r.zeroCopy() {
		buf = make([]byte, scanChunkSlots*codeBytes)
	}

	for start := lo; start < hi; start += scanChunkSlots {
		end := start + scanChunkSlots
		if end > hi {
			end = hi
		}
		block, err := r.chunk(start*codeBytes, end*codeBytes, buf)
		if err != nil {
			return nil, err
		}

		for i := start; i < end; i++ {
			off := (i - start) * codeBytes
			d := hamming(block[off:off+codeBytes], query)
			if len(best) == want && d >= worst {
				continue
			}

			best = append(best, candidate{slot: i, dist: d})
			if len(best) > want {
				sort.Slice(best, func(a, b int) bool { return best[a].dist < best[b].dist })
				best = best[:want]
				worst = best[want-1].dist
			}
		}
	}

	sort.Slice(best, func(a, b int) bool { return best[a].dist < best[b].dist })
	return best, nil
}

// hamming은 두 코드가 몇 비트나 다른지 셉니다.
//
// 8바이트씩 묶어 셉니다. popcount는 명령 하나라, 96바이트가 12번이면
// 끝납니다. 이 부분이 전체 시간의 대부분이므로 바이트 단위로 돌리면
// 여덟 배 느려집니다.
func hamming(a, b []byte) uint16 {
	var d int
	n := len(a)
	i := 0
	for ; i+8 <= n; i += 8 {
		d += bits.OnesCount64(le64(a[i:]) ^ le64(b[i:]))
	}
	for ; i < n; i++ {
		d += bits.OnesCount8(a[i] ^ b[i])
	}
	return uint16(d)
}

func le64(b []byte) uint64 {
	_ = b[7]
	return uint64(b[0]) | uint64(b[1])<<8 | uint64(b[2])<<16 | uint64(b[3])<<24 |
		uint64(b[4])<<32 | uint64(b[5])<<40 | uint64(b[6])<<48 | uint64(b[7])<<56
}
