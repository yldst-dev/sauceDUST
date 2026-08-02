// Package config는 환경값을 읽고 검증합니다.
// 잘못된 설정은 여기서 걸러 실행 중에 터지지 않게 합니다.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"saucedust/internal/domain"
)

type Config struct {
	NodeID   string
	Role     domain.Role
	DataDir  string
	ThumbDir string

	DatabaseURL    string
	MaxConnections int
	AcquireTimeout time.Duration

	QdrantURL    string
	QdrantAPIKey string

	ControlURL   string
	ControlToken string
	ControlBind  string

	EmbedWorkerURL   string
	EmbedBatchSize   int
	EmbedBatchWindow time.Duration

	SourceSite  string
	ScopeKey    string
	IndexTags   string
	PollEvery   time.Duration
	UserAgent   string
	DanbooruAPI string
	// 계정을 넣으면 인증된 요청을 보내 더 높은 속도 한도를 받습니다.
	DanbooruLogin  string
	DanbooruAPIKey string

	BackfillWorkers   int
	BackfillRangeSize int64
	BackfillFloor     int64
	MaxIndexed        int64

	Concurrency    int
	MinConcurrency int
	MaxConcurrency int
	Adaptive       bool
	// RatePerSecond는 이 노드가 대상 사이트에 내는 초당 요청 수 상한입니다.
	// 동시 수와 별개입니다. 상대가 IP 단위로 제한하므로 동시 수만 낮춰서는
	// 부족합니다. 실측에서 동시 32로 올렸을 때 429가 쏟아졌습니다.
	RatePerSecond float64
	RateBurst     int

	ThumbSize    int
	ThumbQuality int

	NetOrder      []domain.NetMode
	FragmentParts int
	FragmentDelay time.Duration
	ProxyURL      string
	AllowPrivate  bool
	DirectOnFail  bool

	HeartbeatEvery time.Duration
	NodeTimeout    time.Duration

	TelegramToken       string
	TelegramPollTimeout time.Duration
	// TelegramAllowedUsers가 비어 있으면 토큰을 아는 누구나 검색할 수 있습니다.
	TelegramAllowedUsers []int64
}

