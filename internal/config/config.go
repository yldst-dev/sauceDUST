// Package config는 환경값을 읽고 검증합니다.
// 잘못된 설정은 여기서 걸러 실행 중에 터지지 않게 합니다.
package config

import (
	"bufio"
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
	ProbeInterval time.Duration

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

	dataDir := envStr("SAUCEDUST_DATA_DIR", "")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("홈 디렉터리를 찾지 못했습니다: %w", err)
		}
		dataDir = filepath.Join(home, ".saucedust")
	}
	dataDir = expandPath(dataDir, root)

	role := domain.Role(envStr("SAUCEDUST_NODE_ROLE", string(domain.RoleControl)))
	if !role.Valid() {
		return nil, fmt.Errorf("SAUCEDUST_NODE_ROLE 값이 잘못되었습니다: %q (control 또는 worker)", role)
	}

	nodeID := envStr("SAUCEDUST_NODE_ID", "")
	if nodeID == "" {
		host, err := os.Hostname()
		if err != nil || host == "" {
			return nil, fmt.Errorf("SAUCEDUST_NODE_ID를 정할 수 없습니다. .env에 직접 지정하십시오")
		}
		nodeID = sanitizeNodeID(host)
	}

	cfg := &Config{
		NodeID:   nodeID,
		Role:     role,
		DataDir:  dataDir,
		ThumbDir: expandPath(envStr("SAUCEDUST_THUMB_DIR", filepath.Join(dataDir, "thumbs")), root),

		DatabaseURL:    envStr("DATABASE_URL", "postgres://sauce:saucepass@localhost:5432/sauce"),
		MaxConnections: envInt("POSTGRES_MAX_CONNECTIONS", 0),
		AcquireTimeout: envDuration("POSTGRES_ACQUIRE_TIMEOUT_SECS", 120*time.Second),

		QdrantURL:    envStr("QDRANT_URL", "http://localhost:6333"),
		QdrantAPIKey: envStr("QDRANT_API_KEY", ""),

		ControlURL:   strings.TrimRight(envStr("SAUCEDUST_CONTROL_URL", "http://localhost:8000"), "/"),
		ControlToken: envStr("SAUCEDUST_CONTROL_TOKEN", ""),
		ControlBind:  envStr("SAUCEDUST_CONTROL_BIND", "127.0.0.1:8000"),

		EmbedWorkerURL:   strings.TrimRight(envStr("EMBED_WORKER_URL", "http://127.0.0.1:8100"), "/"),
		EmbedBatchSize:   envInt("EMBED_BATCH_SIZE", 16),
		EmbedBatchWindow: envMillis("EMBED_BATCH_TIMEOUT_MS", 50*time.Millisecond),

		SourceSite:     envStr("SAUCEDUST_SOURCE_SITE", "danbooru"),
		ScopeKey:       envStr("SAUCEDUST_SCOPE_KEY", "default"),
		IndexTags:      envStr("INDEX_TAGS", ""),
		PollEvery:      envDuration("SAUCEDUST_POLL_SECS", 15*time.Second),
		UserAgent:      envStr("SAUCEDUST_USER_AGENT", "saucedust/0.2"),
		DanbooruAPI:    strings.TrimRight(envStr("DANBOORU_API_URL", "https://danbooru.donmai.us"), "/"),
		DanbooruLogin:  envStr("DANBOORU_LOGIN", ""),
		DanbooruAPIKey: envStr("DANBOORU_API_KEY", ""),

		BackfillWorkers:   envInt("CRAWL_BACKFILL_WORKERS", 2),
		BackfillRangeSize: int64(envInt("CRAWL_BACKFILL_RANGE_SIZE", 10000)),

		Concurrency:    envInt("CRAWL_CONCURRENCY", 4),
		MinConcurrency: envInt("CRAWL_MIN_CONCURRENCY", 1),
		MaxConcurrency: envInt("CRAWL_MAX_CONCURRENCY", 16),
		Adaptive:       envBool("CRAWL_ADAPTIVE", true),
		RatePerSecond:  envFloat("CRAWL_RATE_PER_SEC", 5),
		RateBurst:      envInt("CRAWL_RATE_BURST", 8),

		ThumbSize:    envInt("THUMB_SIZE", 384),
		ThumbQuality: envInt("THUMB_QUALITY", 85),

		FragmentParts: envInt("SAUCEDUST_FRAGMENT_PARTS", 3),
		FragmentDelay: envMillis("SAUCEDUST_FRAGMENT_DELAY_MS", 0),
		ProxyURL:      envStr("SAUCEDUST_PROXY_URL", ""),
		AllowPrivate:  envBool("SAUCEDUST_ALLOW_PRIVATE_TARGETS", false),
		DirectOnFail:  envBool("SAUCEDUST_DIRECT_FALLBACK", true),
		ProbeInterval: envDuration("SAUCEDUST_PROBE_INTERVAL_SECS", 3600*time.Second),

		HeartbeatEvery: envDuration("SAUCEDUST_HEARTBEAT_SECS", 30*time.Second),
		NodeTimeout:    envDuration("SAUCEDUST_NODE_TIMEOUT_SECS", 90*time.Second),

		TelegramToken:       envStr("TELEGRAM_BOT_TOKEN", ""),
		TelegramPollTimeout: envDuration("TELEGRAM_POLL_TIMEOUT_SECS", 30*time.Second),
	}

	allowed, err := parseIDs(envStr("TELEGRAM_ALLOWED_USERS", ""))
	if err != nil {
		return nil, err
	}
	cfg.TelegramAllowedUsers = allowed

	order, err := parseNetOrder(envStr("SAUCEDUST_NET_ORDER", "direct,ech,frag,vpn"))
	if err != nil {
		return nil, err
	}
	cfg.NetOrder = order

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

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
	if c.ThumbSize < 64 {
		return fmt.Errorf("THUMB_SIZE는 64 이상이어야 합니다")
	}
	if c.ThumbQuality < 1 || c.ThumbQuality > 100 {
		return fmt.Errorf("THUMB_QUALITY는 1에서 100 사이여야 합니다")
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

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(envStr(key, "")); err == nil {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(envStr(key, "")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if raw := envStr(key, ""); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return v
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := envInt(key, 0); v > 0 {
		return time.Duration(v) * time.Second
	}
	return def
}

func envMillis(key string, def time.Duration) time.Duration {
	if v := envInt(key, 0); v > 0 {
		return time.Duration(v) * time.Millisecond
	}
	return def
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
