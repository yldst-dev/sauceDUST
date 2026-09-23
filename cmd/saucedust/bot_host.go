package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"saucedust/internal/adapter/telegram"
	"saucedust/internal/app"
)

type botHost struct {
	rt     *nodeRuntime
	search app.ImageSearcher
	log    *slog.Logger
	file   string

	mu    sync.Mutex
	token string
	child context.CancelFunc
	wake  chan struct{}
}

func newBotHost(rt *nodeRuntime, search app.ImageSearcher, token, file string, log *slog.Logger) *botHost {
	return &botHost{
		rt: rt, search: search, log: log, file: file,
		token: strings.TrimSpace(token),
		wake:  make(chan struct{}, 1),
	}
}

func loadTelegramFile(path string) (string, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var file struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		return "", false, fmt.Errorf("텔레그램 설정 파일을 해석하지 못했습니다: %w", err)
	}
	return strings.TrimSpace(file.Token), true, nil
}

func writeTelegramFile(path, token string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("텔레그램 설정 폴더를 만들지 못했습니다: %w", err)
	}
	raw, err := json.Marshal(struct {
		Token string `json:"token"`
	}{Token: token})
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("텔레그램 설정 파일을 쓰지 못했습니다: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func maskBotToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	r := []rune(token)
	if len(r) <= 4 {
		return "••••"
	}
	return "…" + string(r[len(r)-4:])
}

func validBotToken(token string) bool {
	id, secret, ok := strings.Cut(token, ":")
	if !ok || secret == "" || strings.Contains(secret, ":") {
		return false
	}
	if len(id) < 6 || len(secret) < 20 || len(token) > 120 {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	for _, r := range secret {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func (h *botHost) Status() (bool, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.token == "" {
		return false, ""
	}
	return true, maskBotToken(h.token)
}

func (h *botHost) Set(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token != "" {
		if !validBotToken(token) {
			return errors.New("봇 토큰 형식이 아닙니다. BotFather가 준 값을 그대로 넣으십시오")
		}
		if err := h.check(ctx, token); err != nil {
			return err
		}
	}
	if err := writeTelegramFile(h.file, token); err != nil {
		return err
	}
	h.mu.Lock()
	same := h.token == token
	h.token = token
	cancel := h.child
	h.mu.Unlock()
	if same {
		return nil
	}
	if cancel != nil {
		cancel()
	}
	h.poke()
	if token == "" {
		h.log.Info("텔레그램 봇을 껐습니다")
	} else {
		h.log.Info("텔레그램 봇 토큰을 바꿨습니다", slog.String("끝", maskBotToken(token)))
	}
	return nil
}

func (h *botHost) check(ctx context.Context, token string) error {
	chain, err := h.rt.newNetChain()
	if err != nil {
		return err
	}
	client, err := telegram.New(chain, telegram.Options{Token: token, PollTimeout: time.Second})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, err := client.Identity(ctx); err != nil {
		return fmt.Errorf("텔레그램이 이 토큰을 받아들이지 않았습니다: %s", err.Error())
	}
	return nil
}

func (h *botHost) poke() {
	select {
	case h.wake <- struct{}{}:
	default:
	}
}

func (h *botHost) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		h.mu.Lock()
		token := h.token
		h.mu.Unlock()
		if token == "" {
			select {
			case <-ctx.Done():
				return nil
			case <-h.wake:
			}
			continue
		}
		bot, err := h.rt.newBotToken(h.search, token)
		if err != nil {
			h.log.Error("텔레그램 봇을 만들지 못했습니다", slog.String("error", err.Error()))
			select {
			case <-ctx.Done():
				return nil
			case <-h.wake:
			case <-time.After(5 * time.Second):
			}
			continue
		}
		if bot == nil {
			continue
		}
		child, cancel := context.WithCancel(ctx)
		h.mu.Lock()
		h.child = cancel
		stale := h.token != token
		h.mu.Unlock()
		if stale {
			cancel()
			continue
		}
		done := make(chan struct{})
		go func() {
			_ = bot.Run(child)
			close(done)
		}()
		select {
		case <-ctx.Done():
			cancel()
			<-done
			return nil
		case <-h.wake:
			cancel()
			<-done
		case <-done:
			cancel()
		}
	}
}