func Load(root string) (*Config, error) {
	if err := loadDotEnv(filepath.Join(root, ".env")); err != nil {
		return nil, err
	}

	r := &reader{}

	dataDir := r.str("SAUCEDUST_DATA_DIR", "")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("홈 디렉터리를 찾지 못했습니다: %w", err)
		}
		dataDir = filepath.Join(home, ".saucedust")
	}
	dataDir = expandPath(dataDir, root)

	role := domain.Role(r.str("SAUCEDUST_NODE_ROLE", string(domain.RoleControl)))
	if !role.Valid() {
		return nil, fmt.Errorf("SAUCEDUST_NODE_ROLE 값이 잘못되었습니다: %q (control 또는 worker)", role)
	}

	nodeID := r.str("SAUCEDUST_NODE_ID", "")
	if nodeID == "" {
		host, err := os.Hostname()
		if err != nil || host == "" {
			return nil, errNoNodeID
		}
		// 컴퓨터 이름이 전부 한글이면 쓸 수 있는 글자가 하나도 남지 않습니다.
		// 그대로 두면 모든 노드가 빈 이름으로 등록되어 서로를 덮어씁니다.
		if nodeID = sanitizeNodeID(host); nodeID == "" {
			return nil, errNoNodeID
		}
	}

	cfg := &Config{
		NodeID:   nodeID,
		Role:     role,
		DataDir:  dataDir,
		ThumbDir: expandPath(r.str("SAUCEDUST_THUMB_DIR", filepath.Join(dataDir, "thumbs")), root),

		DatabaseURL:    r.str("DATABASE_URL", "postgres://sauce:saucepass@localhost:5432/sauce"),
		MaxConnections: r.intVal("POSTGRES_MAX_CONNECTIONS", 0),
		AcquireTimeout: r.secs("POSTGRES_ACQUIRE_TIMEOUT_SECS", 120*time.Second),

		QdrantURL:    r.str("QDRANT_URL", "http://localhost:6333"),
		QdrantAPIKey: r.str("QDRANT_API_KEY", ""),

		ControlURL:   r.url("SAUCEDUST_CONTROL_URL", "http://localhost:8000"),
		ControlToken: r.str("SAUCEDUST_CONTROL_TOKEN", ""),
		ControlBind:  r.str("SAUCEDUST_CONTROL_BIND", "127.0.0.1:8000"),

		EmbedWorkerURL:   r.url("EMBED_WORKER_URL", "http://127.0.0.1:8100"),
		EmbedBatchSize:   r.intVal("EMBED_BATCH_SIZE", 16),
		EmbedBatchWindow: r.millis("EMBED_BATCH_TIMEOUT_MS", 50*time.Millisecond),

		SourceSite:     r.str("SAUCEDUST_SOURCE_SITE", "danbooru"),
		ScopeKey:       r.str("SAUCEDUST_SCOPE_KEY", "default"),
		IndexTags:      r.str("INDEX_TAGS", ""),
		PollEvery:      r.secs("SAUCEDUST_POLL_SECS", 15*time.Second),
		UserAgent:      r.str("SAUCEDUST_USER_AGENT", "saucedust/0.2"),
		DanbooruAPI:    r.url("DANBOORU_API_URL", "https://danbooru.donmai.us"),
		DanbooruLogin:  r.str("DANBOORU_LOGIN", ""),
		DanbooruAPIKey: r.str("DANBOORU_API_KEY", ""),

		BackfillWorkers:   r.intVal("CRAWL_BACKFILL_WORKERS", 2),
		BackfillRangeSize: int64(r.intVal("CRAWL_BACKFILL_RANGE_SIZE", 10000)),
		BackfillFloor:     int64(r.intVal("CRAWL_BACKFILL_FLOOR", 0)),
		MaxIndexed:        int64(r.intVal("SAUCEDUST_MAX_INDEXED", 0)),

		Concurrency:    r.intVal("CRAWL_CONCURRENCY", 4),
		MinConcurrency: r.intVal("CRAWL_MIN_CONCURRENCY", 1),
		MaxConcurrency: r.intVal("CRAWL_MAX_CONCURRENCY", 16),
		Adaptive:       r.boolVal("CRAWL_ADAPTIVE", true),
		RatePerSecond:  r.floatVal("CRAWL_RATE_PER_SEC", 5),
		RateBurst:      r.intVal("CRAWL_RATE_BURST", 8),

		ThumbSize:    r.intVal("THUMB_SIZE", 384),
		ThumbQuality: r.intVal("THUMB_QUALITY", 85),

		FragmentParts: r.intVal("SAUCEDUST_FRAGMENT_PARTS", 3),
		FragmentDelay: r.millis("SAUCEDUST_FRAGMENT_DELAY_MS", 0),
		ProxyURL:      r.str("SAUCEDUST_PROXY_URL", ""),
		AllowPrivate:  r.boolVal("SAUCEDUST_ALLOW_PRIVATE_TARGETS", false),
		DirectOnFail:  r.boolVal("SAUCEDUST_DIRECT_FALLBACK", true),

		HeartbeatEvery: r.secs("SAUCEDUST_HEARTBEAT_SECS", 30*time.Second),
		NodeTimeout:    r.secs("SAUCEDUST_NODE_TIMEOUT_SECS", 90*time.Second),

		TelegramToken:       r.str("TELEGRAM_BOT_TOKEN", ""),
		TelegramPollTimeout: r.secs("TELEGRAM_POLL_TIMEOUT_SECS", 30*time.Second),
	}

	allowed, err := parseIDs(r.str("TELEGRAM_ALLOWED_USERS", ""))
	if err != nil {
		return nil, err
	}
	cfg.TelegramAllowedUsers = allowed

	order, err := parseNetOrder(r.str("SAUCEDUST_NET_ORDER", "direct,ech,frag,vpn"))
	if err != nil {
		return nil, err
	}
	cfg.NetOrder = order

	// 잘못 적힌 값을 먼저 알립니다. 기본값으로 대체된 상태에서 validate를 돌리면
	// 엉뚱한 항목을 탓하는 오류가 나옵니다.
	if err := r.err(); err != nil {
		return nil, err
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

var errNoNodeID = errors.New(
	"SAUCEDUST_NODE_ID를 정할 수 없습니다. 컴퓨터 이름에서 쓸 수 있는 글자를 찾지 못했습니다. .env에 직접 지정하십시오")

func (c *Config) validate() error {
	if c.MinConcurrency < 1 {
		return fmt.Errorf("CRAWL_MIN_CONCURRENCY는 1 이상이어야 합니다")
	}
	if c.MaxConcurrency < c.MinConcurrency {
		return fmt.Errorf("CRAWL_MAX_CONCURRENCY(%d)가 CRAWL_MIN_CONCURRENCY(%d)보다 작습니다",
			c.MaxConcurrency, c.MinConcurrency)
	}
	c.Concurrency = clamp(c.Concurrency, c.MinConcurrency, c.MaxConcurrency)

	if c.RatePerSecond < 0 {
		return fmt.Errorf("CRAWL_RATE_PER_SEC는 0 이상이어야 합니다")
	}
	if c.RateBurst < 1 {
		c.RateBurst = 1
	}
	if c.BackfillRangeSize < 1 {
		return fmt.Errorf("CRAWL_BACKFILL_RANGE_SIZE는 1 이상이어야 합니다")
	}
	if c.BackfillFloor < 0 {
		return fmt.Errorf("CRAWL_BACKFILL_FLOOR는 0 이상이어야 합니다")
	}
	if c.MaxIndexed < 0 {
		return fmt.Errorf("SAUCEDUST_MAX_INDEXED는 0 이상이어야 합니다")
	}
	if c.ThumbSize < 64 {
		return fmt.Errorf("THUMB_SIZE는 64 이상이어야 합니다")
	}
	if c.ThumbQuality < 1 || c.ThumbQuality > 100 {
		return fmt.Errorf("THUMB_QUALITY는 1에서 100 사이여야 합니다")
	}
	// 0이면 NewTicker가 터집니다. NodeTimeout과만 견주면 0이 통과합니다.
	if c.HeartbeatEvery <= 0 {
		return fmt.Errorf("SAUCEDUST_HEARTBEAT_SECS는 0보다 커야 합니다")
	}
	if c.NodeTimeout <= c.HeartbeatEvery {
		return fmt.Errorf("SAUCEDUST_NODE_TIMEOUT_SECS는 SAUCEDUST_HEARTBEAT_SECS보다 커야 합니다")
	}
	if c.Role == domain.RoleWorker && c.ControlURL == "" {
		return fmt.Errorf("worker 노드는 SAUCEDUST_CONTROL_URL이 필요합니다")
	}
	return nil
}

// PoolSize는 DB 커넥션 수를 정합니다.
// 사용자가 지정한 값도 상한을 둡니다. pgx가 int32를 쓰므로 지나치게 큰 값은
// 뒤집혀서 음수가 되고, 그러면 연결이 아예 되지 않습니다.
func (c *Config) PoolSize() int {
	const maxPool = 500
	if c.MaxConnections > 0 {
		return clamp(c.MaxConnections, 1, maxPool)
	}
	return clamp(c.Concurrency*2+8, 8, 95)
}

// parseIDs는 쉼표로 나열한 숫자 목록을 읽습니다.
func parseIDs(raw string) ([]int64, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		id, err := strconv.ParseInt(token, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("TELEGRAM_ALLOWED_USERS에 숫자가 아닌 값이 있습니다: %q", token)
		}
		out = append(out, id)
	}
	return out, nil
}

