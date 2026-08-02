package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"saucedust/internal/domain"
)

// baseEnv는 Load가 통과하는 최소 설정입니다.
// 노드 이름을 못 박아 두어야 이 컴퓨터의 호스트 이름에 결과가 흔들리지 않습니다.
func baseEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("SAUCEDUST_NODE_ID", "test-node")
	return t.TempDir()
}

func TestLoadDefaults(t *testing.T) {
	root := baseEnv(t)

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("기본값으로 실패했습니다: %v", err)
	}

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"NodeID", cfg.NodeID, "test-node"},
		{"Role", cfg.Role, domain.RoleControl},
		{"Concurrency", cfg.Concurrency, 4},
		{"MinConcurrency", cfg.MinConcurrency, 1},
		{"MaxConcurrency", cfg.MaxConcurrency, 16},
		{"Adaptive", cfg.Adaptive, true},
		{"RatePerSecond", cfg.RatePerSecond, 5.0},
		{"ThumbSize", cfg.ThumbSize, 384},
		{"ThumbQuality", cfg.ThumbQuality, 85},
		{"EmbedBatchWindow", cfg.EmbedBatchWindow, 50 * time.Millisecond},
		{"HeartbeatEvery", cfg.HeartbeatEvery, 30 * time.Second},
		{"NodeTimeout", cfg.NodeTimeout, 90 * time.Second},
		{"AllowPrivate", cfg.AllowPrivate, false},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s가 %v입니다. %v를 기대했습니다", c.name, c.got, c.want)
		}
	}
}

// 값이 있는데 해석하지 못하면 기본값으로 넘어가지 않아야 합니다.
// CRAWL_RATE_PER_SEC에 오타를 내고도 아무 말 없이 기본 속도로 도는 것이
// 가장 나쁜 경우입니다. 운영자는 값을 바꿨다고 믿고 있습니다.
func TestLoadRejectsMalformedValues(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{"CRAWL_MAX_CONCURRENCY", "열여섯"},
		{"CRAWL_MAX_CONCURRENCY", "16개"},
		{"CRAWL_MAX_CONCURRENCY", "1.5"},
		{"CRAWL_RATE_PER_SEC", "다섯"},
		{"CRAWL_RATE_PER_SEC", "5,0"},
		{"CRAWL_ADAPTIVE", "아니오"},
		{"CRAWL_ADAPTIVE", "2"},
		{"THUMB_SIZE", "384px"},
		{"SAUCEDUST_HEARTBEAT_SECS", "30초"},
		{"EMBED_BATCH_TIMEOUT_MS", "빠르게"},
	}

	for _, tc := range tests {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			root := baseEnv(t)
			t.Setenv(tc.key, tc.value)

			_, err := Load(root)
			if err == nil {
				t.Fatal("조용히 기본값으로 넘어갔습니다")
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Errorf("오류가 어느 항목인지 알려주지 않습니다: %v", err)
			}
			if !strings.Contains(err.Error(), tc.value) {
				t.Errorf("오류가 잘못 적은 값을 보여주지 않습니다: %v", err)
			}
		})
	}
}

func TestLoadReportsEveryMalformedValueAtOnce(t *testing.T) {
	// 하나씩 알려 주면 고치고 다시 돌리기를 반복해야 합니다.
	root := baseEnv(t)
	t.Setenv("CRAWL_MAX_CONCURRENCY", "많이")
	t.Setenv("THUMB_QUALITY", "높게")
	t.Setenv("CRAWL_ADAPTIVE", "그럼요")

	_, err := Load(root)
	if err == nil {
		t.Fatal("오류가 나야 합니다")
	}
	for _, key := range []string{"CRAWL_MAX_CONCURRENCY", "THUMB_QUALITY", "CRAWL_ADAPTIVE"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("%s가 빠졌습니다: %v", key, err)
		}
	}
}

