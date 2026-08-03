package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// Windows에서는 가상 환경 안 실행 파일이 Scripts 아래에 있고 확장자가 붙습니다.
// 유닉스 기준으로만 적어 두면 빌드는 멀쩡히 되고 돌릴 때만 실패합니다.
// 실제로 그 상태였고, Windows에서 setup이 반드시 실패했습니다.
func TestVenvExeByPlatform(t *testing.T) {
	tests := []struct {
		goos, name, want string
	}{
		{"windows", "python", filepath.Join(".venv", "Scripts", "python.exe")},
		{"windows", "pip", filepath.Join(".venv", "Scripts", "pip.exe")},
		{"windows", "uvicorn", filepath.Join(".venv", "Scripts", "uvicorn.exe")},
		{"darwin", "python", filepath.Join(".venv", "bin", "python")},
		{"linux", "python", filepath.Join(".venv", "bin", "python")},
		{"linux", "pip", filepath.Join(".venv", "bin", "pip")},
	}

	for _, tc := range tests {
		got := venvExe(tc.goos, ".venv", tc.name)
		if got != tc.want {
			t.Errorf("%s의 %s가 %q입니다. %q를 기대했습니다",
				tc.goos, tc.name, got, tc.want)
		}
	}
}

func TestVenvBinDir(t *testing.T) {
	if got := venvBinDir("windows"); got != "Scripts" {
		t.Errorf("windows가 %q입니다", got)
	}
	for _, goos := range []string{"darwin", "linux", "freebsd"} {
		if got := venvBinDir(goos); got != "bin" {
			t.Errorf("%s가 %q입니다", goos, got)
		}
	}
}

// Windows에는 python3.12 같은 이름이 보통 없습니다.
// 유닉스 이름만 찾으면 Python이 깔려 있어도 못 찾습니다.
func TestPythonCandidates(t *testing.T) {
	win := pythonCandidates("windows")
	if !contains(win, "python") {
		t.Errorf("windows 후보에 python이 없습니다: %v", win)
	}
	if !contains(win, "py") {
		t.Errorf("windows 후보에 py 실행기가 없습니다: %v", win)
	}

	unix := pythonCandidates("linux")
	if !contains(unix, "python3") {
		t.Errorf("유닉스 후보에 python3이 없습니다: %v", unix)
	}

	// 어느 쪽이든 판이 붙은 이름을 먼저 봐야 합니다.
	// 최신 판은 torch 휠이 아직 없을 수 있습니다.
	for _, list := range [][]string{win, unix} {
		if !strings.HasPrefix(list[0], "python3.1") {
			t.Errorf("첫 후보가 %q입니다. 판이 정해진 이름을 먼저 봐야 합니다", list[0])
		}
	}
}

// 안내는 그 운영체제에서 실제로 먹히는 명령이어야 합니다.
// 리눅스 사용자에게 brew를 알려 주면 아무 도움이 안 됩니다.
func TestHintsMatchThePlatform(t *testing.T) {
	tests := []struct {
		goos, family, want string
	}{
		{"windows", "", "winget"},
		{"darwin", "", "brew"},
		{"linux", "debian", "apt"},
		{"linux", "rhel", "dnf"},
		{"linux", "suse", "zypper"},
		{"linux", "arch", "pacman"},
	}

	for _, tc := range tests {
		if got := postgresHint(tc.goos, tc.family); !strings.Contains(got, tc.want) {
			t.Errorf("%s/%s의 psql 안내에 %q가 없습니다: %s", tc.goos, tc.family, tc.want, got)
		}
		if got := pythonHint(tc.goos, tc.family); !strings.Contains(got, tc.want) {
			t.Errorf("%s/%s의 Python 안내에 %q가 없습니다: %s", tc.goos, tc.family, tc.want, got)
		}
	}
}

// 모르는 배포판에는 없는 명령을 알려 주면 안 됩니다.
// 따라 해도 아무 일이 안 일어나서 어디로 가야 할지 모르게 만듭니다.
func TestUnknownDistroGetsNoCommand(t *testing.T) {
	for _, hint := range []string{postgresHint("linux", ""), pythonHint("linux", "")} {
		for _, cmd := range []string{"apt", "dnf", "zypper", "pacman"} {
			if strings.Contains(hint, cmd) {
				t.Errorf("모르는 배포판에 %q를 알려 줍니다: %s", cmd, hint)
			}
		}
	}
}

