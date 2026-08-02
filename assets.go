// Package saucedust는 실행 파일에 넣어 나르는 자원을 담습니다.
//
// 이 패키지가 저장소 뿌리에 있는 이유는 go:embed가 자기 폴더 아래만 볼 수
// 있기 때문입니다. cmd/saucedust에서는 python/worker에 닿지 못합니다.
package saucedust

import (
	"embed"
	"io/fs"
	"strings"
)

// workerFS는 임베딩 워커의 소스입니다.
//
// 노드에 실행 파일 하나만 올리면 Python 쪽까지 갖춰지도록 함께 넣습니다.
// 128킬로바이트라 실행 파일 크기에 사실상 영향이 없습니다.
//
// 넣지 않는 것이 있습니다. Python 자체와 torch, 모델 가중치는 합쳐서
// 4기가바이트가 넘고 CPU 종류마다 다릅니다. 그것들은 setup이 받아 옵니다.
//
// 시험 파일과 가상 환경은 빠집니다. 점이나 밑줄로 시작하는 이름은 embed가
// 기본으로 걸러 주므로 .venv와 __pycache__는 저절로 빠집니다.
//
//go:embed python/worker/*.py python/worker/*.json
//go:embed python/worker/requirements.txt python/worker/domain/*.py
var workerFS embed.FS

// skipInBundle은 노드에서 쓰지 않는 파일입니다.
// pytest가 읽는 파일이라 배포본에 두면 혼란만 줍니다.
var skipInBundle = map[string]bool{
	"conftest.py": true,
}

// WorkerFiles는 노드에 풀어 놓을 파일을 경로와 내용으로 돌려줍니다.
// 경로는 워커 폴더 기준 상대 경로입니다.
func WorkerFiles() (map[string][]byte, error) {
	const root = "python/worker"
	out := map[string][]byte{}

	err := fs.WalkDir(workerFS, root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// embed.FS는 늘 빗금 경로를 씁니다. 윈도우에서도 같습니다.
		rel := strings.TrimPrefix(path, root+"/")
		if skipInBundle[rel] {
			return nil
		}
		data, err := workerFS.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = data
		return nil
	})
	return out, err
}
