package app

import (
	"testing"

	"go.uber.org/goleak"
)

// 크롤러와 인덱서는 몇 주씩 도는 것이 목적입니다. 한 번 돌 때마다 고루틴이
// 하나씩 남으면 처음에는 아무 티도 안 나다가 어느 날 메모리를 다 씁니다.
// 시험이 끝난 시점에 살아 있는 고루틴이 있으면 여기서 걸립니다.
//
// 시험 자체가 만드는 고루틴은 무시합니다.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
		goleak.IgnoreAnyFunction("testing.(*T).Parallel"),
	)
}