func TestLoadAcceptsZeroDuration(t *testing.T) {
	// 0으로 꺼 두려는 설정을 기본값으로 되돌리면 끌 방법이 없어집니다.
	root := baseEnv(t)
	t.Setenv("EMBED_BATCH_TIMEOUT_MS", "0")
	t.Setenv("SAUCEDUST_FRAGMENT_DELAY_MS", "0")

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("0을 거부했습니다: %v", err)
	}
	if cfg.EmbedBatchWindow != 0 {
		t.Errorf("EMBED_BATCH_TIMEOUT_MS가 %v입니다. 0을 기대했습니다", cfg.EmbedBatchWindow)
	}
}

func TestLoadRejectsNegativeDuration(t *testing.T) {
	root := baseEnv(t)
	t.Setenv("SAUCEDUST_POLL_SECS", "-5")

	if _, err := Load(root); err == nil {
		t.Fatal("음수 시간을 받아들였습니다")
	}
}

func TestLoadTreatsEmptyAsUnset(t *testing.T) {
	// .env에 `KEY=`만 남겨 두는 일이 흔합니다. 지운 것으로 봅니다.
	root := baseEnv(t)
	t.Setenv("CRAWL_MAX_CONCURRENCY", "")
	t.Setenv("CRAWL_ADAPTIVE", "   ")

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("빈 값에서 실패했습니다: %v", err)
	}
	if cfg.MaxConcurrency != 16 || !cfg.Adaptive {
		t.Errorf("기본값으로 돌아가지 않았습니다: %d %v", cfg.MaxConcurrency, cfg.Adaptive)
	}
}

func TestLoadValidationRules(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"최대가 최소보다 작음", map[string]string{
			"CRAWL_MIN_CONCURRENCY": "8", "CRAWL_MAX_CONCURRENCY": "4"}},
		{"최소가 0", map[string]string{"CRAWL_MIN_CONCURRENCY": "0"}},
		{"속도가 음수", map[string]string{"CRAWL_RATE_PER_SEC": "-1"}},
		{"구간 크기가 0", map[string]string{"CRAWL_BACKFILL_RANGE_SIZE": "0"}},
		{"축소본이 너무 작음", map[string]string{"THUMB_SIZE": "32"}},
		{"품질이 범위를 넘음", map[string]string{"THUMB_QUALITY": "101"}},
		{"품질이 0", map[string]string{"THUMB_QUALITY": "0"}},
		{"죽음 판정이 심장박동보다 짧음", map[string]string{
			"SAUCEDUST_HEARTBEAT_SECS": "60", "SAUCEDUST_NODE_TIMEOUT_SECS": "30"}},
		{"죽음 판정이 심장박동과 같음", map[string]string{
			"SAUCEDUST_HEARTBEAT_SECS": "30", "SAUCEDUST_NODE_TIMEOUT_SECS": "30"}},
		{"역할이 잘못됨", map[string]string{"SAUCEDUST_NODE_ROLE": "master"}},
		{"경로가 잘못됨", map[string]string{"SAUCEDUST_NET_ORDER": "tor"}},
		{"경로가 비었음", map[string]string{"SAUCEDUST_NET_ORDER": ","}},
		{"허용 사용자에 숫자가 아닌 값", map[string]string{"TELEGRAM_ALLOWED_USERS": "abc"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			root := baseEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := Load(root); err == nil {
				t.Error("잘못된 설정을 받아들였습니다")
			}
		})
	}
}

func TestLoadClampsConcurrency(t *testing.T) {
	root := baseEnv(t)
	t.Setenv("CRAWL_MIN_CONCURRENCY", "4")
	t.Setenv("CRAWL_MAX_CONCURRENCY", "8")
	t.Setenv("CRAWL_CONCURRENCY", "99")

	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Concurrency != 8 {
		t.Errorf("동시 수가 %d입니다. 8로 눌러야 합니다", cfg.Concurrency)
	}
}

func TestLoadTrimsTrailingSlash(t *testing.T) {
	root := baseEnv(t)
	t.Setenv("SAUCEDUST_CONTROL_URL", "http://central.test:8000/")
	t.Setenv("EMBED_WORKER_URL", "http://127.0.0.1:8100//")
	t.Setenv("DANBOORU_API_URL", "https://danbooru.donmai.us/")

	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ControlURL != "http://central.test:8000" {
		t.Errorf("ControlURL이 %q입니다", cfg.ControlURL)
	}
	if cfg.EmbedWorkerURL != "http://127.0.0.1:8100" {
		t.Errorf("EmbedWorkerURL이 %q입니다", cfg.EmbedWorkerURL)
	}
	if cfg.DanbooruAPI != "https://danbooru.donmai.us" {
		t.Errorf("DanbooruAPI가 %q입니다", cfg.DanbooruAPI)
	}
}