// parseNetOrder는 경로 우선순위를 읽습니다. "sni"는 두 우회 방식을 한 번에
// 지정하는 줄임말이며 ech, frag 순서로 펼쳐집니다.
func parseNetOrder(raw string) ([]domain.NetMode, error) {
	parts := strings.Split(raw, ",")
	out := make([]domain.NetMode, 0, len(parts)+1)
	seen := map[domain.NetMode]bool{}

	add := func(mode domain.NetMode) {
		if !seen[mode] {
			seen[mode] = true
			out = append(out, mode)
		}
	}

	for _, part := range parts {
		token := strings.ToLower(strings.TrimSpace(part))
		if token == "" {
			continue
		}
		if token == "sni" {
			add(domain.NetECH)
			add(domain.NetFragment)
			continue
		}
		mode := domain.NetMode(token)
		if !mode.Valid() {
			return nil, fmt.Errorf("SAUCEDUST_NET_ORDER에 알 수 없는 값이 있습니다: %q", token)
		}
		add(mode)
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("SAUCEDUST_NET_ORDER가 비어 있습니다")
	}
	return out, nil
}

func loadDotEnv(path string) error {
	// 경로는 실행 파일 위치에서 만들어집니다. 바깥에서 정하지 못합니다.
	file, err := os.Open(path) // #nosec G304 -- 작업 폴더 기준 고정 경로입니다
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return fmt.Errorf("%s:%d 형식이 잘못되었습니다", path, line)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func expandPath(path, root string) string {
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, path)
}

