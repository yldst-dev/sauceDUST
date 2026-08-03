package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// 운영체제마다 다른 것들을 한곳에 모읍니다.
//
// 흩어 두면 한 군데를 고치고 다른 데를 빠뜨립니다. 실제로 가상 환경 경로가
// 유닉스 기준으로만 적혀 있어 Windows에서는 setup이 반드시 실패했습니다.
// 빌드는 멀쩡히 되기 때문에 돌려 보기 전에는 알 수 없었습니다.
//
// goos를 인자로 받는 형태로 둔 이유는 시험 때문입니다. macOS에서
// Windows 경로가 맞는지 확인하려면 이렇게 해야 합니다.

// venvBinDir는 가상 환경 안에서 실행 파일이 놓이는 폴더 이름입니다.
func venvBinDir(goos string) string {
	if goos == "windows" {
		return "Scripts"
	}
	return "bin"
}

// venvExe는 가상 환경 안 실행 파일의 전체 경로입니다.
func venvExe(goos, venv, name string) string {
	if goos == "windows" {
		name += ".exe"
	}
	return filepath.Join(venv, venvBinDir(goos), name)
}

// pythonCandidates는 Python을 찾을 이름 목록입니다.
//
// 유닉스는 판이 이름에 붙습니다. Windows는 python.org 설치본이 python으로
// 잡히고, py는 여러 판을 골라 주는 실행기입니다.
//
// 아주 최신 판은 torch 휠이 아직 없을 수 있어 검증된 순서로 봅니다.
func pythonCandidates(goos string) []string {
	if goos == "windows" {
		return []string{"python3.12", "python3.11", "python", "py"}
	}
	return []string{"python3.12", "python3.11", "python3.13", "python3"}
}

// linuxFamily는 배포판 갈래를 냅니다.
//
// 리눅스를 전부 Debian으로 보고 apt를 알려 주면, Rocky나 openSUSE에서는
// 그대로 따라 해도 아무 일이 안 일어납니다. 없는 명령을 알려 주는 것은
// 안 알려 주는 것보다 나쁩니다. 어디로 가야 할지 모르게 만듭니다.
//
// ID를 먼저 보고 없으면 ID_LIKE를 봅니다. Rocky는 ID가 rocky이고
// ID_LIKE가 "rhel centos fedora"입니다.
func linuxFamily(osRelease string) string {
	fields := map[string]string{}
	for _, line := range strings.Split(osRelease, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		fields[key] = strings.Trim(value, `"'`)
	}

	known := map[string]string{
		"rocky": "rhel", "almalinux": "rhel", "centos": "rhel",
		"rhel": "rhel", "fedora": "rhel",
		"debian": "debian", "ubuntu": "debian",
		"opensuse": "suse", "sles": "suse",
		"arch": "arch",
	}
	if family, ok := known[fields["ID"]]; ok {
		return family
	}
	for _, like := range strings.Fields(fields["ID_LIKE"]) {
		if family, ok := known[like]; ok {
			return family
		}
	}
	return ""
}

// currentLinuxFamily는 이 컴퓨터의 갈래를 봅니다. 리눅스가 아니면 빕니다.
func currentLinuxFamily() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	raw, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	return linuxFamily(string(raw))
}

// installHint는 갈래에 맞는 설치 명령을 냅니다.
// 모르는 갈래면 명령 대신 무엇이 필요한지만 알려 줍니다.
func installHint(family string, pkgs map[string]string) string {
	if cmd, ok := pkgs[family]; ok {
		return cmd
	}
	return pkgs[""]
}

// postgresHint는 psql이 없을 때 무엇을 하라고 알려 줄지입니다.
func postgresHint(goos, family string) string {
	const need = "PostgreSQL 클라이언트가 필요합니다. "
	switch goos {
	case "windows":
		return need + "winget install PostgreSQL.PostgreSQL"
	case "darwin":
		return need + "brew install postgresql@16"
	default:
		return need + installHint(family, map[string]string{
			"rhel":   "dnf install postgresql",
			"debian": "apt install postgresql-client",
			"suse":   "zypper install postgresql",
			"arch":   "pacman -S postgresql-libs",
			"":       "쓰는 배포판의 postgresql 클라이언트 꾸러미를 깔아 주십시오",
		})
	}
}

// pythonHint는 Python이 없을 때 알려 줄 말입니다.
func pythonHint(goos, family string) string {
	const need = "Python 3.11 이상을 찾지 못했습니다. "
	switch goos {
	case "windows":
		return need + "winget install Python.Python.3.12"
	case "darwin":
		return need + "brew install python@3.12"
	default:
		return need + installHint(family, map[string]string{
			// RHEL 갈래는 venv가 본체에 들어 있어 따로 깔 것이 없습니다.
			"rhel":   "dnf install python3.12 python3.12-pip",
			"debian": "apt install python3.12-venv",
			"suse":   "zypper install python312 python312-pip",
			"arch":   "pacman -S python",
			"":       "쓰는 배포판의 python 3.12와 venv 꾸러미를 깔아 주십시오",
		})
	}
}

// workerStartCommand는 워커를 띄우는 명령을 그 운영체제 표기로 냅니다.
func workerStartCommand(goos, workerDir string) string {
	uvicorn := venvExe(goos, ".venv", "uvicorn")
	if goos == "windows" {
		return "cd " + workerDir + " && " + uvicorn + " main:app --host 127.0.0.1 --port 8100"
	}
	return "cd " + workerDir + " && ./" + uvicorn + " main:app --host 127.0.0.1 --port 8100"
}

// 아래는 실제로 도는 운영체제를 그대로 넘기는 짧은 껍데기입니다.

func venvPython(venv string) string { return venvExe(runtime.GOOS, venv, "python") }
func venvPip(venv string) string    { return venvExe(runtime.GOOS, venv, "pip") }
