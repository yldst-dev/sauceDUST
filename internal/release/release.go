package release

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const DefaultRepo = "yldst-dev/sauceDUST"

const maxBinary = 64 << 20

type Status struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Notes     string `json:"notes"`
	Error     string `json:"error,omitempty"`
	AssetURL  string `json:"-"`
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Body    string `json:"body"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func AssetName(goos, goarch string) string {
	name := "saucedust-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func Newer(current, latest string) bool {
	latest = strings.TrimSpace(latest)
	current = strings.TrimSpace(current)
	if latest == "" || latest == current {
		return false
	}
	lv, lok := parseVersion(latest)
	cv, cok := parseVersion(current)
	if lok && cok {
		for i := 0; i < len(lv) || i < len(cv); i++ {
			var l, c int
			if i < len(lv) {
				l = lv[i]
			}
			if i < len(cv) {
				c = cv[i]
			}
			if l != c {
				return l > c
			}
		}
		return false
	}
	return lok
}

func parseVersion(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nil, false
		}
		out = append(out, n)
	}
	return out, true
}

func Check(ctx context.Context, client *http.Client, repo, current, goos, goarch string) (Status, error) {
	if client == nil {
		client = http.DefaultClient
	}
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	if repo == "" {
		repo = DefaultRepo
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseAPI+"/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return Status{}, err
	}
	req.Header.Set("accept", "application/vnd.github+json")
	req.Header.Set("user-agent", "saucedust")
	res, err := client.Do(req)
	if err != nil {
		return Status{}, errors.New("최신 릴리스를 확인하지 못했습니다")
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return Status{}, errors.New("최신 릴리스를 읽지 못했습니다")
	}
	if res.StatusCode == http.StatusNotFound {
		return Status{Current: current}, errors.New("올라온 릴리스가 없습니다")
	}
	if res.StatusCode != http.StatusOK {
		return Status{Current: current}, fmt.Errorf("릴리스 확인이 %d로 끝났습니다", res.StatusCode)
	}
	var rel ghRelease
	if err := json.Unmarshal(body, &rel); err != nil {
		return Status{}, errors.New("릴리스 정보를 해석하지 못했습니다")
	}
	status := Status{
		Current:   current,
		Latest:    rel.TagName,
		Available: Newer(current, rel.TagName),
		Notes:     clip(rel.Body, 400),
	}
	want := AssetName(goos, goarch)
	for _, asset := range rel.Assets {
		if asset.Name == want {
			status.AssetURL = asset.URL
			break
		}
	}
	if status.Available && status.AssetURL == "" {
		status.Available = false
		status.Error = "이 컴퓨터용 파일 " + want + " 이 릴리스에 없습니다"
	}
	return status, nil
}

var releaseAPI = "https://api.github.com"

func Install(ctx context.Context, client *http.Client, assetURL, dest string) error {
	if client == nil {
		client = http.DefaultClient
	}
	if !strings.HasPrefix(assetURL, "https://github.com/") && !strings.HasPrefix(assetURL, "https://release-assets.github.com/") && !strings.HasPrefix(assetURL, "https://objects.githubusercontent.com/") {
		return errors.New("릴리스 파일 주소가 아닙니다")
	}
	return saveDownload(ctx, client, assetURL, dest)
}

func saveDownload(ctx context.Context, client *http.Client, assetURL, dest string) error {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("user-agent", "saucedust")
	req.Header.Set("accept", "application/octet-stream")
	res, err := client.Do(req)
	if err != nil {
		return errors.New("새 실행 파일을 받지 못했습니다")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("실행 파일 받기가 %d로 끝났습니다", res.StatusCode)
	}
	next := dest + ".new"
	f, err := os.OpenFile(next, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return fmt.Errorf("새 실행 파일을 쓰지 못했습니다: %w", err)
	}
	n, copyErr := io.Copy(f, io.LimitReader(res.Body, maxBinary+1))
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(next)
		if copyErr != nil {
			return errors.New("새 실행 파일을 저장하지 못했습니다")
		}
		return closeErr
	}
	if n < 4 || n > maxBinary {
		_ = os.Remove(next)
		return errors.New("받은 실행 파일 크기가 이상합니다")
	}
	head := make([]byte, 4)
	rf, err := os.Open(next)
	if err != nil {
		_ = os.Remove(next)
		return err
	}
	_, _ = io.ReadFull(rf, head)
	_ = rf.Close()
	if !looksExecutable(head) {
		_ = os.Remove(next)
		return errors.New("받은 파일이 실행 파일이 아닙니다")
	}
	if err := os.Chmod(next, 0o755); err != nil {
		_ = os.Remove(next)
		return err
	}
	if err := os.Rename(next, dest); err != nil {
		_ = os.Remove(next)
		return fmt.Errorf("실행 파일을 바꾸지 못했습니다: %w", err)
	}
	return nil
}

func InstallSelf(ctx context.Context, client *http.Client, assetURL string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	return Install(ctx, client, assetURL, exe)
}

func looksExecutable(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	if b[0] == 0x7f && b[1] == 'E' && b[2] == 'L' && b[3] == 'F' {
		return true
	}
	if b[0] == 0xCF && b[1] == 0xFA && b[2] == 0xED && b[3] == 0xFE {
		return true
	}
	if b[0] == 0xFE && b[1] == 0xED && b[2] == 0xFA && (b[3] == 0xCE || b[3] == 0xCF) {
		return true
	}
	if b[0] == 'M' && b[1] == 'Z' {
		return true
	}
	return false
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "..."
}
