package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"saucedust"
	"saucedust/internal/config"
)

// cmdSetup은 새 노드를 쓸 수 있는 상태로 만듭니다.
//
// 노드를 하나 추가할 때마다 Python 설치, venv 생성, torch 내려받기, 모델
// 가중치 받기를 손으로 하면 다섯 대만 돼도 감당이 안 됩니다.
func cmdSetup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	skipPython := fs.Bool("skip-python", false, "Python 워커 준비를 건너뜁니다")
	skipModels := fs.Bool("skip-models", false, "모델 가중치 내려받기를 건너뜁니다")
	pythonBin := fs.String("python", "", "쓸 Python 실행 파일. 비우면 자동으로 찾습니다")
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	root = findRoot(root)

	fmt.Println("노드를 준비합니다.")
	fmt.Printf("작업 폴더: %s\n\n", root)

	if err := checkTools(); err != nil {
		return err
	}
	if err := ensureEnvFile(root); err != nil {
		return err
	}
	if *skipPython {
		fmt.Println("\nPython 워커 준비를 건너뜁니다.")
		return nil
	}

	workerDir, err := ensureWorkerDir(root)
	if err != nil {
		return err
	}

	python, err := findPython(*pythonBin)
	if err != nil {
		return err
	}
	fmt.Printf("\nPython: %s\n", python)

	venv := filepath.Join(workerDir, ".venv")
	if err := ensureVenv(ctx, python, venv); err != nil {
		return err
	}
	if err := installRequirements(ctx, venv, workerDir); err != nil {
		return err
	}
	if !*skipModels {
		if err := warmModels(ctx, venv, workerDir); err != nil {
			return err
		}
	}

	fmt.Println("\n준비를 마쳤습니다. 아래 순서로 띄우십시오.")
	fmt.Printf("  %s\n", workerStartCommand(runtime.GOOS, workerDir))
	fmt.Println("  saucedust worker")
	return nil
}

// requiredTools는 없으면 아무것도 못 하는 것들입니다.
var requiredTools = []struct {
	name string
	hint string
}{
	{"psql", postgresHint(runtime.GOOS, currentLinuxFamily())},
}

func checkTools() error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "도구\t결과\t비고")

	var missing []string
	for _, tool := range requiredTools {
		path, err := exec.LookPath(tool.name)
		if err != nil {
			fmt.Fprintf(w, "%s\t없음\t%s\n", tool.name, tool.hint)
			missing = append(missing, tool.name)
			continue
		}
		fmt.Fprintf(w, "%s\t있음\t%s\n", tool.name, path)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("필요한 도구가 없습니다: %s", strings.Join(missing, ", "))
	}
	return nil
}

