package release

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewer(t *testing.T) {
	if !Newer("v0.2.0", "v0.2.1") || !Newer("d9cc964", "v0.2.0") || !Newer("dev", "v0.2.0") {
		t.Fatal("더 새 릴리스를 못 알아봤습니다")
	}
	if Newer("v0.2.1", "v0.2.0") || Newer("v0.2.0", "v0.2.0") || Newer("v0.2.0", "abc") {
		t.Fatal("같거나 오래된 릴리스를 새것으로 봤습니다")
	}
}

func TestAssetName(t *testing.T) {
	if AssetName("linux", "amd64") != "saucedust-linux-amd64" {
		t.Fatal(AssetName("linux", "amd64"))
	}
	if AssetName("windows", "amd64") != "saucedust-windows-amd64.exe" {
		t.Fatal(AssetName("windows", "amd64"))
	}
}

func TestCheckAndInstall(t *testing.T) {
	elf := append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 64)...)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/yldst-dev/sauceDUST/releases/latest":
			_, _ = w.Write([]byte(`{"tag_name":"v0.2.1","body":"고침","assets":[{"name":"saucedust-linux-amd64","browser_download_url":"` + srv.URL + `/saucedust-linux-amd64"}]}`))
		case "/saucedust-linux-amd64":
			_, _ = w.Write(elf)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	prev := releaseAPI
	releaseAPI = srv.URL
	t.Cleanup(func() { releaseAPI = prev })

	status, err := Check(context.Background(), srv.Client(), "yldst-dev/sauceDUST", "v0.2.0", "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Available || status.Latest != "v0.2.1" || status.AssetURL == "" {
		t.Fatalf("%+v", status)
	}
	dest := filepath.Join(t.TempDir(), "saucedust")
	if err := os.WriteFile(dest, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveDownload(context.Background(), srv.Client(), status.AssetURL, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 4 || got[0] != 0x7f {
		t.Fatalf("바꾼 파일이 이상합니다: %d", len(got))
	}
	if err := Install(context.Background(), srv.Client(), "https://example.com/saucedust", dest); err == nil {
		t.Fatal("다른 주소의 파일을 받았습니다")
	}
}
