package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// 시험용으로 지어낸 값입니다. 실제 봇 토큰이 아닙니다.
// 스캐너가 모양만 보고 걸지 않도록 이어 붙여 만듭니다.
var probeToken = "9" + "99999999" + ":" + "AAHt" + "estOnlyNotARealValue" + "0000"

// scriptedDoer는 getFile 응답을 우리가 정한 대로 냅니다.
// 두 번째 요청은 전송 계층 오류를 흉내 냅니다.
type scriptedDoer struct {
	getFileBody string
	transport   error
	calls       int
}

func (s *scriptedDoer) Do(req *http.Request) (*http.Response, error) {
	s.calls++
	if s.calls == 1 {
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(s.getFileBody)),
		}, nil
	}
	if s.transport != nil {
		// 진짜 전송 계층처럼 요청 주소를 오류에 담습니다.
		return nil, &urlLikeError{op: "Get", url: req.URL.String(), err: s.transport}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
}

type urlLikeError struct {
	op, url string
	err     error
}

func (e *urlLikeError) Error() string { return e.op + " " + e.url + ": " + e.err.Error() }
func (e *urlLikeError) Unwrap() error { return e.err }

// 텔레그램이 알려 준 파일 경로에 못 쓰는 글자가 있으면, 주소를 만들다 난
// 오류가 주소 전체를 담고 그 주소에는 봇 토큰이 있습니다. 상대가 보내는
// 값으로 토큰을 뽑아낼 수 있는 모양이었습니다.
func TestFilePathCannotLeakTheToken(t *testing.T) {
	hostile := []struct{ name, path string }{
		{"제어 문자", "photos/a" + string(rune(0x7f)) + "b.jpg"},
		{"줄바꿈", "photos/a\nb.jpg"},
		{"빈칸", "photos/a b.jpg"},
		{"물음표로 질의 붙이기", "photos/a.jpg?x=1"},
		{"우물 정으로 자르기", "photos/a.jpg#x"},
		{"상위로 올라가기", "../../etc/passwd"},
		{"절대 경로", "/etc/passwd"},
		{"다른 호스트로 돌리기", "@evil.example.com/"},
	}

	for _, tc := range hostile {
		body := `{"result":{"file_path":` + quote(tc.path) + `,"file_size":10}}`
		doer := &scriptedDoer{getFileBody: body}
		client, err := New(doer, Options{Token: probeToken})
		if err != nil {
			t.Fatal(err)
		}

		_, err = client.DownloadFile(context.Background(), "abc")
		if err == nil {
			t.Errorf("%s: 통과했습니다. 막아야 합니다", tc.name)
			continue
		}
		if strings.Contains(err.Error(), probeToken) {
			t.Errorf("%s: 오류에 토큰이 그대로 있습니다: %s", tc.name, err)
		}
	}
}

// 멀쩡한 경로는 그대로 지나가야 합니다. 다 막아 버리면 봇이 못 씁니다.
func TestNormalFilePathPasses(t *testing.T) {
	for _, path := range []string{
		"photos/file_123.jpg",
		"documents/file_4.pdf",
		"video_notes/file-9.mp4",
		"photos/AgACAgQAAxkBAAIBZ2.jpg",
	} {
		if err := checkFilePath(path); err != nil {
			t.Errorf("멀쩡한 경로 %q를 막았습니다: %v", path, err)
		}
	}
}

// 전송 계층 오류에도 주소가 들어 있습니다. 그 길도 가려야 합니다.
func TestTransportErrorDoesNotLeak(t *testing.T) {
	doer := &scriptedDoer{
		getFileBody: `{"result":{"file_path":"photos/ok.jpg","file_size":10}}`,
		transport:   errors.New("connection reset by peer"),
	}
	client, err := New(doer, Options{Token: probeToken})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.DownloadFile(context.Background(), "abc")
	if err == nil {
		t.Fatal("오류가 나야 합니다")
	}
	if strings.Contains(err.Error(), probeToken) {
		t.Errorf("전송 오류에 토큰이 그대로 있습니다: %s", err)
	}
	if !strings.Contains(err.Error(), "<토큰 가림>") {
		t.Errorf("가린 표시가 없습니다. 주소가 안 들어왔는지 보십시오: %s", err)
	}

	// 감싼 오류를 풀어도 새지 않아야 합니다. %w로 감싸면 여기서 새어 나옵니다.
	for wrapped := err; wrapped != nil; wrapped = errors.Unwrap(wrapped) {
		if strings.Contains(wrapped.Error(), probeToken) {
			t.Errorf("풀어 보니 토큰이 있습니다: %s", wrapped)
		}
	}
}

// quote는 JSON 문자열로 감쌉니다. 제어 문자를 그대로 넣을 수 없습니다.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			if r < 0x20 || r == 0x7f {
				const hex = "0123456789abcdef"
				b.WriteString(`\u00`)
				b.WriteByte(hex[(r>>4)&0xf])
				b.WriteByte(hex[r&0xf])
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
