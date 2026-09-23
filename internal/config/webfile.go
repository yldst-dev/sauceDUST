package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"saucedust/internal/domain"
)

const webFileName = "web-config.json"

type WebFile struct {
	BackfillFloor       int64   `json:"backfill_floor"`
	MaxIndexed          int64   `json:"max_indexed"`
	BackfillWorkers     int     `json:"backfill_workers"`
	BackfillRangeSize   int64   `json:"backfill_range_size"`
	Concurrency         int     `json:"concurrency"`
	MinConcurrency      int     `json:"min_concurrency"`
	MaxConcurrency      int     `json:"max_concurrency"`
	Adaptive            bool    `json:"adaptive"`
	RatePerSecond       float64 `json:"rate_per_sec"`
	RateBurst           int     `json:"rate_burst"`
	PollSecs            int     `json:"poll_secs"`
	ThumbSize           int     `json:"thumb_size"`
	ThumbQuality        int     `json:"thumb_quality"`
	ThumbDir            string  `json:"thumb_dir"`
	IndexTags           string  `json:"index_tags"`
	UserAgent           string  `json:"user_agent"`
	DanbooruLogin       string  `json:"danbooru_login"`
	DanbooruAPIKey      *string `json:"danbooru_api_key,omitempty"`
	TelegramAllowed     string  `json:"telegram_allowed"`
	NetOrder            string  `json:"net_order"`
	FragmentParts       int     `json:"fragment_parts"`
	FragmentDelayMS     int     `json:"fragment_delay_ms"`
	ProxyURL            *string `json:"proxy_url,omitempty"`
	AllowPrivate        bool    `json:"allow_private"`
	DirectFallback      bool    `json:"direct_fallback"`
	EmbedBatchSize      int     `json:"embed_batch_size"`
	EmbedBatchTimeoutMS int     `json:"embed_batch_timeout_ms"`
	HeartbeatSecs       int     `json:"heartbeat_secs"`
	NodeTimeoutSecs     int     `json:"node_timeout_secs"`
	ControlBind         string  `json:"control_bind"`
	ControlToken        *string `json:"control_token,omitempty"`
}

func WebFileFrom(cfg *Config) WebFile {
	key := cfg.DanbooruAPIKey
	proxy := cfg.ProxyURL
	token := cfg.ControlToken
	allowed := ""
	for i, id := range cfg.TelegramAllowedUsers {
		if i > 0 {
			allowed += ","
		}
		allowed += fmt.Sprintf("%d", id)
	}
	order := ""
	for i, mode := range cfg.NetOrder {
		if i > 0 {
			order += ","
		}
		order += string(mode)
	}
	return WebFile{
		BackfillFloor: cfg.BackfillFloor, MaxIndexed: cfg.MaxIndexed,
		BackfillWorkers: cfg.BackfillWorkers, BackfillRangeSize: cfg.BackfillRangeSize,
		Concurrency: cfg.Concurrency, MinConcurrency: cfg.MinConcurrency, MaxConcurrency: cfg.MaxConcurrency,
		Adaptive: cfg.Adaptive, RatePerSecond: cfg.RatePerSecond, RateBurst: cfg.RateBurst,
		PollSecs:  int(cfg.PollEvery / time.Second),
		ThumbSize: cfg.ThumbSize, ThumbQuality: cfg.ThumbQuality, ThumbDir: cfg.ThumbDir,
		IndexTags: cfg.IndexTags, UserAgent: cfg.UserAgent, DanbooruLogin: cfg.DanbooruLogin,
		DanbooruAPIKey: &key, TelegramAllowed: allowed, NetOrder: order,
		FragmentParts: cfg.FragmentParts, FragmentDelayMS: int(cfg.FragmentDelay / time.Millisecond),
		ProxyURL: &proxy, AllowPrivate: cfg.AllowPrivate, DirectFallback: cfg.DirectOnFail,
		EmbedBatchSize: cfg.EmbedBatchSize, EmbedBatchTimeoutMS: int(cfg.EmbedBatchWindow / time.Millisecond),
		HeartbeatSecs: int(cfg.HeartbeatEvery / time.Second), NodeTimeoutSecs: int(cfg.NodeTimeout / time.Second),
		ControlBind: cfg.ControlBind, ControlToken: &token,
	}
}

