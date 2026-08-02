package filestore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newStore(t *testing.T) *ThumbStore {
	t.Helper()
	store, err := NewThumbStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestNewThumbStoreRejectsEmptyRoot(t *testing.T) {
	for _, root := range []string{"", "   "} {
		if _, err := NewThumbStore(root); err == nil {
			t.Errorf("%q를 받아들였습니다", root)
		}
	}
}

func TestPutGetRoundTrip(t *testing.T) {
	store := newStore(t)
	want := []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3}

	rel, err := store.Put(context.Background(), "danbooru", 12345, want)
	if err != nil {
		t.Fatalf("저장하지 못했습니다: %v", err)
	}

	got, err := store.Get(context.Background(), rel)
	if err != nil {
		t.Fatalf("읽지 못했습니다: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("%v입니다. %v를 기대했습니다", got, want)
	}
	if !store.Has(context.Background(), rel) {
		t.Error("있는 파일을 없다고 합니다")
	}
}

func TestPutReturnsPathThatMatchesRelPath(t *testing.T) {
	// 경로를 미리 알 수 있어야 이미지 행을 한 번만 씁니다.
	// Put이 다른 경로를 돌려주면 DB에 적힌 경로가 어긋납니다.
	store := newStore(t)

	rel, err := store.Put(context.Background(), "danbooru", 777, []byte{1})
	if err != nil {
		t.Fatal(err)
	}
	if want := RelPath("danbooru", 777); rel != want {
		t.Errorf("%q를 돌려줬습니다. %q를 기대했습니다", rel, want)
	}
}

func TestPutRejectsEmpty(t *testing.T) {
	store := newStore(t)
	if _, err := store.Put(context.Background(), "danbooru", 1, nil); err == nil {
		t.Error("빈 축소본을 저장했습니다")
	}
}

func TestPutHonorsCanceledContext(t *testing.T) {
	store := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.Put(ctx, "danbooru", 1, []byte{1}); err == nil {
		t.Error("취소된 뒤에도 저장했습니다")
	}
}

// site는 설정에서 옵니다. 오타 하나로 저장 루트 바깥에 쓰면 안 됩니다.
func TestPutStaysInsideRoot(t *testing.T) {
	root := t.TempDir()
	store, err := NewThumbStore(filepath.Join(root, "thumbs"))
	if err != nil {
		t.Fatal(err)
	}

	for _, site := range []string{"../escaped", "../../etc", "..", "/absolute", "a/b"} {
		rel, err := store.Put(context.Background(), site, 1, []byte{1})
		if err != nil {
			continue
		}
		// DB에 적히는 것은 이 rel입니다. 이 값을 그대로 이어 붙여도
		// 루트를 벗어나면 안 됩니다.
		full := filepath.Join(store.Root(), rel)
		if !strings.HasPrefix(filepath.Clean(full), filepath.Clean(store.Root())+string(os.PathSeparator)) {
			t.Errorf("%q가 루트 밖 %q를 가리킵니다", site, full)
		}
		if _, err := os.Stat(full); err != nil {
			t.Errorf("%q로 저장한 파일이 %q에 없습니다: %v", site, full, err)
		}
	}

	// 루트 바깥에 아무것도 생기지 않았는지 직접 확인합니다.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "thumbs" {
			t.Errorf("루트 밖에 %q가 생겼습니다", e.Name())
		}
	}
}

func TestGetRejectsEscapingPath(t *testing.T) {
	store := newStore(t)

	for _, rel := range []string{"", "../../etc/passwd", "..", "danbooru/../../x"} {
		if _, err := store.Get(context.Background(), rel); err == nil {
			t.Errorf("%q를 읽었습니다", rel)
		}
		if store.Has(context.Background(), rel) {
			t.Errorf("%q가 있다고 합니다", rel)
		}
	}
}

