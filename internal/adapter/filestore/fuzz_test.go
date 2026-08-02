package filestore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 축소본 경로는 설정에서 온 사이트 이름과 상대가 준 게시물 번호로 만들어집니다.
// 어떤 값이 와도 저장 루트를 벗어나면 안 되고, 그 경로가 DB에 그대로 적히므로
// 나중에 이어 붙여도 벗어나면 안 됩니다.
func FuzzRelPathStaysInsideRoot(f *testing.F) {
	f.Add("danbooru", int64(1))
	f.Add("../escaped", int64(1))
	f.Add("../../etc/passwd", int64(-1))
	f.Add("", int64(0))
	f.Add("a/b/c", int64(9000001))
	f.Add(".", int64(1))
	f.Add("..", int64(1))
	f.Add("한글사이트", int64(42))

	root := "/srv/thumbs"

	f.Fuzz(func(t *testing.T, site string, postID int64) {
		rel := RelPath(site, postID)

		if filepath.IsAbs(rel) {
			t.Fatalf("절대 경로가 나왔습니다: %q", rel)
		}
		joined := filepath.Join(root, rel)
		if !strings.HasPrefix(joined, root+string(filepath.Separator)) {
			t.Fatalf("사이트 %q가 루트 밖 %q를 가리킵니다", site, joined)
		}
		// 정리해도 그대로여야 합니다. 상위 폴더 표시가 남아 있으면
		// 이어 붙이는 쪽에 따라 결과가 달라집니다.
		if filepath.Clean(rel) != rel {
			t.Fatalf("정리되지 않은 경로입니다: %q", rel)
		}
		if strings.Contains(rel, "..") {
			t.Fatalf("상위 폴더 표시가 남았습니다: %q", rel)
		}
		// 조각 수가 항상 같아야 두 단계 분산이 유지됩니다.
		if parts := strings.Split(rel, string(filepath.Separator)); len(parts) != 4 {
			t.Fatalf("경로 조각이 %d개입니다: %q", len(parts), rel)
		}
	})
}

// 읽기 경로는 DB에서 옵니다. DB가 오염됐다고 가정해도 루트 밖을 읽으면 안 됩니다.
func FuzzGetNeverEscapesRoot(f *testing.F) {
	f.Add("danbooru/00/00/1.jpg")
	f.Add("../../../etc/passwd")
	f.Add("/etc/passwd")
	f.Add("")
	f.Add("./../x")
	f.Add("a/../../b")

	f.Fuzz(func(t *testing.T, rel string) {
		root := t.TempDir()
		store, err := NewThumbStore(root)
		if err != nil {
			t.Fatal(err)
		}

		// 루트 밖에 미끼를 둡니다. 이것을 읽어 오면 실패입니다.
		bait := filepath.Join(filepath.Dir(root), "bait.txt")
		if err := os.WriteFile(bait, []byte("루트 밖입니다"), 0o600); err != nil {
			t.Skip("미끼를 만들지 못했습니다")
		}
		defer func() { _ = os.Remove(bait) }()

		data, err := store.Get(context.Background(), rel)
		if err != nil {
			return
		}
		if string(data) == "루트 밖입니다" {
			t.Fatalf("%q로 루트 밖 파일을 읽었습니다", rel)
		}
	})
}
