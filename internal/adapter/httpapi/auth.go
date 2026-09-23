package httpapi

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const sessionCookie = "saucedust_session"

const sessionTTL = 14 * 24 * time.Hour

const maxLoginFails = 8

type failState struct {
	n     int
	until time.Time
}

type adminFile struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

func EnsureAdmin(dir, envUser, envPass string, log *slog.Logger) (string, string, string, error) {
	if log == nil {
		log = slog.Default()
	}
	user := strings.TrimSpace(envUser)
	if user == "" {
		user = "admin"
	}
	if strings.TrimSpace(dir) == "" {
		return "", "", "", errors.New("자료 폴더가 없어 웹 로그인 계정을 만들 수 없습니다")
	}
	path := filepath.Join(dir, "web-admin.json")
	loadedUser, loadedPass, ok, err := readAdminFile(path)
	if err != nil {
		return "", "", "", err
	}
	if ok {
		return loadedUser, loadedPass, path, nil
	}
	pass := strings.TrimSpace(envPass)
	if pass != "" {
		log.Info("웹 로그인 비밀번호는 환경 변수에서 읽었습니다", slog.String("사용자", user))
		return user, pass, "", nil
	}
	log.Info("웹 계정이 없습니다. 브라우저에서 처음 접속할 때 만듭니다")
	return user, "", "", nil
}

func readAdminFile(path string) (string, string, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	var file adminFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return "", "", false, fmt.Errorf("웹 로그인 파일을 해석하지 못했습니다: %w", err)
	}
	if strings.TrimSpace(file.Password) == "" {
		return "", "", false, nil
	}
	user := strings.TrimSpace(file.User)
	if user == "" {
		user = "admin"
	}
	return user, file.Password, true, nil
}

func writeAdminFile(path, user, password string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("웹 로그인 폴더를 만들지 못했습니다: %w", err)
	}
	raw, err := json.Marshal(adminFile{User: user, Password: password})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("웹 로그인 파일을 쓰지 못했습니다: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (s *Server) allowed(r *http.Request) bool {
	if s.cfg.Token == "" {
		return true
	}
	supplied := r.Header.Get("authorization")
	expected := "Bearer " + s.cfg.Token
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(expected)) == 1 {
		return true
	}
	return s.validSession(r)
}

func (s *Server) validSession(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[cookie.Value]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, cookie.Value)
		return false
	}
	return true
}

func (s *Server) startSession(w http.ResponseWriter) error {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return err
	}
	id := hex.EncodeToString(buf)
	s.mu.Lock()
	s.sessions[id] = time.Now().Add(sessionTTL)
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	return nil
}

func (s *Server) passwordMatches(pass string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.AdminPassword == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.AdminPassword)) == 1
}

func (s *Server) userMatches(user string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.AdminUser == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.AdminUser)) == 1
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) lockedOut(r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.fails[clientIP(r)]
	return state.n >= maxLoginFails && time.Now().Before(state.until)
}

func (s *Server) noteLoginFail(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ip := clientIP(r)
	state := s.fails[ip]
	state.n++
	if state.n >= maxLoginFails {
		state.until = time.Now().Add(time.Minute)
	}
	s.fails[ip] = state
}

func (s *Server) clearLoginFail(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.fails, clientIP(r))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.lockedOut(r) {
		writeError(w, http.StatusTooManyRequests, errors.New("시도가 너무 많습니다. 잠시 뒤에 다시 하십시오"))
		return
	}
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("요청을 해석하지 못했습니다"))
		return
	}
	if strings.TrimSpace(s.cfg.AdminPassword) == "" {
		writeError(w, http.StatusServiceUnavailable, errors.New("웹 로그인 비밀번호가 없습니다"))
		return
	}
	if !s.userMatches(body.User) || !s.passwordMatches(body.Password) {
		s.noteLoginFail(r)
		writeError(w, http.StatusUnauthorized, errors.New("사용자 이름 또는 비밀번호가 다릅니다"))
		return
	}
	s.clearLoginFail(r)
	if err := s.startSession(w); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user": s.cfg.AdminUser})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.mu.Lock()
		delete(s.sessions, cookie.Value)
		s.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if s.validSession(r) {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"user":          s.cfg.AdminUser,
		})
		return
	}
	if strings.TrimSpace(s.cfg.AdminPassword) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"setup": true})
		return
	}
	if s.cfg.Token == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"open":          true,
		})
		return
	}
	writeError(w, http.StatusUnauthorized, errors.New("로그인이 필요합니다"))
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("요청을 해석하지 못했습니다"))
		return
	}
	user := strings.TrimSpace(body.User)
	if user == "" || strings.ContainsAny(user, "\r\n") || len([]rune(user)) > 64 {
		writeError(w, http.StatusBadRequest, errors.New("사용자 이름을 입력하십시오"))
		return
	}
	if len([]rune(body.Password)) < 8 {
		writeError(w, http.StatusBadRequest, errors.New("비밀번호는 8자 이상이어야 합니다"))
		return
	}
	if body.Password != body.Confirm {
		writeError(w, http.StatusBadRequest, errors.New("비밀번호가 서로 다릅니다"))
		return
	}
	if strings.TrimSpace(s.cfg.DataDir) == "" {
		writeError(w, http.StatusInternalServerError, errors.New("계정을 저장할 폴더가 없습니다"))
		return
	}
	path := filepath.Join(s.cfg.DataDir, "web-admin.json")
	s.mu.Lock()
	if strings.TrimSpace(s.cfg.AdminPassword) != "" {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, errors.New("이미 계정이 있습니다. 로그인하십시오"))
		return
	}
	s.mu.Unlock()
	if err := writeAdminFile(path, user, body.Password); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.mu.Lock()
	if strings.TrimSpace(s.cfg.AdminPassword) != "" {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, errors.New("이미 계정이 있습니다. 로그인하십시오"))
		return
	}
	s.cfg.AdminUser = user
	s.cfg.AdminPassword = body.Password
	s.cfg.AdminFile = path
	s.mu.Unlock()
	if err := s.startSession(w); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user": user})
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	path := s.cfg.AdminFile
	if path == "" {
		if strings.TrimSpace(s.cfg.DataDir) == "" {
			writeError(w, http.StatusConflict, errors.New("비밀번호를 저장할 폴더가 없습니다"))
			return
		}
		path = filepath.Join(s.cfg.DataDir, "web-admin.json")
	}
	var body struct {
		Current string `json:"current"`
		Next    string `json:"next"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("요청을 해석하지 못했습니다"))
		return
	}
	next := strings.TrimSpace(body.Next)
	if len([]rune(next)) < 8 {
		writeError(w, http.StatusBadRequest, errors.New("새 비밀번호는 8자 이상이어야 합니다"))
		return
	}
	if !s.passwordMatches(body.Current) {
		writeError(w, http.StatusUnauthorized, errors.New("현재 비밀번호가 다릅니다"))
		return
	}
	if err := writeAdminFile(path, s.cfg.AdminUser, next); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.mu.Lock()
	s.cfg.AdminFile = path
	s.cfg.AdminPassword = next
	s.sessions = map[string]time.Time{}
	s.mu.Unlock()
	if err := s.startSession(w); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"user": s.cfg.AdminUser})
}
