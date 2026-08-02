package config

import _ "embed"

// Example은 설정 본보기입니다.
//
// 파일을 읽지 않고 실행 파일 안에 넣어 둡니다. 노드에는 바이너리 하나만
// 올라가므로, 저장소 경로를 찾아 읽게 두면 배포한 노드에서 setup이 실패합니다.
//
//go:embed env.example
var Example string
