package saucedust

import (
	"encoding/json"
	"strings"
	"testing"
)

// 노드에는 실행 파일 하나만 올립니다. 워커를 돌리는 데 필요한 것이 빠지면
// 배포한 뒤에야 알게 되므로 여기서 막습니다.
func TestWorkerFilesHasEverythingTheNodeNeeds(t *testing.T) {
	files, err := WorkerFiles()
	if err != nil {
		t.Fatal(err)
	}

	required := []string{
		"main.py",
		"api.py",
		"service.py",
		"backends.py",
		"config.py",
		"device.py",
		"ports.py",
		"models.json",
		"requirements.txt",
		"domain/__init__.py",
		"domain/imaging.py",
		"domain/hashing.py",
		"domain/spec.py",
	}
	for _, name := range required {
		if len(files[name]) == 0 {
			t.Errorf("%s가 빠졌거나 비어 있습니다", name)
		}
	}
}

// 노드에서 쓰지 않는 것은 넣지 않습니다.
func TestWorkerFilesExcludesTestOnlyFiles(t *testing.T) {
	files, err := WorkerFiles()
	if err != nil {
		t.Fatal(err)
	}

	for name := range files {
		switch {
		case name == "conftest.py":
			t.Errorf("pytest 전용 파일이 들어 있습니다: %s", name)
		case strings.HasPrefix(name, "tests/"):
			t.Errorf("시험 파일이 들어 있습니다: %s", name)
		case strings.Contains(name, "__pycache__"):
			t.Errorf("캐시가 들어 있습니다: %s", name)
		case strings.Contains(name, ".venv"):
			t.Errorf("가상 환경이 들어 있습니다: %s", name)
		}
	}
}

// 넣어 둔 models.json이 실측으로 정한 모델이어야 합니다.
// 이 값이 어긋나면 노드가 다른 모델을 올려 벡터를 비교할 수 없게 됩니다.
func TestEmbeddedModelsMatchTheDecision(t *testing.T) {
	files, err := WorkerFiles()
	if err != nil {
		t.Fatal(err)
	}

	var specs []struct {
		ID         string `json:"id"`
		Kind       string `json:"kind"`
		Checkpoint string `json:"checkpoint"`
		VectorSize int    `json:"vector_size"`
	}
	if err := json.Unmarshal(files["models.json"], &specs); err != nil {
		t.Fatalf("models.json을 읽지 못했습니다: %v", err)
	}

	byKind := map[string]int{}
	for _, s := range specs {
		byKind[s.Kind]++
		if s.VectorSize <= 0 {
			t.Errorf("%s의 차원이 %d입니다", s.ID, s.VectorSize)
		}
		if s.Kind == "copy" && s.ID != "siglip-b16" {
			t.Errorf("copy 모델이 %s입니다. 실측으로 siglip-b16을 골랐습니다", s.ID)
		}
	}
	if byKind["copy"] != 1 {
		t.Errorf("copy 모델이 %d개입니다. 하나여야 합니다", byKind["copy"])
	}
}

// 판을 고정하지 않으면 노드마다 다른 것이 깔립니다.
// transformers 5.14가 semantic 모델을 통째로 깨뜨린 적이 있습니다.
func TestEmbeddedRequirementsArePinned(t *testing.T) {
	files, err := WorkerFiles()
	if err != nil {
		t.Fatal(err)
	}

	var loose []string
	for _, line := range strings.Split(string(files["requirements.txt"]), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "==") {
			loose = append(loose, line)
		}
	}
	if len(loose) > 0 {
		t.Errorf("판이 고정되지 않은 의존성이 있습니다: %v", loose)
	}

	// 실제로 검증한 판이 들어 있는지 봅니다.
	for _, want := range []string{"torch==", "transformers==", "pillow==", "numpy=="} {
		if !strings.Contains(string(files["requirements.txt"]), want) {
			t.Errorf("%s 항목이 없습니다", want)
		}
	}
}

// 워커 소스가 통째로 빠진 채 빌드되는 일이 없어야 합니다.
func TestWorkerFilesIsNotEmpty(t *testing.T) {
	files, err := WorkerFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 10 {
		t.Fatalf("파일이 %d개뿐입니다. embed가 제대로 걸리지 않은 것 같습니다", len(files))
	}

	var total int
	for _, data := range files {
		total += len(data)
	}
	if total < 20_000 {
		t.Errorf("전부 합쳐 %d바이트입니다. 너무 작습니다", total)
	}
	t.Logf("파일 %d개, 전부 합쳐 %.1fKB", len(files), float64(total)/1024)
}