func (f WebFile) Apply(cfg *Config) error {
	if f.PollSecs < 1 {
		return errors.New("수집 주기는 1초 이상이어야 합니다")
	}
	if f.BackfillWorkers < 1 {
		return errors.New("과거 수집 일꾼은 1명 이상이어야 합니다")
	}
	if f.FragmentParts < 1 {
		return errors.New("조각 수는 1 이상이어야 합니다")
	}
	if f.FragmentDelayMS < 0 {
		return errors.New("조각 간격은 0 이상이어야 합니다")
	}
	if f.EmbedBatchSize < 1 {
		return errors.New("임베딩 묶음은 1장 이상이어야 합니다")
	}
	if f.EmbedBatchTimeoutMS < 1 {
		return errors.New("임베딩 대기는 1밀리초 이상이어야 합니다")
	}
	if f.HeartbeatSecs < 1 {
		return errors.New("심장박동은 1초 이상이어야 합니다")
	}
	if _, _, err := net.SplitHostPort(f.ControlBind); err != nil {
		return errors.New("제어 화면 주소는 호스트:포트 형식이어야 합니다. 예: 192.168.0.4:8000")
	}
	if f.ThumbDir != "" && !filepath.IsAbs(f.ThumbDir) {
		return errors.New("축소본 폴더는 절대 경로여야 합니다")
	}
	allowed, err := parseIDs(f.TelegramAllowed)
	if err != nil {
		return err
	}
	order, err := parseNetOrder(f.NetOrder)
	if err != nil {
		return err
	}
	cfg.BackfillFloor = f.BackfillFloor
	cfg.MaxIndexed = f.MaxIndexed
	cfg.BackfillWorkers = f.BackfillWorkers
	cfg.BackfillRangeSize = f.BackfillRangeSize
	cfg.Concurrency = f.Concurrency
	cfg.MinConcurrency = f.MinConcurrency
	cfg.MaxConcurrency = f.MaxConcurrency
	cfg.Adaptive = f.Adaptive
	cfg.RatePerSecond = f.RatePerSecond
	cfg.RateBurst = f.RateBurst
	cfg.PollEvery = time.Duration(f.PollSecs) * time.Second
	cfg.ThumbSize = f.ThumbSize
	cfg.ThumbQuality = f.ThumbQuality
	if f.ThumbDir != "" {
		cfg.ThumbDir = f.ThumbDir
	}
	cfg.IndexTags = f.IndexTags
	cfg.UserAgent = f.UserAgent
	cfg.DanbooruLogin = f.DanbooruLogin
	if f.DanbooruAPIKey != nil {
		cfg.DanbooruAPIKey = *f.DanbooruAPIKey
	}
	cfg.TelegramAllowedUsers = allowed
	cfg.NetOrder = order
	cfg.FragmentParts = f.FragmentParts
	cfg.FragmentDelay = time.Duration(f.FragmentDelayMS) * time.Millisecond
	if f.ProxyURL != nil {
		cfg.ProxyURL = *f.ProxyURL
	}
	cfg.AllowPrivate = f.AllowPrivate
	cfg.DirectOnFail = f.DirectFallback
	cfg.EmbedBatchSize = f.EmbedBatchSize
	cfg.EmbedBatchWindow = time.Duration(f.EmbedBatchTimeoutMS) * time.Millisecond
	cfg.HeartbeatEvery = time.Duration(f.HeartbeatSecs) * time.Second
	cfg.NodeTimeout = time.Duration(f.NodeTimeoutSecs) * time.Second
	cfg.ControlBind = f.ControlBind
	if f.ControlToken != nil {
		cfg.ControlToken = *f.ControlToken
	}
	if cfg.Role == domain.RoleControl {
		if err := exposureOK(cfg.ControlBind, cfg.ControlToken); err != nil {
			return err
		}
	}
	return cfg.validate()
}

func exposureOK(bind, token string) error {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return fmt.Errorf("제어 화면 주소가 잘못되었습니다: %w", err)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	if len(token) < 16 {
		return errors.New("이 주소로 열려면 접속 토큰이 16자 이상이어야 합니다")
	}
	return nil
}

func webPath(dir string) string {
	return filepath.Join(dir, webFileName)
}

func ApplyWebFile(cfg *Config) error {
	raw, err := os.ReadFile(webPath(cfg.DataDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	file := WebFileFrom(cfg)
	if err := json.Unmarshal(raw, &file); err != nil {
		return fmt.Errorf("웹 설정 파일을 해석하지 못했습니다: %w", err)
	}
	return file.Apply(cfg)
}

func MergeWebFile(cfg *Config, raw []byte) (WebFile, error) {
	file := WebFileFrom(cfg)
	if err := json.Unmarshal(raw, &file); err != nil {
		return WebFile{}, errors.New("설정을 해석하지 못했습니다")
	}
	trial := *cfg
	if err := file.Apply(&trial); err != nil {
		return WebFile{}, err
	}
	return file, nil
}

func WriteWebFile(dir string, file WebFile) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("설정 폴더를 만들지 못했습니다: %w", err)
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	path := webPath(dir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("웹 설정을 쓰지 못했습니다: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