// pgx가 int32를 쓰므로 지나치게 큰 값은 뒤집혀 음수가 되고 연결이 아예 안 됩니다.
func TestPoolSizeStaysInRange(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want int
	}{
		{"지정 없음", Config{Concurrency: 4}, 16},
		{"지정 없고 동시 수가 큼", Config{Concurrency: 64}, 95},
		{"지정 없고 동시 수가 0", Config{Concurrency: 0}, 8},
		{"직접 지정", Config{MaxConnections: 32}, 32},
		{"상한을 넘김", Config{MaxConnections: 100000}, 500},
		{"int32를 넘김", Config{MaxConnections: 2147483647}, 500},
		{"음수", Config{MaxConnections: -1, Concurrency: 4}, 16},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.PoolSize()
			if got != tc.want {
				t.Errorf("%d입니다. %d를 기대했습니다", got, tc.want)
			}
			if got < 1 || got > 500 {
				t.Errorf("%d는 쓸 수 없는 값입니다", got)
			}
		})
	}
}

func TestParseNetOrder(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []domain.NetMode
	}{
		{"기본", "direct,ech,frag,vpn",
			[]domain.NetMode{domain.NetDirect, domain.NetECH, domain.NetFragment, domain.NetProxy}},
		{"sni는 두 방식으로 펼쳐짐", "direct,sni",
			[]domain.NetMode{domain.NetDirect, domain.NetECH, domain.NetFragment}},
		{"겹치면 한 번만", "direct,direct,frag",
			[]domain.NetMode{domain.NetDirect, domain.NetFragment}},
		{"sni와 ech가 겹쳐도 한 번만", "ech,sni",
			[]domain.NetMode{domain.NetECH, domain.NetFragment}},
		{"공백과 대문자", " DIRECT , Frag ",
			[]domain.NetMode{domain.NetDirect, domain.NetFragment}},
		{"빈 칸은 건너뜀", "direct,,frag",
			[]domain.NetMode{domain.NetDirect, domain.NetFragment}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseNetOrder(tc.raw)
			if err != nil {
				t.Fatalf("오류가 났습니다: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("%v입니다. %v를 기대했습니다", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("%v입니다. %v를 기대했습니다", got, tc.want)
				}
			}
		})
	}
}

func TestParseIDs(t *testing.T) {
	got, err := parseIDs(" 1, 22 ,333,")
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{1, 22, 333}
	if len(got) != len(want) {
		t.Fatalf("%v입니다. %v를 기대했습니다", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v입니다. %v를 기대했습니다", got, want)
		}
	}

	if ids, err := parseIDs("   "); err != nil || ids != nil {
		t.Errorf("빈 값이 %v %v입니다", ids, err)
	}
	if _, err := parseIDs("1,둘"); err == nil {
		t.Error("숫자가 아닌 값을 받아들였습니다")
	}
}

func TestSanitizeNodeID(t *testing.T) {
	tests := map[string]string{
		"MacBook-Pro.local":  "macbook-pro",
		"worker_01":          "worker_01",
		"Node 3":             "node-3",
		"--edge--":           "edge",
		"수민의 맥북":             "",
		"a.b.c":              "a-b-c",
		"ALLCAPS":            "allcaps",
		"node.example.local": "node-example",
	}

	for input, want := range tests {
		if got := sanitizeNodeID(input); got != want {
			t.Errorf("%q가 %q입니다. %q를 기대했습니다", input, got, want)
		}
	}
}

// 컴퓨터 이름이 전부 한글이면 쓸 수 있는 글자가 남지 않습니다.
// 빈 이름으로 등록하면 모든 노드가 서로를 덮어씁니다.
func TestLoadRefusesEmptyNodeID(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SAUCEDUST_NODE_ID", "")

	host, err := os.Hostname()
	if err != nil || sanitizeNodeID(host) != "" {
		t.Skipf("이 컴퓨터의 이름 %q에서는 재현되지 않습니다", host)
	}
	if _, err := Load(root); err == nil {
		t.Error("빈 노드 이름을 받아들였습니다")
	}
}

