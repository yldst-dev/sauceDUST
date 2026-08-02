// Package telegram은 Telegram Bot API를 감쌉니다.
// 봇의 행동 규칙은 여기 없습니다. 전송만 담당합니다.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"saucedust/internal/domain"
)

// Doer는 실제 요청을 보내는 쪽입니다. netpath.Chain이 이 모양을 만족하므로
// 텔레그램이 막혀 있어도 같은 우회 경로를 그대로 씁니다.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

const (
	apiBase      = "https://api.telegram.org"
	maxFileBytes = 32 << 20
)

type Options struct {
	Token string
	// PollTimeout은 long polling 대기 시간입니다. 길수록 요청 수가 줄어듭니다.
	PollTimeout time.Duration
	BaseURL     string
}

type Client struct {
	token   string
	base    string
	poll    time.Duration
	http    Doer
	timeout time.Duration
}

func New(doer Doer, opts Options) (*Client, error) {
	if doer == nil {
		return nil, errors.New("HTTP 실행기가 없습니다")
	}
	if strings.TrimSpace(opts.Token) == "" {
		return nil, errors.New("봇 토큰이 비어 있습니다")
	}
	if opts.PollTimeout <= 0 {
		opts.PollTimeout = 30 * time.Second
	}
	if opts.BaseURL == "" {
		opts.BaseURL = apiBase
	}

	return &Client{
		token: opts.Token,
		base:  strings.TrimRight(opts.BaseURL, "/"),
		poll:  opts.PollTimeout,
		http:  doer,
		// 응답 대기는 long polling 시간보다 넉넉해야 합니다.
		timeout: opts.PollTimeout + 30*time.Second,
	}, nil
}

// GetUpdates는 새 메시지를 기다립니다. offset보다 큰 것만 돌려줍니다.
func (c *Client) GetUpdates(ctx context.Context, offset int64) ([]domain.BotUpdate, error) {
	body := map[string]any{
		"timeout":         int(c.poll.Seconds()),
		"allowed_updates": []string{"message"},
	}
	if offset > 0 {
		body["offset"] = offset
	}

	var payload struct {
		Result []struct {
			UpdateID int64 `json:"update_id"`
			Message  *struct {
				MessageID int64 `json:"message_id"`
				Chat      struct {
					ID int64 `json:"id"`
				} `json:"chat"`
				From *struct {
					ID       int64  `json:"id"`
					Username string `json:"username"`
				} `json:"from"`
				Text     string    `json:"text"`
				Caption  string    `json:"caption"`
				Photo    []photo   `json:"photo"`
				Document *document `json:"document"`
			} `json:"message"`
		} `json:"result"`
	}
	if err := c.call(ctx, "getUpdates", body, &payload); err != nil {
		return nil, err
	}

	out := make([]domain.BotUpdate, 0, len(payload.Result))
	for _, item := range payload.Result {
		update := domain.BotUpdate{ID: item.UpdateID}
		if item.Message != nil {
			update.Message = &domain.BotMessage{
				ChatID:  item.Message.Chat.ID,
				Text:    strings.TrimSpace(item.Message.Text),
				Caption: strings.TrimSpace(item.Message.Caption),
				FileID:  pickImage(item.Message.Photo, item.Message.Document),
			}
			if item.Message.From != nil {
				update.Message.SenderID = item.Message.From.ID
				update.Message.Sender = item.Message.From.Username
			}
		}
		out = append(out, update)
	}
	return out, nil
}

type photo struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type document struct {
	FileID   string `json:"file_id"`
	MimeType string `json:"mime_type"`
}

// pickImage는 가장 큰 사진을 고릅니다. 텔레그램은 여러 해상도를 함께 보냅니다.
// 사진이 없으면 이미지 파일로 보낸 것을 봅니다.
func pickImage(photos []photo, doc *document) string {
	var (
		best string
		area int64
	)
	for _, p := range photos {
		size := p.FileSize
		if size == 0 {
			size = int64(p.Width) * int64(p.Height)
		}
		if size > area {
			area = size
			best = p.FileID
		}
	}
	if best != "" {
		return best
	}
	if doc != nil && strings.HasPrefix(doc.MimeType, "image/") {
		return doc.FileID
	}
	return ""
}

// DownloadFile은 file_id로 실제 바이트를 받습니다. 두 단계입니다.
func (c *Client) DownloadFile(ctx context.Context, fileID string) ([]byte, error) {
	var meta struct {
		Result struct {
			FilePath string `json:"file_path"`
			FileSize int64  `json:"file_size"`
		} `json:"result"`
	}
	if err := c.call(ctx, "getFile", map[string]any{"file_id": fileID}, &meta); err != nil {
		return nil, err
	}
	if meta.Result.FilePath == "" {
		return nil, errors.New("파일 경로를 받지 못했습니다")
	}
	if meta.Result.FileSize > maxFileBytes {
		return nil, fmt.Errorf("파일이 너무 큽니다: %d바이트", meta.Result.FileSize)
	}

	url := fmt.Sprintf("%s/file/bot%s/%s", c.base, c.token, meta.Result.FilePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("파일을 받지 못했습니다: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("파일 응답이 %d입니다", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxFileBytes {
		return nil, errors.New("파일이 크기 제한을 넘었습니다")
	}
	return data, nil
}

func (c *Client) SendMessage(ctx context.Context, msg domain.BotReply) error {
	body := map[string]any{
		"chat_id":                  msg.ChatID,
		"text":                     msg.Text,
		"disable_web_page_preview": msg.DisablePreview,
	}
	if len(msg.Buttons) > 0 {
		row := make([]map[string]string, 0, len(msg.Buttons))
		for _, b := range msg.Buttons {
			row = append(row, map[string]string{"text": b.Label, "url": b.URL})
		}
		body["reply_markup"] = map[string]any{
			"inline_keyboard": [][]map[string]string{row},
		}
	}
	return c.call(ctx, "sendMessage", body, nil)
}

func (c *Client) call(ctx context.Context, method string, body any, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/bot%s/%s", c.base, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("텔레그램 %s 호출에 실패했습니다: %w", method, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return &APIError{Method: method, Status: resp.StatusCode, Body: describe(raw)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("텔레그램 %s 응답을 해석하지 못했습니다: %w", method, err)
	}
	return nil
}

type APIError struct {
	Method string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("텔레그램 %s 응답이 %d입니다: %s", e.Method, e.Status, e.Body)
}

// Retryable은 잠시 뒤 다시 시도할 가치가 있는지 알려줍니다.
// 토큰이 틀렸으면 아무리 다시 해도 소용없습니다.
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

func describe(raw []byte) string {
	var payload struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &payload); err == nil && payload.Description != "" {
		return payload.Description
	}
	if len(raw) > 200 {
		raw = raw[:200]
	}
	return strings.TrimSpace(string(raw))
}
