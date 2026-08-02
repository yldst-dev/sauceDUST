package httpapi

import (
	"testing"

	"go.uber.org/goleak"
)

// 중앙 노드의 HTTP 서버는 계속 떠 있습니다. 요청마다 고루틴이 남으면
// 오래 돌수록 쌓입니다. 시험이 끝난 뒤에도 살아 있으면 여기서 걸립니다.
//
// 연결 재사용을 위해 대기하는 고루틴은 누수가 아닙니다. 유휴 연결이
// 스스로 정리되기 전에 시험이 끝나면 잡히므로 이름으로 빼 둡니다.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreAnyFunction("testing.(*T).Parallel"),
	)
}
