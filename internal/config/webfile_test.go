package config

import (
	"testing"
	"time"

	"saucedust/internal/domain"
)

func testConfig() *Config {
	return &Config{
		Role: domain.RoleControl, DataDir: "/tmp/saucedust-test",
		ThumbDir: "/data/thumbs", ControlBind: "127.0.0.1:8000", ControlToken: "node-token-node-token",
		DanbooruAPIKey: "secret-key", ProxyURL: "http://proxy",
		BackfillWorkers: 2, BackfillRangeSize: 10000, Concurrency: 4,
		MinConcurrency: 1, MaxConcurrency: 16, Adaptive: true,
		RatePerSecond: 5, RateBurst: 8, PollEvery: 15 * time.Second,
		ThumbSize: 384, ThumbQuality: 85, UserAgent: "saucedust/0.2",
		NetOrder:      []domain.NetMode{domain.NetDirect},
		FragmentParts: 3, EmbedBatchSize: 16, EmbedBatchWindow: 50 * time.Millisecond,
		HeartbeatEvery: 30 * time.Second, NodeTimeout: 90 * time.Second,
	}
}

func TestMergeKeepsSecretsWhenOmitted(t *testing.T) {
	cfg := testConfig()
	file, err := MergeWebFile(cfg, []byte(`{"concurrency":8,"max_concurrency":16,"min_concurrency":1}`))
	if err != nil {
		t.Fatal(err)
	}
	trial := *cfg
	if err := file.Apply(&trial); err != nil {
		t.Fatal(err)
	}
	if trial.Concurrency != 8 {
		t.Fatalf("동시 수가 %d입니다", trial.Concurrency)
	}
	if trial.ControlToken != cfg.ControlToken || trial.DanbooruAPIKey != cfg.DanbooruAPIKey {
		t.Fatal("비밀 값이 지워졌습니다")
	}
}

func TestMergeRejectsExposedShortToken(t *testing.T) {
	cfg := testConfig()
	_, err := MergeWebFile(cfg, []byte(`{"control_bind":"192.168.0.4:8000","control_token":"short"}`))
	if err == nil {
		t.Fatal("짧은 토큰을 통과시켰습니다")
	}
}

func TestApplyWebFileMissingIsFine(t *testing.T) {
	cfg := testConfig()
	cfg.DataDir = t.TempDir()
	if err := ApplyWebFile(cfg); err != nil {
		t.Fatal(err)
	}
}
