package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"saucedust/internal/adapter/danbooru"
)

// 이 프로그램의 오류 메시지는 전부 한국어입니다. 바이트로 자르면
// 글자 중간이 끊겨 터미널에 깨진 문자가 찍힙니다.
func TestTruncateKeepsRunesWhole(t *testing.T) {
	inputs := []string{
		"짧은 글",
		"연결하지 못했습니다. 상대가 응답하지 않습니다. 잠시 뒤 다시 시도하십시오.",
		strings.Repeat("한", 100),
		strings.Repeat("🎨", 100),
		strings.Repeat("a", 100),
		"섞임abc한글🎨mixed",
	}

	for _, in := range inputs {
		for _, n := range []int{1, 2, 3, 4, 10, 44, 60, 70} {
			got := truncate(in, n)
			if !utf8.ValidString(got) {
				t.Fatalf("%q를 %d로 자르니 글자가 깨졌습니다: %q", in, n, got)
			}
			if strings.ContainsRune(got, '�') {
				t.Fatalf("%q를 %d로 자르니 대체 문자가 들어갔습니다", in, n)
			}
			if len(in) <= n && got != in {
				t.Fatalf("자를 필요가 없는데 %q로 바뀌었습니다", got)
			}
		}
	}
}

func TestTruncateStaysNearBudget(t *testing.T) {
	// 표 칸을 넘지 않아야 정렬이 무너지지 않습니다.
	// 말줄임표 세 바이트만큼은 넘을 수 있습니다.
	long := strings.Repeat("한", 100)
	for _, n := range []int{10, 44, 70} {
		got := truncate(long, n)
		if len(got) > n+len("…") {
			t.Errorf("%d로 잘랐는데 %d바이트입니다", n, len(got))
		}
	}
}

func TestComma(t *testing.T) {
	tests := map[int64]string{
		0:                   "0",
		7:                   "7",
		999:                 "999",
		1000:                "1,000",
		11907690:            "11,907,690",
		1234567890:          "1,234,567,890",
		-1:                  "-1",
		-123:                "-123",
		-1234:               "-1,234",
		-11907690:           "-11,907,690",
		9223372036854775807: "9,223,372,036,854,775,807",
	}

	for input, want := range tests {
		if got := comma(input); got != want {
			t.Errorf("%d가 %q입니다. %q를 기대했습니다", input, got, want)
		}
	}
}

func TestCommaNeverStartsWithSeparator(t *testing.T) {
	for v := int64(-2000); v <= 2000; v++ {
		got := comma(v)
		if strings.HasPrefix(got, ",") || strings.HasPrefix(got, "-,") {
			t.Fatalf("%d가 %q입니다", v, got)
		}
	}
}

func TestParallelSteps(t *testing.T) {
	tests := []struct {
		limit int
		want  []int
	}{
		{1, []int{1}},
		{8, []int{1, 4, 8}},
		{16, []int{1, 4, 8, 16}},
		{100, []int{1, 4, 8, 16, 32, 64}},
		{0, []int{0}},
	}

	for _, tc := range tests {
		got := parallelSteps(tc.limit)
		if len(got) != len(tc.want) {
			t.Fatalf("%d에서 %v입니다. %v를 기대했습니다", tc.limit, got, tc.want)
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Fatalf("%d에서 %v입니다. %v를 기대했습니다", tc.limit, got, tc.want)
			}
		}
	}
}

func TestYesNo(t *testing.T) {
	if yesNo(true) == yesNo(false) {
		t.Error("참과 거짓이 같게 나옵니다")
	}
}

func TestBytesOf(t *testing.T) {
	tests := map[int64]string{
		0:                  "-",
		-1:                 "-",
		512:                "512B",
		1024:               "1KB",
		1024 * 1024:        "1.0MB",
		1024 * 1024 * 1024: "1024.0MB",
	}
	for input, want := range tests {
		if got := bytesOf(input); got != want {
			t.Errorf("%d가 %q입니다. %q를 기대했습니다", input, got, want)
		}
	}
}

func TestMillis(t *testing.T) {
	if got := millis(1500 * time.Millisecond); !strings.Contains(got, "1500") &&
		!strings.Contains(got, "1,500") {
		t.Errorf("1.5초가 %q입니다", got)
	}
}

func TestDashReplacesEmpty(t *testing.T) {
	if dash("") == "" {
		t.Error("빈 값이 그대로 나옵니다. 표에서 칸이 비어 보입니다")
	}
	if got := dash("http/2"); got != "http/2" {
		t.Errorf("값이 있는데 %q로 바뀌었습니다", got)
	}
}

func TestSortedKeys(t *testing.T) {
	got := sortedKeys(map[string]int{"c": 1, "a": 2, "b": 3})
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v입니다. %v를 기대했습니다", got, want)
		}
	}
	if len(sortedKeys(map[string]int{})) != 0 {
		t.Error("빈 map에서 뭔가 나왔습니다")
	}
}

