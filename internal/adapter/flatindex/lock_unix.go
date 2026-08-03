//go:build unix

package flatindex

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockName은 폴더를 누가 쥐고 있는지 표시하는 파일입니다.
const lockName = ".lock"

// dirLock은 한 폴더를 여러 프로세스가 함께 쓰다 망가지는 것을 막습니다.
//
// Qdrant는 서버라 이 문제가 없었습니다. 파일을 직접 다루면 다릅니다.
// 두 프로세스가 각자 제 자리 수를 세고 그 자리에 붙이면 서로를 덮습니다.
// control이 도는 중에 reembed를 돌리면 실제로 그렇게 됩니다.
//
// 읽기만 하는 쪽은 함께 쥘 수 있습니다. doctor가 도는 동안 control이
// 멈추면 곤란합니다.
type dirLock struct {
	f *os.File
}

func lockDir(dir string, readOnly bool) (*dirLock, error) {
	path := filepath.Join(dir, lockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- 설정에서 온 색인 폴더 안 고정 이름입니다
	if err != nil {
		return nil, fmt.Errorf("잠금 파일을 열지 못했습니다: %w", err)
	}

	how := syscall.LOCK_EX | syscall.LOCK_NB
	if readOnly {
		how = syscall.LOCK_SH | syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf(
			"색인 폴더 %s를 다른 프로세스가 쓰고 있습니다. 그쪽을 먼저 멈추십시오: %w", dir, err)
	}
	return &dirLock{f: f}, nil
}

func (l *dirLock) close() error {
	if l == nil || l.f == nil {
		return nil
	}
	// 파일을 닫으면 잠금도 풀립니다.
	err := l.f.Close()
	l.f = nil
	return err
}
