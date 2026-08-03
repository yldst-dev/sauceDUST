//go:build !unix

package flatindex

// dirLock은 걸지 못하는 곳에서 아무것도 하지 않습니다.
//
// flock이 표준 라이브러리에 없어서 의존성을 늘리지 않으려면 여기서는
// 막을 방법이 없습니다. 중앙 노드는 리눅스를 전제로 하고, 이 경로는
// 윈도우에서도 빌드되고 돌아가게 하기 위한 것입니다.
//
// 윈도우에서 색인을 쓴다면 control과 reembed를 같이 돌리지 마십시오.
type dirLock struct{}

func lockDir(string, bool) (*dirLock, error) { return &dirLock{}, nil }

func (l *dirLock) close() error { return nil }