func TestHasIsFalseForMissingOrEmpty(t *testing.T) {
	store := newStore(t)

	if store.Has(context.Background(), "danbooru/00/00/1.jpg") {
		t.Error("없는 파일이 있다고 합니다")
	}

	// 크기가 0인 파일은 쓰다 만 것입니다. 있다고 하면 다시 만들 기회를 놓칩니다.
	rel := RelPath("danbooru", 1)
	full := filepath.Join(store.Root(), rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if store.Has(context.Background(), rel) {
		t.Error("빈 파일이 있다고 합니다")
	}
}

func TestPutOverwritesAtomically(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	if _, err := store.Put(ctx, "danbooru", 5, []byte("first")); err != nil {
		t.Fatal(err)
	}
	rel, err := store.Put(ctx, "danbooru", 5, []byte("second"))
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, rel)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Errorf("%q입니다. 나중 값을 기대했습니다", got)
	}
}

// 같은 이미지를 두 노드가 동시에 쓸 수 있습니다.
// 임시 파일에 쓰고 바꿔치기하므로 반쯤 쓰인 파일이 보이면 안 됩니다.
func TestPutIsSafeUnderConcurrency(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	payload := strings.Repeat("x", 4096)

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Put(ctx, "danbooru", 99, []byte(payload)); err != nil {
				t.Errorf("동시 저장에 실패했습니다: %v", err)
			}
		}()
	}
	wg.Wait()

	got, err := store.Get(ctx, RelPath("danbooru", 99))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Errorf("%d바이트를 읽었습니다. %d바이트를 기대했습니다", len(got), len(payload))
	}

	// 임시 파일이 남아 있으면 디스크가 조용히 찹니다.
	dir := filepath.Dir(filepath.Join(store.Root(), RelPath("danbooru", 99)))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("임시 파일이 남았습니다: %s", e.Name())
		}
	}
}

func TestRelPathIsStable(t *testing.T) {
	// 경로가 바뀌면 이미 저장한 축소본을 전부 찾지 못하게 됩니다.
	tests := map[int64]string{
		1:       filepath.Join("danbooru", "01", "00", "1.jpg"),
		255:     filepath.Join("danbooru", "ff", "00", "255.jpg"),
		256:     filepath.Join("danbooru", "00", "01", "256.jpg"),
		9000001: filepath.Join("danbooru", "41", "54", "9000001.jpg"),
	}
	for postID, want := range tests {
		if got := RelPath("danbooru", postID); got != want {
			t.Errorf("%d가 %q입니다. %q를 기대했습니다", postID, got, want)
		}
	}
}

func TestRelPathHandlesNegativeID(t *testing.T) {
	// 음수가 들어올 일은 없지만, 마스크를 놓치면 경로에 음수 폴더가 생깁니다.
	rel := RelPath("danbooru", -1)
	for _, part := range strings.Split(rel, string(filepath.Separator))[1:3] {
		if len(part) != 2 || strings.Contains(part, "-") {
			t.Errorf("경로 조각이 %q입니다", part)
		}
	}
}

// 한 폴더에 파일이 몰리면 파일 시스템이 느려집니다.
// 번호가 이어져도 고르게 퍼지는지 봅니다.
func TestRelPathSpreadsSequentialIDs(t *testing.T) {
	dirs := map[string]int{}
	for id := int64(1); id <= 4096; id++ {
		dirs[filepath.Dir(RelPath("danbooru", id))]++
	}

	if len(dirs) < 256 {
		t.Errorf("폴더가 %d개뿐입니다. 256개 이상으로 퍼져야 합니다", len(dirs))
	}
	for dir, count := range dirs {
		if count > 32 {
			t.Errorf("%s에 %d개가 몰렸습니다", dir, count)
		}
	}
}

func TestDirectoryPermissions(t *testing.T) {
	store := newStore(t)
	rel, err := store.Put(context.Background(), "danbooru", 1, []byte{1})
	if err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Dir(filepath.Join(store.Root(), rel)))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o007 != 0 {
		t.Errorf("권한이 %o입니다. 다른 사용자에게 열려 있습니다", perm)
	}
}
