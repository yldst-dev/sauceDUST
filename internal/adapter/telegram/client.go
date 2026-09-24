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
	if meta.Result.FileSize > maxFileBytes {
		return nil, fmt.Errorf("파일이 너무 큽니다: %d바이트", meta.Result.FileSize)
	}

	// 경로를 먼저 봅니다. 이 값은 텔레그램 응답에서 오는 바깥 입력입니다.
	//
	// 그대로 주소에 이어 붙이면, 못 쓰는 글자가 하나 섞인 것만으로
	// http.NewRequest가 주소 전체를 담은 오류를 냅니다. 그 주소에는 봇
	// 토큰이 들어 있어서, 상대가 보내는 값으로 토큰을 뽑아낼 수 있습니다.
	if err := checkFilePath(meta.Result.FilePath); err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/file/bot%s/%s", c.base, c.token, meta.Result.FilePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		// 주소를 못 만든 오류에도 그 주소가 들어 있습니다.
		return nil, c.maskErr("파일 주소를 만들지 못했습니다", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// 이 주소에도 토큰이 들어 있습니다. call과 같은 이유로 가립니다.
		return nil, c.maskErr("파일을 받지 못했습니다", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("파일 응답이 %d입니다", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFileBytes+1))
	if err != nil {
		// 본문을 읽다 끊기면 전송 계층이 *url.Error를 냅니다. 주소가 있습니다.
		return nil, c.maskErr("파일 본문을 읽지 못했습니다", err)
	}
	if int64(len(data)) > maxFileBytes {
		return nil, errors.New("파일이 크기 제한을 넘었습니다")
	}
	return data, nil
}

func (c *Client) Identity(ctx context.Context) (string, error) {
	var payload struct {
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if err := c.call(ctx, "getMe", map[string]any{}, &payload); err != nil {
		return "", err
	}
	name := strings.TrimSpace(payload.Result.Username)
	if name == "" {
		return "", errors.New("텔레그램이 봇 이름을 주지 않았습니다")
	}
	return name, nil
}

func (c *Client) SendMessage(ctx context.Context, msg domain.BotReply) (int64, error) {
	var payload struct {
		Result struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := c.call(ctx, "sendMessage", messageBody(msg), &payload); err != nil {
		return 0, err
	}
	return payload.Result.MessageID, nil
}

func (c *Client) EditMessage(ctx context.Context, msg domain.BotReply) error {
	body := messageBody(msg)
	body["message_id"] = msg.MessageID
	if len(msg.Buttons) == 0 {
		body["reply_markup"] = map[string]any{"inline_keyboard": []any{}}
	}
	return c.call(ctx, "editMessageText", body, nil)
}

func messageBody(msg domain.BotReply) map[string]any {
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
	return body
}

// maskToken은 문자열에서 봇 토큰을 지웁니다.
//
// 토큰이 주소 경로에 들어가는 API라, 주소를 담는 오류가 그대로 흘러
// 나갑니다. 로그와 저장소 양쪽에 남으므로 내보내기 전에 가립니다.
func (c *Client) maskToken(text string) string {
	if c.token == "" {
		return text
	}
	return strings.ReplaceAll(text, c.token, "<토큰 가림>")
}

// maskErr는 오류를 내보내기 전에 토큰을 지웁니다.
//
// %w로 감싸면 감싼 오류가 원문을 그대로 들고 있어서, 부르는 쪽이
// Unwrap을 하거나 %+v로 찍는 순간 다시 새어 나옵니다. 문자열로 눌러
// 담습니다. 이 꾸러미에서 풀어 봐야 하는 것은 APIError뿐이고 그것은
// 주소를 담지 않으므로 이 길을 지나지 않습니다.
func (c *Client) maskErr(what string, err error) error {
	return fmt.Errorf("%s: %s", what, c.maskToken(err.Error()))
}

// checkFilePath는 텔레그램이 알려 준 파일 경로가 주소에 넣어도 되는지 봅니다.
//
// 통과시키는 것은 글자, 숫자, 밑줄, 붙임표, 점, 그리고 빗금뿐입니다.
// 실제 값은 photos/file_123.jpg 같은 모양이라 이걸로 충분합니다.
func checkFilePath(path string) error {
	if path == "" {
		return errors.New("파일 경로를 받지 못했습니다")
	}
	if len(path) > 512 {
		return errors.New("파일 경로가 너무 깁니다")
	}
	// 상위로 올라가는 것과 절대 경로를 막습니다. 주소 경로가 바뀝니다.
	if strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
		return errors.New("파일 경로 모양이 잘못되었습니다")
	}
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.' || r == '/':
		default:
			return errors.New("파일 경로에 쓸 수 없는 글자가 있습니다")
		}
	}
	return nil
}

func (c *Client) call(ctx context.Context, method string, body any, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}

	// long polling은 응답이 늦게 옵니다. 부르는 쪽 시간이 그보다 짧으면
	// 봇이 매번 끊깁니다. 대기 시간보다 넉넉한 상한을 여기서 겁니다.
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	url := fmt.Sprintf("%s/bot%s/%s", c.base, c.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		// 주소를 못 만든 오류에도 그 주소가 들어 있습니다.
		return c.maskErr("텔레그램 "+method+" 주소를 만들지 못했습니다", err)
	}
	req.Header.Set("content-type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// *url.Error는 요청 주소를 그대로 담고, 그 주소에는 봇 토큰이
		// 들어 있습니다. 가리지 않으면 폴링이 실패할 때마다 토큰 전체가
		// 로그와 net_probes.detail에 평문으로 남습니다.
		return c.maskErr("텔레그램 "+method+" 호출에 실패했습니다", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		// 본문을 읽다 끊기면 전송 계층이 *url.Error를 냅니다. 주소가 있습니다.
		return c.maskErr("텔레그램 "+method+" 응답을 읽지 못했습니다", err)
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
