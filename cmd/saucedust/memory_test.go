package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMemInfoLine(t *testing.T) {
	tests := []struct {
		line string
		want int64
	}{
		{"MemTotal:       16311456 kB", 16311456 * 1024},
		{"MemTotal:  8000000 kB", 8000000 * 1024},
		{"MemTotal:", 0},
		{"MemTotal:       없음 kB", 0},
		{"MemTotal:       -5 kB", 0},
		{"", 0},
	}

	for _, tc := range tests {
		if got := parseMemInfoLine(tc.line); got != tc.want {
			t.Errorf("%q에서 %d가 나왔습니다. %d를 기대했습니다", tc.line, got, tc.want)
		}
	}
}

func TestMemInfoTotalReadsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meminfo")
	body := "MemFree:         123456 kB\nMemTotal:       8388608 kB\nBuffers:  1 kB\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := memInfoTotal(path); got != 8388608*1024 {
		t.Errorf("%d가 나왔습니다", got)
	}
	if got := memInfoTotal(filepath.Join(t.TempDir(), "없음")); got != 0 {
		t.Errorf("없는 파일에서 %d가 나왔습니다", got)
	}
}

// 나머지 몫보다 적으면 Qdrant에 줄 것이 없습니다.
// 음수가 나오면 담을 장수 계산이 뒤집힙니다.
func TestQdrantShareNeverNegative(t *testing.T) {
	const mb = 1 << 20
	for _, total := range []int64{0, 100 * mb, 2900 * mb} {
		if got := qdrantShareBytes(total); got < 0 {
			t.Errorf("%d바이트에서 %d가 나왔습니다", total, got)
		}
	}

	eight := qdrantShareBytes(8 << 30)
	if eight < 3<<30 || eight > 6<<30 {
		t.Errorf("8 GB에서 Qdrant 몫이 %d바이트입니다. 4 GB 근처를 기대했습니다", eight)
	}
}
