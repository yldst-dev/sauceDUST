//go:build !unix

package flatindex

import (
	"fmt"
	"os"
)

// readAtReader는 걸어 두지 못하는 곳에서 쓰는 대체 경로입니다.
//
// 윈도우의 파일 매핑은 표준 라이브러리로 바로 되지 않습니다. 의존성을
// 늘리지 않으려고 여기서는 읽어 옵니다. 운영체제 페이지 캐시는 그대로
// 쓰므로 램 요건은 같고, 옮겨 담는 만큼만 느립니다.
//
// 중앙 노드는 리눅스를 전제로 합니다. 이 경로는 윈도우에서도 빌드되고
// 돌아가게 하기 위한 것입니다.
type readAtReader struct {
	f    *os.File
	size int
}

func openCodes(f *os.File, size int) (codeReader, error) {
	return &readAtReader{f: f, size: size}, nil
}

func (r *readAtReader) chunk(from, to int, buf []byte) ([]byte, error) {
	if from < 0 || to > r.size || from > to {
		return nil, fmt.Errorf("코드 파일 범위를 벗어났습니다: %d~%d, 크기 %d", from, to, r.size)
	}
	n := to - from
	if len(buf) < n {
		return nil, fmt.Errorf("버퍼가 %d바이트인데 %d바이트가 필요합니다", len(buf), n)
	}
	if _, err := r.f.ReadAt(buf[:n], int64(from)); err != nil {
		return nil, fmt.Errorf("코드 파일을 읽지 못했습니다: %w", err)
	}
	return buf[:n], nil
}

func (r *readAtReader) zeroCopy() bool { return false }

func (r *readAtReader) close() error { return nil }
