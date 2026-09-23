package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidBotToken(t *testing.T) {
	ok := "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcd"
	if !validBotToken(ok) {
		t.Fatal("정상적인 토큰을 거절했습니다")
	}
	for _, bad := range []string{"", "no-colon", "123:short", "abc:ABCDEFGHIJKLMNOPQRSTUVWXYZabcd", ok + "\n"} {
		if validBotToken(bad) {
			t.Fatalf("잘못된 토큰을 통과시켰습니다: %q", bad)
		}
	}
}

func TestMaskBotTokenKeepsTail(t *testing.T) {
	got := maskBotToken("123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcd")
	if strings.Contains(got, "ABCDEFGHIJKLMNOP") || !strings.HasSuffix(got, "abcd") {
		t.Fatalf("가린 값이 %q입니다", got)
	}
}

func TestTelegramFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "telegram.json")
	token := "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcd"
	if err := writeTelegramFile(path, token); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("권한이 %o입니다", info.Mode().Perm())
	}
	got, ok, err := loadTelegramFile(path)
	if err != nil || !ok || got != token {
		t.Fatalf("읽기 결과 %q %v %v", got, ok, err)
	}
	if _, ok, err := loadTelegramFile(filepath.Join(dir, "missing.json")); err != nil || ok {
		t.Fatalf("없는 파일이 %v %v", ok, err)
	}
}