func TestLoadDotEnv(t *testing.T) {
	root := t.TempDir()
	const key = "SAUCEDUST_TEST_DOTENV_VALUE"
	t.Cleanup(func() { _ = os.Unsetenv(key) })

	body := strings.Join([]string{
		"# 주석입니다",
		"",
		key + "=hello",
		"   " + key + "_QUOTED=\"a b c\"",
		key + "_EQUALS=a=b=c",
	}, "\n")
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv(key + "_QUOTED")
		_ = os.Unsetenv(key + "_EQUALS")
	})

	if err := loadDotEnv(filepath.Join(root, ".env")); err != nil {
		t.Fatalf(".env를 읽지 못했습니다: %v", err)
	}

	checks := map[string]string{
		key:             "hello",
		key + "_QUOTED": "a b c",
		key + "_EQUALS": "a=b=c",
	}
	for k, want := range checks {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s가 %q입니다. %q를 기대했습니다", k, got, want)
		}
	}
}

// 이미 환경에 있는 값이 .env보다 셉니다.
// 한 번만 다르게 돌려 보려고 앞에 붙인 값이 파일에 덮이면 안 됩니다.
func TestLoadDotEnvDoesNotOverrideEnv(t *testing.T) {
	root := t.TempDir()
	const key = "SAUCEDUST_TEST_DOTENV_PRIORITY"
	t.Setenv(key, "from-env")

	if err := os.WriteFile(filepath.Join(root, ".env"),
		[]byte(key+"=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loadDotEnv(filepath.Join(root, ".env")); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(key); got != "from-env" {
		t.Errorf("%q입니다. 환경값이 이겨야 합니다", got)
	}
}

func TestLoadDotEnvMissingFileIsFine(t *testing.T) {
	if err := loadDotEnv(filepath.Join(t.TempDir(), "없는파일")); err != nil {
		t.Errorf("없는 파일에서 오류가 났습니다: %v", err)
	}
}

func TestLoadDotEnvRejectsMalformedLine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env")
	if err := os.WriteFile(path, []byte("KEY=ok\n등호가없는줄\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("KEY") })

	if err := loadDotEnv(path); err == nil {
		t.Error("형식이 잘못된 줄을 넘겼습니다")
	}
}

func TestExpandPath(t *testing.T) {
	root := "/srv/saucedust"

	if got := expandPath("thumbs", root); got != filepath.Join(root, "thumbs") {
		t.Errorf("상대 경로가 %q입니다", got)
	}
	if got := expandPath("/mnt/ssd/thumbs", root); got != "/mnt/ssd/thumbs" {
		t.Errorf("절대 경로가 %q입니다", got)
	}
	if got := expandPath("/mnt/ssd/../ssd/thumbs", root); got != "/mnt/ssd/thumbs" {
		t.Errorf("정리되지 않았습니다: %q", got)
	}
	if got := expandPath("", root); got != "" {
		t.Errorf("빈 경로가 %q가 되었습니다", got)
	}

	home, err := os.UserHomeDir()
	if err == nil {
		if got := expandPath("~/thumbs", root); got != filepath.Join(home, "thumbs") {
			t.Errorf("물결표를 펴지 못했습니다: %q", got)
		}
	}
}

func TestExampleCoversEveryKey(t *testing.T) {
	// 본보기에 없는 항목은 운영자가 있는 줄도 모릅니다.
	// 실행 파일에 박아 넣은 본보기가 실제 파일과 같은지도 함께 봅니다.
	onDisk, err := os.ReadFile("env.example")
	if err != nil {
		t.Fatal(err)
	}
	if Example != string(onDisk) {
		t.Error("실행 파일에 박힌 본보기가 env.example과 다릅니다")
	}
	if !strings.Contains(Example, "SAUCEDUST_NODE_ROLE") {
		t.Error("본보기가 비어 있는 것 같습니다")
	}
}