// findRoot는 go.mod가 있는 곳까지 올라갑니다.
// 못 찾으면 시작한 자리를 그대로 돌려줘야 합니다. 무한히 올라가면 안 됩니다.
func TestFindRoot(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}

	// 위로 끝까지 올라가도 못 찾으면 시작한 자리를 그대로 돌려줍니다.
	// 무한히 올라가거나 파일 시스템 뿌리를 고르면 안 됩니다.
	if got := findRoot(deep); got != deep {
		t.Errorf("go.mod가 없는데 %q를 골랐습니다. 시작 자리를 기대했습니다", got)
	}

	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := findRoot(deep); got != root {
		t.Errorf("%q입니다. %q를 기대했습니다", got, root)
	}
	if got := findRoot(root); got != root {
		t.Errorf("go.mod가 있는 자리에서 %q를 골랐습니다", got)
	}
}

// probe가 왜 실패했는지 한 줄로 알려 주는 부분입니다.
// 네트워크 실패를 뭉뚱그리면 차단인지 회선 문제인지 구분할 수 없습니다.
func TestFailureKind(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&danbooru.StatusError{Code: 429}, "HTTP 429"},
		{&danbooru.StatusError{Code: 503}, "HTTP 503"},
		{errors.New("dial tcp: i/o timeout"), "시간 초과"},
		{errors.New("read: connection reset by peer"), "연결 끊김"},
		{errors.New("lookup x.test: no such host"), "이름 조회 실패"},
		{context.DeadlineExceeded, "시간 초과"},
		{errors.New("무슨 일인지 모르겠습니다"), "기타"},
	}

	for _, tc := range tests {
		if got := failureKind(tc.err); got != tc.want {
			t.Errorf("%v가 %q입니다. %q를 기대했습니다", tc.err, got, tc.want)
		}
	}
}

// 감싼 오류도 알아봐야 합니다. 실제로는 늘 여러 겹으로 감싸여 옵니다.
func TestFailureKindSeesThroughWrapping(t *testing.T) {
	wrapped := fmt.Errorf("이미지를 받지 못했습니다: %w",
		fmt.Errorf("두 번째 겹: %w", &danbooru.StatusError{Code: 404}))

	if got := failureKind(wrapped); got != "HTTP 404" {
		t.Errorf("%q입니다. HTTP 404를 기대했습니다", got)
	}
}

func TestMax64(t *testing.T) {
	cases := [][3]int64{{1, 2, 2}, {2, 1, 2}, {-5, -1, -1}, {0, 0, 0}}
	for _, c := range cases {
		if got := max64(c[0], c[1]); got != c[2] {
			t.Errorf("max64(%d, %d)가 %d입니다. %d를 기대했습니다", c[0], c[1], got, c[2])
		}
	}
}

// 저장소에서는 python/worker이고, 노드에 배포하면 실행 파일 옆의 worker입니다.
// 한쪽만 보면 배포한 노드에서 setup이 실패합니다. 실제로 그랬습니다.
func TestFindWorkerDirHandlesBothLayouts(t *testing.T) {
	tests := []struct {
		name  string
		parts []string
	}{
		{"저장소 구조", []string{"python", "worker"}},
		{"배포 꾸러미 구조", []string{"worker"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(append([]string{root}, tc.parts...)...)
			if err := os.MkdirAll(dir, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}

			got, err := findWorkerDir(root)
			if err != nil {
				t.Fatalf("찾지 못했습니다: %v", err)
			}
			if got != dir {
				t.Errorf("%q입니다. %q를 기대했습니다", got, dir)
			}
		})
	}
}

// 이름만 같고 안이 빈 폴더를 고르면 안 됩니다.
func TestFindWorkerDirNeedsMainPy(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "worker"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := findWorkerDir(root); err == nil {
		t.Error("main.py가 없는 폴더를 골랐습니다")
	}
}

// 저장소 구조가 있으면 그쪽을 먼저 씁니다. 개발할 때 헷갈리지 않게 합니다.
func TestFindWorkerDirPrefersRepoLayout(t *testing.T) {
	root := t.TempDir()
	for _, parts := range [][]string{{"python", "worker"}, {"worker"}} {
		dir := filepath.Join(append([]string{root}, parts...)...)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := findWorkerDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(root, "python", "worker") {
		t.Errorf("%q를 골랐습니다. 저장소 구조를 먼저 봐야 합니다", got)
	}
}

func TestFindWorkerDirErrorNamesWhatItTried(t *testing.T) {
	_, err := findWorkerDir(t.TempDir())
	if err == nil {
		t.Fatal("오류가 나야 합니다")
	}
	for _, want := range []string{"python", "worker"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("오류에 %q가 없습니다: %v", want, err)
		}
	}
}