func sanitizeNodeID(raw string) string {
	raw = strings.ToLower(strings.TrimSuffix(raw, ".local"))
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// reader는 환경값을 읽으면서 잘못 적힌 것을 모읍니다.
//
// 값이 있는데 해석하지 못하면 기본값으로 조용히 넘어가지 않고 오류로 올립니다.
// CRAWL_RATE_PER_SEC에 오타를 내고도 아무 말 없이 기본값으로 도는 것이
// 가장 나쁜 경우입니다. 운영자는 값을 바꿨다고 믿고 있습니다.
//
// 하나 만날 때마다 멈추지 않고 끝까지 읽는 이유는, 오타가 여러 개일 때
// 고치고 다시 돌리기를 반복하지 않게 하기 위해서입니다.
type reader struct {
	errs []string
}

func (r *reader) reject(key, raw, want string) {
	r.errs = append(r.errs, fmt.Sprintf("%s=%q: %s여야 합니다", key, raw, want))
}

func (r *reader) err() error {
	switch len(r.errs) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("설정값이 잘못되었습니다. %s", r.errs[0])
	default:
		return fmt.Errorf("설정값 %d개가 잘못되었습니다.\n  %s",
			len(r.errs), strings.Join(r.errs, "\n  "))
	}
}

// raw는 값이 실제로 적혀 있는지까지 알려줍니다.
// 빈 문자열은 적지 않은 것으로 봅니다. .env에 `KEY=`만 남겨 두는 일이 흔합니다.
func (r *reader) raw(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	return v, v != ""
}

func (r *reader) str(key, def string) string {
	if v, ok := r.raw(key); ok {
		return v
	}
	return def
}

// url은 뒤에 붙은 빗금을 떼어 냅니다. 경로를 이어 붙일 때 두 겹이 되지 않게 합니다.
func (r *reader) url(key, def string) string {
	return strings.TrimRight(r.str(key, def), "/")
}

func (r *reader) intVal(key string, def int) int {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.reject(key, v, "정수")
		return def
	}
	return n
}

func (r *reader) boolVal(key string, def bool) bool {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	r.reject(key, v, "1 또는 0 (true, false, yes, no, on, off도 됩니다)")
	return def
}

func (r *reader) floatVal(key string, def float64) float64 {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		r.reject(key, v, "숫자")
		return def
	}
	return n
}

// secs와 millis는 0을 그대로 받습니다. 0으로 꺼 두려는 설정을 기본값으로
// 되돌려 버리면 운영자가 끌 방법이 없어집니다.
func (r *reader) secs(key string, def time.Duration) time.Duration {
	return r.span(key, def, time.Second)
}

func (r *reader) millis(key string, def time.Duration) time.Duration {
	return r.span(key, def, time.Millisecond)
}

func (r *reader) span(key string, def time.Duration, unit time.Duration) time.Duration {
	v, ok := r.raw(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		r.reject(key, v, "정수")
		return def
	}
	if n < 0 {
		r.reject(key, v, "0 이상")
		return def
	}
	return time.Duration(n) * unit
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