// findPython은 torch가 지원하는 버전을 찾습니다.
// 아주 최신 버전은 torch 휠이 아직 없을 수 있어 검증된 순서로 봅니다.
func findPython(override string) (string, error) {
	if override != "" {
		path, err := exec.LookPath(override)
		if err != nil {
			return "", fmt.Errorf("지정한 Python을 찾지 못했습니다: %w", err)
		}
		return path, nil
	}

	for _, name := range pythonCandidates(runtime.GOOS) {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New(pythonHint(runtime.GOOS, currentLinuxFamily()))
}

func ensureVenv(ctx context.Context, python, venv string) error {
	if _, err := os.Stat(venvPython(venv)); err == nil {
		fmt.Println("가상 환경이 이미 있습니다.")
		return nil
	}
	fmt.Println("가상 환경을 만듭니다...")
	return runStep(ctx, "", python, "-m", "venv", venv)
}

func installRequirements(ctx context.Context, venv, workerDir string) error {
	pip := venvPip(venv)
	fmt.Println("의존성을 설치합니다. torch 때문에 몇 분 걸립니다...")
	return runStep(ctx, workerDir, pip, "install", "-q", "-r", "requirements.txt")
}

// warmModels는 모델 가중치를 미리 받아 둡니다.
// 처음 요청할 때 받으면 그 요청이 몇 분씩 걸립니다.
func warmModels(ctx context.Context, venv, workerDir string) error {
	fmt.Println("모델 가중치를 미리 받습니다. 처음 한 번만 오래 걸립니다...")

	script := `
import json, sys, pathlib
sys.path.insert(0, ".")
import config
from transformers import AutoImageProcessor, AutoModel

for spec in config.load().specs:
    if spec.backend != "transformers":
        print(f"  {spec.id}: {spec.backend} 백엔드는 건너뜁니다")
        continue
    print(f"  {spec.id} ({spec.checkpoint})")
    AutoImageProcessor.from_pretrained(spec.checkpoint)
    AutoModel.from_pretrained(spec.checkpoint)
print("모델을 모두 받았습니다.")
`
	return runStep(ctx, workerDir, venvPython(venv), "-c", script)
}

// workerLayouts는 임베딩 워커가 놓일 수 있는 자리입니다.
//
// 저장소에서는 python/worker이고, 노드에 배포하면 실행 파일 옆의 worker입니다.
// 한쪽만 보면 배포한 노드에서 setup이 실패합니다. 실제로 그랬습니다.
var workerLayouts = [][]string{
	{"python", "worker"},
	{"worker"},
}

// findWorkerDir는 두 구조 중 실제로 있는 쪽을 고릅니다.
func findWorkerDir(root string) (string, error) {
	var tried []string
	for _, parts := range workerLayouts {
		dir := filepath.Join(append([]string{root}, parts...)...)
		tried = append(tried, dir)
		// main.py가 있어야 워커 폴더입니다. 이름만 같은 빈 폴더를 고르지 않습니다.
		if _, err := os.Stat(filepath.Join(dir, "main.py")); err == nil {
			return dir, nil
		}
	}
	return "", fmt.Errorf("워커 폴더를 찾지 못했습니다. 다음을 찾아봤습니다:\n  %s",
		strings.Join(tried, "\n  "))
}

// ensureWorkerDir는 워커 소스를 준비합니다.
//
// 없으면 실행 파일 안에 넣어 둔 것을 풀어 씁니다. 노드에는 실행 파일 하나만
// 올리면 되고, 워커를 따로 복사할 필요가 없습니다. 판이 어긋날 일도 없습니다.
//
// 이미 있으면 건드리지 않습니다. 저장소에서 개발할 때 고친 것을 덮어쓰면
// 안 되기 때문입니다.
func ensureWorkerDir(root string) (string, error) {
	if dir, err := findWorkerDir(root); err == nil {
		return dir, nil
	}

	files, err := saucedust.WorkerFiles()
	if err != nil {
		return "", fmt.Errorf("실행 파일에 넣어 둔 워커를 읽지 못했습니다: %w", err)
	}
	if len(files) == 0 {
		return "", errors.New("실행 파일에 워커가 들어 있지 않습니다")
	}

	dir := filepath.Join(root, "worker")
	fmt.Printf("\n워커를 풀어 놓습니다: %s (%d개 파일)\n", dir, len(files))

	for rel, data := range files {
		// 경로는 실행 파일 안에 박혀 있어 바깥 입력이 닿지 않습니다.
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return "", err
		}
		if err := os.WriteFile(target, data, 0o600); err != nil { // #nosec G703 -- 작업 폴더 기준 고정 경로입니다
			return "", fmt.Errorf("%s를 쓰지 못했습니다: %w", rel, err)
		}
	}
	return dir, nil
}

func ensureEnvFile(root string) error {
	target := filepath.Join(root, ".env")
	if _, err := os.Stat(target); err == nil {
		fmt.Println("\n.env가 이미 있습니다.")
		return nil
	}

	// 경로는 작업 폴더에서 만들어집니다. 바깥 입력이 닿지 않습니다.
	// 자격 증명이 들어가는 파일이라 본인만 읽을 수 있게 둡니다.
	if err := os.WriteFile(target, []byte(config.Example), 0o600); err != nil { // #nosec G703 -- 작업 폴더 기준 고정 경로입니다
		return fmt.Errorf(".env를 만들지 못했습니다: %w", err)
	}

	fmt.Printf("\n.env를 만들었습니다: %s\n", target)
	fmt.Println("DATABASE_URL, QDRANT_URL, SAUCEDUST_NODE_ID를 채우십시오.")
	return nil
}

