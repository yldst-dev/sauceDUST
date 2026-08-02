package main

import (
	"path/filepath"
	"runtime"
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

// postgresHint는 psql이 없을 때 무엇을 하라고 알려 줄지입니다.
func postgresHint(goos string) string {
	switch goos {
	case "windows":
		return "PostgreSQL 클라이언트가 필요합니다. winget install PostgreSQL.PostgreSQL"
	case "darwin":
		return "PostgreSQL 클라이언트가 필요합니다. brew install postgresql@16"
	default:
		return "PostgreSQL 클라이언트가 필요합니다. apt install postgresql-client"
	}
}

// pythonHint는 Python이 없을 때 알려 줄 말입니다.
func pythonHint(goos string) string {
	switch goos {
	case "windows":
		return "Python 3.11 이상을 찾지 못했습니다. winget install Python.Python.3.12"
	case "darwin":
		return "Python 3.11 이상을 찾지 못했습니다. brew install python@3.12"
	default:
		return "Python 3.11 이상을 찾지 못했습니다. apt install python3.12-venv"
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
