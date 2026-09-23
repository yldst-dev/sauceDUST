package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoginSessionUnlocksStats(t *testing.T) {
	server, err := New(Config{
		Bind: "127.0.0.1:0", Token: "node-token",
		AdminUser: "admin", AdminPassword: "correct-horse",
		SourceSite: "danbooru", ScopeKey: "default",
	}, Deps{Stats: stubStats{}, Images: stubImages{}, Log: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"user":"admin","password":"wrong"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("틀린 비밀번호가 %d입니다", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"user":"admin","password":"correct-horse"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("로그인이 %d입니다: %s", rec.Code, rec.Body)
	}
	cookie := rec.Result().Cookies()
	if len(cookie) == 0 || cookie[0].Name != sessionCookie {
		t.Fatal("세션 쿠키가 없습니다")
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/stats", nil)
	req.AddCookie(cookie[0])
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("세션으로 %d가 나왔습니다: %s", rec.Code, rec.Body)
	}
}

func TestOpenBindSkipsLogin(t *testing.T) {
	server, err := New(Config{
		Bind: "127.0.0.1:0", AdminUser: "admin", AdminPassword: "correct-horse",
		SourceSite: "danbooru", ScopeKey: "default",
	}, Deps{Stats: stubStats{}, Images: stubImages{}, Log: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/session", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"open":true`) {
		t.Fatalf("열린 표시가 %d %s 입니다", rec.Code, rec.Body)
	}
}

type memTelegram struct {
	token string
	err   error
}

func (m *memTelegram) Status() (bool, string) {
	if m.token == "" {
		return false, ""
	}
	return true, "…" + m.token[len(m.token)-4:]
}

func (m *memTelegram) Set(_ context.Context, token string) error {
	if m.err != nil {
		return m.err
	}
	m.token = token
	return nil
}

func TestTelegramSettingHidesToken(t *testing.T) {
	secret := "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZabcd"
	tg := &memTelegram{}
	server, err := New(Config{
		Bind: "127.0.0.1:0", Token: "node-token",
		AdminUser: "admin", AdminPassword: "correct-horse",
		SourceSite: "danbooru", ScopeKey: "default",
	}, Deps{Stats: stubStats{}, Images: stubImages{}, Log: quiet(), Telegram: tg})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"user":"admin","password":"correct-horse"}`))
	handler.ServeHTTP(rec, req)
	cookie := rec.Result().Cookies()[0]

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/settings/telegram", strings.NewReader(`{"token":"`+secret+`"}`))
	req.AddCookie(cookie)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("저장이 %d입니다: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("응답에 토큰이 있습니다: %s", rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["telegram_set"] != true {
		t.Fatalf("저장 표시가 없습니다: %s", rec.Body)
	}
	if tg.token != secret {
		t.Fatal("토큰이 반영되지 않았습니다")
	}
}

func TestEnsureAdminWaitsForBrowser(t *testing.T) {
	dir := t.TempDir()
	user, pass, file, err := EnsureAdmin(dir, "", "", quiet())
	if err != nil {
		t.Fatal(err)
	}
	if user != "admin" || pass != "" || file != "" {
		t.Fatalf("계정이 %+v %q %s 입니다", user, pass, file)
	}
	if _, err := os.Stat(filepath.Join(dir, "web-admin.json")); !os.IsNotExist(err) {
		t.Fatal("계정을 미리 만들었습니다")
	}
}

func TestSetupThenLogin(t *testing.T) {
	dir := t.TempDir()
	server, err := New(Config{
		Bind: "127.0.0.1:0", Token: "node-token",
		DataDir: dir, SourceSite: "danbooru", ScopeKey: "default",
	}, Deps{Stats: stubStats{}, Images: stubImages{}, Log: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.Handler()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/session", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"setup":true`) {
		t.Fatalf("처음 세션이 %d %s 입니다", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/setup", strings.NewReader(`{"user":"ops","password":"short"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("짧은 비밀번호가 %d입니다", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/setup", strings.NewReader(`{"user":"ops","password":"correct-horse","confirm":"other-horse"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("다른 비밀번호가 %d입니다", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/setup", strings.NewReader(`{"user":"ops","password":"correct-horse","confirm":"correct-horse"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("계정 만들기가 %d입니다: %s", rec.Code, rec.Body)
	}
	info, err := os.Stat(filepath.Join(dir, "web-admin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("권한이 %o입니다", info.Mode().Perm())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/setup", strings.NewReader(`{"user":"ops","password":"correct-horse","confirm":"correct-horse"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("두 번째 계정 만들기가 %d입니다", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/login", strings.NewReader(`{"user":"ops","password":"correct-horse"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("만든 계정 로그인이 %d입니다: %s", rec.Code, rec.Body)
	}
}
