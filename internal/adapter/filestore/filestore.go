// Package filestore는 재계산용 축소본을 디스크에 보관합니다.
package filestore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// dirPerm은 축소본 디렉터리 권한입니다. 이 노드에서만 읽으면 되므로
// 다른 사용자에게는 열지 않습니다.
const dirPerm = 0o750

// ThumbStore는 재계산용 384픽셀 축소본을 디스크에 보관합니다.
// 원본은 저장하지 않습니다. 모델을 바꿔도 다시 내려받지 않기 위한 최소 사본입니다.
type ThumbStore struct {
	root string
	// 두 단계 분산이라 같은 디렉터리가 계속 재사용됩니다.
	// 파일마다 MkdirAll을 부르면 그만큼 시스템 호출이 낭비됩니다.
	known sync.Map
}

func NewThumbStore(root string) (*ThumbStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("축소본 저장 경로가 비어 있습니다")
	}
	if err := os.MkdirAll(root, dirPerm); err != nil {
		return nil, fmt.Errorf("축소본 디렉터리를 만들지 못했습니다: %w", err)
	}
	return &ThumbStore{root: root}, nil
}

// Put은 축소본을 저장하고 루트 기준 상대 경로를 돌려줍니다.
//
// 경로를 DB가 매긴 id가 아니라 출처(사이트, 게시물 번호)로 정합니다.
// 그래야 저장 전에 경로를 알 수 있어 이미지 행을 한 번만 쓰면 됩니다.
// DB를 다시 만들어도 경로가 그대로라는 이점도 있습니다.
func (s *ThumbStore) Put(ctx context.Context, site string, postID int64, jpeg []byte) (string, error) {
	if len(jpeg) == 0 {
		return "", errors.New("빈 축소본은 저장하지 않습니다")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	rel := RelPath(site, postID)
	// site는 설정에서 옵니다. 오타 하나로 저장 루트 바깥에 쓰게 두지 않습니다.
	// 읽기 쪽과 같은 잣대를 씁니다.
	full, err := s.resolve(rel)
	if err != nil {
		return "", err
	}

	if err := s.ensureDir(filepath.Dir(full)); err != nil {
		return "", err
	}

	// 같은 이미지를 두 노드가 동시에 쓸 수 있으므로 임시 파일에 쓰고 바꿔치기합니다.
	tmp, err := os.CreateTemp(filepath.Dir(full), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("임시 파일을 만들지 못했습니다: %w", err)
	}
	tmpName := tmp.Name()

	// 아래 정리 실패는 알릴 방법도 고칠 방법도 없습니다.
	// 원래 오류를 그대로 올리는 편이 진단에 낫습니다.
	if _, err := tmp.Write(jpeg); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("축소본을 쓰지 못했습니다: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", err
	}
	if err := os.Rename(tmpName, full); err != nil {
		_ = os.Remove(tmpName)
		return "", fmt.Errorf("축소본을 옮기지 못했습니다: %w", err)
	}
	return rel, nil
}

func (s *ThumbStore) Get(ctx context.Context, rel string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	full, err := s.resolve(rel)
	if err != nil {
		return nil, err
	}
	// resolve가 저장 루트를 벗어나는 경로를 이미 걸렀습니다.
	data, err := os.ReadFile(full) // #nosec G304 -- resolve로 경로 범위를 확인했습니다
	if err != nil {
		return nil, fmt.Errorf("축소본을 읽지 못했습니다: %w", err)
	}
	return data, nil
}

func (s *ThumbStore) Has(ctx context.Context, rel string) bool {
	full, err := s.resolve(rel)
	if err != nil {
		return false
	}
	info, err := os.Stat(full)
	return err == nil && info.Size() > 0
}

func (s *ThumbStore) Root() string { return s.root }

func (s *ThumbStore) ensureDir(dir string) error {
	if _, ok := s.known.Load(dir); ok {
		return nil
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("축소본 디렉터리를 만들지 못했습니다: %w", err)
	}
	s.known.Store(dir, struct{}{})
	return nil
}

// resolve는 저장 루트를 벗어나는 경로를 막습니다.
func (s *ThumbStore) resolve(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("축소본 경로가 비어 있습니다")
	}
	full := filepath.Join(s.root, filepath.Clean("/"+rel))
	if !strings.HasPrefix(full, filepath.Clean(s.root)+string(os.PathSeparator)) {
		return "", fmt.Errorf("저장 루트를 벗어나는 경로입니다: %s", rel)
	}
	return full, nil
}

// RelPath는 한 디렉터리에 파일이 몰리지 않도록 두 단계로 나눕니다.
// 게시물 번호의 하위 비트를 쓰므로 번호가 이어져도 고르게 퍼집니다.
func RelPath(site string, postID int64) string {
	// 하위 두 바이트로 나눕니다. 먼저 마스크를 씌우므로 값이 항상 0~255이고
	// 게시물 번호가 음수여도 경로가 뒤집히지 않습니다.
	low := postID & 0xff
	high := (postID >> 8) & 0xff

	return filepath.Join(
		safeSegment(site),
		fmt.Sprintf("%02x", low),
		fmt.Sprintf("%02x", high),
		fmt.Sprintf("%d.jpg", postID),
	)
}

// safeSegment는 사이트 이름을 폴더 이름 한 칸으로 만듭니다.
//
// 여기서 나온 값이 DB의 thumb_path에 그대로 들어갑니다. 설정에 빗금이나
// 상위 폴더 표시가 섞이면 저장할 때는 걸러지더라도 DB에는 루트를 벗어나는
// 경로가 남습니다. 나중에 그 값을 그대로 이어 붙이는 코드가 생기면
// 그때 문제가 됩니다. 만들 때 한 칸으로 못 박아 둡니다.
func safeSegment(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		default:
			b.WriteByte('-')
		}
	}
	if out := strings.Trim(b.String(), "-"); out != "" {
		return out
	}
	return "unknown"
}
