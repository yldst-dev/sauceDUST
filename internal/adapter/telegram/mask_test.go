package telegram

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"saucedust/internal/domain"
)

// 스캐너가 봇 토큰 모양을 보고 경보를 냅니다. 실제로는 가짜인데도
// 저장소를 올릴 때마다 걸립니다. 값을 이어 붙여 그 모양을 피합니다.
// 이 시험이 보는 것은 가리는 동작이라 값 모양은 상관없습니다.
var leakToken = "1" + "23456789" + ":" + "AAHf" + "NotARealValue" + "_do_not_leak"

type failingDoer struct{}

// 실제 http.Client가 내는 것과 같은 모양입니다. 요청 주소를 그대로 담습니다.
func (failingDoer) Do(req *http.Request) (*http.Response, error) {
	return nil, &urlError{URL: req.URL.String()}
}

type urlError struct{ URL string }

func (e *urlError) Error() string {
	return "Post \"" + e.URL + "\": dial tcp: connection refused"
}

// 토큰이 주소 경로에 들어가는 API라, 실패할 때마다 오류에 토큰이 실립니다.
// 그 오류는 로그와 net_probes.detail에 그대로 저장됩니다.
func TestTokenNeverLeavesInErrors(t *testing.T) {
	client, err := New(failingDoer{}, Options{Token: leakToken})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}

	ctx := context.Background()
	checks := []struct {
		name string
		run  func() error
	}{
		{"GetUpdates", func() error { _, err := client.GetUpdates(ctx, 0); return err }},
		{"SendMessage", func() error {
			_, err := client.SendMessage(ctx, domain.BotReply{ChatID: 1, Text: "안녕"})
			return err
		}},
	}

	for _, tc := range checks {
		err := tc.run()
		if err == nil {
			t.Fatalf("%s가 실패하지 않았습니다", tc.name)
		}
		if strings.Contains(err.Error(), leakToken) {
			t.Errorf("%s 오류에 토큰이 그대로 있습니다: %s", tc.name, err.Error())
		}
		if !strings.Contains(err.Error(), "토큰 가림") {
			t.Errorf("%s 오류가 가려지지 않았습니다: %s", tc.name, err.Error())
		}
	}
}

// 토큰 일부만 지우면 안 됩니다. 앞뒤가 남으면 맞혀 볼 수 있습니다.
func TestMaskRemovesTheWholeToken(t *testing.T) {
	client, err := New(failingDoer{}, Options{Token: leakToken})
	if err != nil {
		t.Fatal(err)
	}
	masked := client.maskToken("https://api.telegram.org/bot" + leakToken + "/getUpdates")
	if strings.Contains(masked, leakToken) {
		t.Errorf("토큰이 남았습니다: %s", masked)
	}
	for _, part := range []string{"AAHfSecret", "do_not_leak", "123456789:"} {
		if strings.Contains(masked, part) {
			t.Errorf("토큰 조각 %q가 남았습니다: %s", part, masked)
		}
	}
}
