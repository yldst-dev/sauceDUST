package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"saucedust/internal/domain"
)

type fakeAPI struct {
	updates  string
	filePath string
	fileData []byte
	sent     []map[string]any
	status   int
	calls    map[string]int
}

func newAPI() *fakeAPI {
	return &fakeAPI{
		filePath: "photos/file_1.jpg",
		fileData: []byte("image-bytes"),
		calls:    map[string]int{},
	}
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if strings.Contains(path, "/file/bot") {
		w.Write(f.fileData)
		return
	}

	method := path[strings.LastIndex(path, "/")+1:]
	f.calls[method]++

	if f.status != 0 {
		w.WriteHeader(f.status)
		w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
		return
	}

	w.Header().Set("content-type", "application/json")
	switch method {
	case "getUpdates":
		w.Write([]byte(f.updates))
	case "getFile":
		w.Write([]byte(`{"ok":true,"result":{"file_path":"` + f.filePath + `","file_size":11}}`))
	case "sendMessage":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.sent = append(f.sent, body)
		w.Write([]byte(`{"ok":true}`))
	default:
		w.Write([]byte(`{"ok":true}`))
	}
}

func newClient(t *testing.T, api *fakeAPI) *Client {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)

	client, err := New(server.Client(), Options{
		Token: "test-token", BaseURL: server.URL, PollTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}
	return client
}

func TestNewRejectsEmptyToken(t *testing.T) {
	server := httptest.NewServer(newAPI())
	defer server.Close()

	if _, err := New(server.Client(), Options{Token: "  "}); err == nil {
		t.Fatal("빈 토큰은 거부해야 합니다")
	}
	if _, err := New(nil, Options{Token: "x"}); err == nil {
		t.Fatal("실행기 없이 만들면 안 됩니다")
	}
}

// 텔레그램은 사진을 여러 해상도로 보냅니다. 가장 큰 것을 골라야 검색이 정확합니다.
func TestGetUpdatesPicksLargestPhoto(t *testing.T) {
	api := newAPI()
	api.updates = `{"ok":true,"result":[{"update_id":10,"message":{
		"message_id":1,"chat":{"id":555},"from":{"id":77,"username":"someone"},
		"photo":[
			{"file_id":"small","file_size":1000,"width":90,"height":90},
			{"file_id":"large","file_size":50000,"width":1280,"height":1280},
			{"file_id":"medium","file_size":9000,"width":320,"height":320}
		]}}]}`

	updates, err := newClient(t, api).GetUpdates(context.Background(), 0)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("%d건이 왔습니다", len(updates))
	}

	msg := updates[0].Message
	switch {
	case msg == nil:
		t.Fatal("메시지가 없습니다")
	case msg.FileID != "large":
		t.Fatalf("고른 사진이 %q입니다. large를 기대했습니다", msg.FileID)
	case msg.ChatID != 555:
		t.Fatalf("대화방이 %d입니다", msg.ChatID)
	case msg.SenderID != 77:
		t.Fatalf("보낸이가 %d입니다", msg.SenderID)
	}
}

// 크기 정보가 없으면 해상도로 고릅니다.
func TestGetUpdatesFallsBackToResolution(t *testing.T) {
	api := newAPI()
	api.updates = `{"ok":true,"result":[{"update_id":1,"message":{
		"chat":{"id":1},"photo":[
			{"file_id":"small","width":100,"height":100},
			{"file_id":"big","width":900,"height":900}
		]}}]}`

	updates, _ := newClient(t, api).GetUpdates(context.Background(), 0)
	if updates[0].Message.FileID != "big" {
		t.Fatalf("고른 사진이 %q입니다", updates[0].Message.FileID)
	}
}

// 이미지 파일로 보낸 것도 받아야 합니다. 압축을 피하려고 그렇게 보냅니다.
func TestGetUpdatesAcceptsImageDocument(t *testing.T) {
	api := newAPI()
	api.updates = `{"ok":true,"result":[{"update_id":1,"message":{
		"chat":{"id":1},"document":{"file_id":"doc-1","mime_type":"image/png"}}}]}`

	updates, _ := newClient(t, api).GetUpdates(context.Background(), 0)
	if updates[0].Message.FileID != "doc-1" {
		t.Fatalf("파일이 %q입니다", updates[0].Message.FileID)
	}
}

// 이미지가 아닌 파일은 무시해야 합니다.
func TestGetUpdatesIgnoresNonImageDocument(t *testing.T) {
	api := newAPI()
	api.updates = `{"ok":true,"result":[{"update_id":1,"message":{
		"chat":{"id":1},"document":{"file_id":"doc-1","mime_type":"application/pdf"}}}]}`

	updates, _ := newClient(t, api).GetUpdates(context.Background(), 0)
	if updates[0].Message.HasImage() {
		t.Fatal("PDF를 이미지로 받았습니다")
	}
}

