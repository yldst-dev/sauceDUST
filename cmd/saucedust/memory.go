package main

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// 이 컴퓨터가 가진 메모리를 봅니다.
//
// Qdrant가 담을 수 있는 장수를 일러 주기 위해서입니다. 상한을 두지 않으면
// 어느 순간 OOM으로 죽고, 다시 띄워도 컬렉션을 여는 것 자체가 한도를 넘어
// 또 죽습니다. 그때는 지우는 것 말고 방법이 없으므로 미리 알려야 합니다.
//
// 중앙 노드에서만 뜻이 있습니다. 작업 노드가 제 메모리를 봐도 소용없습니다.
// Qdrant는 중앙에 있습니다.

// totalMemoryBytes는 쓸 수 있는 메모리를 냅니다. 알아내지 못하면 0입니다.
//
// 컨테이너나 가상 기계 안이면 cgroup 상한이 실제 천장입니다. /proc/meminfo는
// 호스트 전체를 보여 주므로 그것만 믿으면 없는 메모리를 세게 됩니다.
func totalMemoryBytes() int64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	total := memInfoTotal("/proc/meminfo")
	if limit := cgroupLimit(); limit > 0 && (total == 0 || limit < total) {
		return limit
	}
	return total
}

func memInfoTotal(path string) int64 {
	f, err := os.Open(path) // #nosec G304 -- 붙박이 시스템 경로만 넘깁니다. 인자로 받는 것은 시험 때문입니다
	if err != nil {
		return 0
	}
	defer f.Close()

	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		return parseMemInfoLine(line)
	}
	return 0
}

// parseMemInfoLine은 "MemTotal:  16311456 kB" 같은 줄에서 바이트를 뽑습니다.
func parseMemInfoLine(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	kb, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || kb <= 0 {
		return 0
	}
	return kb * 1024
}

// cgroupLimit은 컨테이너에 걸린 메모리 상한입니다. 없으면 0입니다.
func cgroupLimit() int64 {
	// cgroup v2가 먼저입니다. 상한이 없으면 "max"라고 적혀 있습니다.
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	} {
		data, err := os.ReadFile(path) // #nosec G304 -- 바로 위 목록의 붙박이 경로뿐입니다
		if err != nil {
			continue
		}
		text := strings.TrimSpace(string(data))
		if text == "max" {
			continue
		}
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil || n <= 0 {
			continue
		}
		// v1은 상한이 없을 때 어마어마한 수를 적어 둡니다.
		if n > 1<<50 {
			continue
		}
		return n
	}
	return 0
}

// qdrantShareBytes는 전체 메모리 중 Qdrant에게 갈 몫을 어림합니다.
//
// 나머지는 운영체제, PostgreSQL, 워커, saucedust가 씁니다. 실측한 값들을
// 더한 것입니다. 워커가 두 모델을 올리고 435 MB였습니다.
func qdrantShareBytes(total int64) int64 {
	const others = (700 + 500 + 200 + 1500) << 20
	if total <= others {
		return 0
	}
	return total - others
}
