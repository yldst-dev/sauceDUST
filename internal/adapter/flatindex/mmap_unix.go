//go:build unix

package flatindex

import (
	"fmt"
	"os"
	"syscall"
)

// mmapReader는 파일을 주소 공간에 걸어 두고 그 자리를 그대로 가리킵니다.
//
// 힙에 올리지 않는 것이 요점입니다. 읽어서 슬라이스에 담으면 그만큼이
// 반드시 램에 있어야 하는 몫이 되어, 모자랄 때 프로세스가 죽습니다.
// 걸어 두면 커널이 페이지 캐시로 들고 있다가 모자라면 알아서 버립니다.
// 그러면 죽는 대신 다시 읽느라 느려질 뿐입니다.
type mmapReader struct {
	data []byte
}

func openCodes(f *os.File, size int) (codeReader, error) {
	if size == 0 {
		return &mmapReader{}, nil
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("코드 파일을 걸지 못했습니다: %w", err)
	}
	return &mmapReader{data: data}, nil
}

// chunk는 걸어 둔 자리를 그대로 냅니다. buf는 쓰지 않습니다.
func (m *mmapReader) chunk(from, to int, _ []byte) ([]byte, error) {
	if from < 0 || to > len(m.data) || from > to {
		return nil, fmt.Errorf("코드 파일 범위를 벗어났습니다: %d~%d, 크기 %d", from, to, len(m.data))
	}
	return m.data[from:to], nil
}

// zeroCopy는 훑을 때 buf를 마련할 필요가 없다는 뜻입니다.
func (m *mmapReader) zeroCopy() bool { return true }

func (m *mmapReader) close() error {
	if len(m.data) == 0 {
		return nil
	}
	err := syscall.Munmap(m.data)
	m.data = nil
	return err
}