func TestGetUpdatesHandlesTextMessage(t *testing.T) {
	api := newAPI()
	api.updates = `{"ok":true,"result":[{"update_id":5,"message":{
		"chat":{"id":9},"text":"  /start  "}}]}`

	updates, _ := newClient(t, api).GetUpdates(context.Background(), 0)
	if updates[0].Message.Text != "/start" {
		t.Fatalf("본문이 %q입니다. 앞뒤 공백을 다듬어야 합니다", updates[0].Message.Text)
	}
}

func TestGetUpdatesEmpty(t *testing.T) {
	api := newAPI()
	api.updates = `{"ok":true,"result":[]}`

	updates, err := newClient(t, api).GetUpdates(context.Background(), 0)
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if len(updates) != 0 {
		t.Fatalf("%d건이 왔습니다", len(updates))
	}
}

func TestDownloadFile(t *testing.T) {
	api := newAPI()
	data, err := newClient(t, api).DownloadFile(context.Background(), "photo-1")
	if err != nil {
		t.Fatalf("내려받기 실패: %v", err)
	}
	if string(data) != "image-bytes" {
		t.Fatalf("받은 내용이 %q입니다", data)
	}
	if api.calls["getFile"] != 1 {
		t.Fatalf("getFile이 %d번 불렸습니다", api.calls["getFile"])
	}
}

func TestSendMessageWithButtons(t *testing.T) {
	api := newAPI()
	err := newClient(t, api).SendMessage(context.Background(), domain.BotReply{
		ChatID: 555, Text: "찾았습니다",
		Buttons: []domain.BotButton{
			{Label: "원본", URL: "https://danbooru.example/posts/1"},
		},
	})
	if err != nil {
		t.Fatalf("전송 실패: %v", err)
	}
	if len(api.sent) != 1 {
		t.Fatalf("전송이 %d건입니다", len(api.sent))
	}

	body := api.sent[0]
	if body["text"] != "찾았습니다" {
		t.Fatalf("본문이 %v입니다", body["text"])
	}
	markup, ok := body["reply_markup"].(map[string]any)
	if !ok {
		t.Fatal("단추가 붙지 않았습니다")
	}
	rows := markup["inline_keyboard"].([]any)
	if len(rows) != 1 || len(rows[0].([]any)) != 1 {
		t.Fatalf("단추 배치가 %v입니다", rows)
	}
}

func TestSendMessageWithoutButtons(t *testing.T) {
	api := newAPI()
	err := newClient(t, api).SendMessage(context.Background(), domain.BotReply{
		ChatID: 1, Text: "안내",
	})
	if err != nil {
		t.Fatalf("전송 실패: %v", err)
	}
	if _, has := api.sent[0]["reply_markup"]; has {
		t.Fatal("단추가 없으면 reply_markup을 넣으면 안 됩니다")
	}
}

// 토큰이 틀리면 아무리 다시 해도 소용없습니다. 재시도 대상이 아닙니다.
func TestUnauthorizedIsNotRetryable(t *testing.T) {
	api := newAPI()
	api.status = http.StatusUnauthorized

	err := newClient(t, api).SendMessage(context.Background(), domain.BotReply{ChatID: 1, Text: "x"})
	if err == nil {
		t.Fatal("401은 오류여야 합니다")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("오류 종류가 %T입니다", err)
	}
	if apiErr.Retryable() {
		t.Fatal("401을 재시도하면 안 됩니다")
	}
	if !strings.Contains(apiErr.Body, "Unauthorized") {
		t.Fatalf("설명이 %q입니다", apiErr.Body)
	}
}

func TestServerErrorIsRetryable(t *testing.T) {
	api := newAPI()
	api.status = http.StatusBadGateway

	err := newClient(t, api).SendMessage(context.Background(), domain.BotReply{ChatID: 1, Text: "x"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.Retryable() {
		t.Fatalf("502는 재시도 대상이어야 합니다: %v", err)
	}
}

func TestDownloadRejectsOversizeFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "getFile") {
			w.Write([]byte(`{"ok":true,"result":{"file_path":"x.jpg","file_size":99999999999}}`))
			return
		}
		io.Copy(io.Discard, r.Body)
	}))
	defer server.Close()

	client, _ := New(server.Client(), Options{Token: "t", BaseURL: server.URL})
	if _, err := client.DownloadFile(context.Background(), "big"); err == nil {
		t.Fatal("너무 큰 파일은 거부해야 합니다")
	}
}
