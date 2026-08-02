package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"saucedust/internal/domain"
)

// 토큰이 비어 있으면 authed는 아무것도 검사하지 않습니다.
// 그 상태로 밖에 열리면 /v1/ingest가 무방비가 되므로 시작을 막아야 합니다.
func TestExposedBindNeedsToken(t *testing.T) {
	exposed := []string{
		"0.0.0.0:8000",
		":8000",
		"192.168.1.10:8000",
		"100.64.1.5:8000",
		"[::]:8000",
		"10.0.0.4:8000",
	}

	for _, bind := range exposed {
		if err := checkExposure(bind, ""); err == nil {
			t.Errorf("%s가 토큰 없이 통과했습니다", bind)
		}
	}
}

// 혼자 쓸 때까지 토큰을 요구하면 번거롭기만 합니다.
func TestLoopbackBindNeedsNoToken(t *testing.T) {
	loopback := []string{
		"127.0.0.1:8000",
		"localhost:8000",
		"[::1]:8000",
		"127.0.0.53:8000",
	}

	for _, bind := range loopback {
		if err := checkExposure(bind, ""); err != nil {
			t.Errorf("%s를 막았습니다: %v", bind, err)
		}
	}
}

// 여러 사람이 나눠 쓰는 값이라 짧으면 그대로 뚫립니다.
func TestExposedBindNeedsLongToken(t *testing.T) {
	if err := checkExposure("0.0.0.0:8000", "1234"); err == nil {
		t.Error("네 자 토큰이 통과했습니다")
	}

	long := strings.Repeat("a", minTokenLen)
	if err := checkExposure("0.0.0.0:8000", long); err != nil {
		t.Errorf("%d자 토큰을 막았습니다: %v", minTokenLen, err)
	}

	// 안에서만 쓸 때는 길이를 따지지 않습니다.
	if err := checkExposure("127.0.0.1:8000", "1234"); err != nil {
		t.Errorf("되돌아오는 주소에서 짧은 토큰을 막았습니다: %v", err)
	}
}

// 알아볼 수 없는 주소는 열려 있다고 봅니다.
// 틀렸을 때 한쪽은 시작을 막을 뿐이고 다른 쪽은 인증 없는 서버를 엽니다.
func TestUnknownBindIsTreatedAsExposed(t *testing.T) {
	unknown := []string{
		"saucedust.내부:8000",
		"포트없음",
		"",
	}

	for _, bind := range unknown {
		if bindIsLoopback(bind) {
			t.Errorf("%q를 되돌아오는 주소로 봤습니다", bind)
		}
	}
}

// 막을 때는 무엇을 어떻게 해야 하는지 말해 줘야 합니다.
func TestExposureErrorNamesTheSetting(t *testing.T) {
	err := checkExposure("0.0.0.0:8000", "")
	if err == nil {
		t.Fatal("막지 않았습니다")
	}
	if !strings.Contains(err.Error(), "SAUCEDUST_CONTROL_TOKEN") {
		t.Errorf("고칠 설정 이름이 없습니다: %v", err)
	}
	if !strings.Contains(err.Error(), "0.0.0.0:8000") {
		t.Errorf("문제가 된 주소가 없습니다: %v", err)
	}
}

// New가 실제로 이 확인을 거치는지 봅니다.
// checkExposure만 맞고 연결이 빠져 있으면 아무것도 막지 못합니다.
func TestNewRefusesExposedBindWithoutToken(t *testing.T) {
	_, err := New(Config{Bind: "0.0.0.0:8000"}, Deps{
		Stats:  stubStats{},
		Images: stubImages{},
	})
	if err == nil {
		t.Fatal("토큰 없이 0.0.0.0에 뜨는 것을 허용했습니다")
	}
	if !strings.Contains(err.Error(), "SAUCEDUST_CONTROL_TOKEN") {
		t.Errorf("막은 이유가 다릅니다: %v", err)
	}
}

// 색인이 차면 다시 보내도 소용없습니다. 500번대로 내면 보낸 쪽이
// 끝없이 다시 보냅니다. 507은 재시도하지 않는 쪽으로 갈라집니다.
func TestFullIndexIsNotRetryable(t *testing.T) {
	retry, err := classifyIngestStatus(http.StatusInsufficientStorage, "찼습니다")
	if retry {
		t.Error("507을 재시도 대상으로 봤습니다")
	}
	if !errors.Is(err, domain.ErrIndexFull) {
		t.Errorf("찼다는 것을 알아보지 못했습니다: %v", err)
	}

	// 다른 500번대는 그대로 재시도해야 합니다. 잠깐 흔들린 것일 수 있습니다.
	if retry, _ := classifyIngestStatus(http.StatusBadGateway, "502"); !retry {
		t.Error("502를 재시도하지 않습니다")
	}
	if retry, _ := classifyIngestStatus(http.StatusInternalServerError, "500"); !retry {
		t.Error("500을 재시도하지 않습니다")
	}

	// 모델 불일치와 인증 실패는 원래대로 멈춰야 합니다.
	if retry, _ := classifyIngestStatus(http.StatusConflict, "다름"); retry {
		t.Error("409를 재시도합니다")
	}
	if retry, _ := classifyIngestStatus(http.StatusUnauthorized, "토큰"); retry {
		t.Error("401을 재시도합니다")
	}
}
