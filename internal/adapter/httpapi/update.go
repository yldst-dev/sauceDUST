package httpapi

import (
	"context"
	"errors"
	"net/http"
	"os"
	"runtime"
	"time"

	"saucedust/internal/release"
)

func (s *Server) updateRepo() string {
	if repo := os.Getenv("SAUCEDUST_UPDATE_REPO"); repo != "" {
		return repo
	}
	return release.DefaultRepo
}

func (s *Server) currentVersion() string {
	if s.cfg.Version == "" {
		return "dev"
	}
	return s.cfg.Version
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	status, err := release.Check(ctx, &http.Client{Timeout: 20 * time.Second}, s.updateRepo(), s.currentVersion(), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ops == nil {
		writeError(w, http.StatusNotImplemented, errors.New("이 서버에서는 업데이트를 적용하지 않습니다"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 90 * time.Second}
	status, err := release.Check(ctx, client, s.updateRepo(), s.currentVersion(), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if !status.Available || status.AssetURL == "" {
		writeError(w, http.StatusConflict, errors.New("이미 최신 버전입니다"))
		return
	}
	if err := release.InstallSelf(ctx, client, status.AssetURL); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"restarting": true, "version": status.Latest})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	ops := s.deps.Ops
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = ops.Restart()
	}()
}