// Rocky는 ID가 rocky이고 ID_LIKE에 rhel이 들어 있습니다.
func TestLinuxFamilyFromOSRelease(t *testing.T) {
	tests := []struct {
		name, raw, want string
	}{
		{"rocky", "NAME=\"Rocky Linux\"\nID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n", "rhel"},
		{"almalinux", "ID=\"almalinux\"\nID_LIKE=\"rhel centos fedora\"\n", "rhel"},
		{"ubuntu", "ID=ubuntu\nID_LIKE=debian\n", "debian"},
		{"debian", "ID=debian\n", "debian"},
		{"opensuse", "ID=\"opensuse-leap\"\nID_LIKE=\"suse opensuse\"\n", "suse"},
		{"arch", "ID=arch\n", "arch"},
		{"모르는 것", "ID=plan9\n", ""},
		{"빈 파일", "", ""},
		{"주석과 빈 줄", "# 설명\n\nID=rocky\n", "rhel"},
	}

	for _, tc := range tests {
		if got := linuxFamily(tc.raw); got != tc.want {
			t.Errorf("%s에서 %q가 나왔습니다. %q여야 합니다", tc.name, got, tc.want)
		}
	}
}

// 띄우는 명령도 그 운영체제 표기여야 복사해 붙여 쓸 수 있습니다.
func TestWorkerStartCommand(t *testing.T) {
	win := workerStartCommand("windows", `C:\saucedust\worker`)
	if !strings.Contains(win, "Scripts") || !strings.Contains(win, ".exe") {
		t.Errorf("windows 명령이 %q입니다", win)
	}
	if strings.Contains(win, "./") {
		t.Errorf("windows 명령에 유닉스 표기가 있습니다: %q", win)
	}

	unix := workerStartCommand("linux", "/home/me/saucedust/worker")
	if !strings.Contains(unix, "bin/uvicorn") {
		t.Errorf("리눅스 명령이 %q입니다", unix)
	}
	if strings.Contains(unix, ".exe") {
		t.Errorf("리눅스 명령에 exe가 있습니다: %q", unix)
	}
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// Rocky 9의 python3는 3.9입니다. 이름만 보고 고르면 torch가 안 깔리는
// 가상 환경을 만들고, 몇 분 뒤 pip가 엉뚱한 말로 실패합니다.
func TestParsePythonVersion(t *testing.T) {
	tests := []struct {
		raw            string
		major, minor   int
		ok             bool
		enoughForTorch bool
	}{
		{"Python 3.12.13", 3, 12, true, true},
		{"Python 3.11.13", 3, 11, true, true},
		{"Python 3.9.25", 3, 9, true, false},
		{"Python 3.10.0", 3, 10, true, false},
		{"Python 4.0.1", 4, 0, true, true},
		{"Python 3.13", 3, 13, true, true},
		{"", 0, 0, false, true},
		{"Python", 0, 0, false, true},
		{"Python 3", 0, 0, false, true},
		{"Python 세.열둘", 0, 0, false, true},
	}

	for _, tc := range tests {
		major, minor, ok := parsePythonVersion(tc.raw)
		if ok != tc.ok || (ok && (major != tc.major || minor != tc.minor)) {
			t.Errorf("%q에서 %d.%d(ok=%v)가 나왔습니다. %d.%d(ok=%v)여야 합니다",
				tc.raw, major, minor, ok, tc.major, tc.minor, tc.ok)
		}

		// 못 알아들으면 막지 않는 것까지 함께 봅니다. 판 표기가 바뀌었다고
		// 설치를 세우면 안 됩니다.
		got := !ok || major > minPythonMajor ||
			(major == minPythonMajor && minor >= minPythonMinor)
		if got != tc.enoughForTorch {
			t.Errorf("%q를 쓸 수 있다고 %v로 봤습니다. %v여야 합니다",
				tc.raw, got, tc.enoughForTorch)
		}
	}
}