// runStep은 외부 명령을 돌리고 출력을 그대로 흘려보냅니다.
// 오래 걸리는 설치를 조용히 기다리게 하지 않기 위해서입니다.
// 실행 파일 경로는 exec.LookPath가 찾았거나 운영자가 -python으로 직접 준 것입니다.
// 이 명령은 운영자가 자기 컴퓨터에서 돌리는 설치 도구이며, 바깥 입력이 닿지 않습니다.
func runStep(ctx context.Context, dir, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- 운영자가 지정한 실행 파일입니다
	cmd.Dir = dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s 실행에 실패했습니다: %w", name, err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		fmt.Printf("  %s\n", scanner.Text())
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%s가 실패했습니다: %w", filepath.Base(name), err)
	}
	return nil
}

// cmdDoctor는 이 컴퓨터가 노드로 쓸 준비가 됐는지 봅니다.
// setup과 달리 아무것도 바꾸지 않습니다.
func cmdDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	root, err := os.Getwd()
	if err != nil {
		return err
	}
	root = findRoot(root)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "항목\t상태\t비고")
	fmt.Fprintf(w, "운영체제\t%s/%s\t코어 %d개\n",
		runtime.GOOS, runtime.GOARCH, runtime.NumCPU())

	var problems int
	note := func(name, status, detail string, bad bool) {
		if bad {
			problems++
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, status, detail)
	}

	if path, err := findPython(""); err != nil {
		note("Python", "없음", err.Error(), true)
	} else {
		note("Python", "있음", pythonVersion(ctx, path), false)
	}

	// setup과 같은 자리를 봐야 합니다. 한쪽만 보면 배포한 노드에서
	// 멀쩡히 깔린 것을 없다고 합니다.
	if workerDir, err := findWorkerDir(root); err != nil {
		note("워커 폴더", "없음", err.Error(), true)
	} else {
		venv := venvPython(filepath.Join(workerDir, ".venv"))
		if _, err := os.Stat(venv); err != nil {
			note("가상 환경", "없음", "saucedust setup으로 만드십시오", true)
		} else {
			note("가상 환경", "있음", torchInfo(ctx, venv), false)
		}
	}

	if _, err := os.Stat(filepath.Join(root, ".env")); err != nil {
		note(".env", "없음", "saucedust setup으로 만드십시오", true)
	} else {
		note(".env", "있음", "", false)
	}

	if err := w.Flush(); err != nil {
		return err
	}
	if problems > 0 {
		return fmt.Errorf("%d개 항목을 손봐야 합니다", problems)
	}
	fmt.Println("\n노드로 쓸 준비가 됐습니다. saucedust verify로 연결을 확인하십시오.")
	return nil
}

func pythonVersion(ctx context.Context, path string) string {
	return firstLine(ctx, path, "--version")
}

// torchInfo는 어떤 장치를 쓸 수 있는지 알려줍니다.
// 장치가 무엇인지에 따라 노드 처리량이 열 배 넘게 갈립니다.
func torchInfo(ctx context.Context, python string) string {
	script := `
try:
    import torch
    if torch.cuda.is_available():
        print(f"torch {torch.__version__}, CUDA {torch.cuda.get_device_name(0)}")
    elif torch.backends.mps.is_available():
        print(f"torch {torch.__version__}, MPS")
    else:
        print(f"torch {torch.__version__}, CPU만 가능")
except ImportError:
    print("torch가 설치되지 않았습니다")
`
	return firstLine(ctx, python, "-c", script)
}

func firstLine(ctx context.Context, name string, args ...string) string {
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 위와 같습니다. 운영자가 자기 컴퓨터에서 돌리는 점검 명령입니다.
	out, err := exec.CommandContext(runCtx, name, args...).CombinedOutput() // #nosec G204 -- 운영자가 지정한 실행 파일입니다
	if err != nil {
		return "확인하지 못했습니다"
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return line
}
