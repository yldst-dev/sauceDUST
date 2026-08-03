package domain

import "strings"

// IndexKind는 검색용 벡터를 어디에 두는지입니다.
//
// 둘의 차이는 속도가 아니라 메모리가 요구 조건인지 캐시인지입니다.
//
// Qdrant는 장수에 비례하는 몫을 반드시 램에 붙들고 있어야 합니다.
// 모자라면 느려지는 것이 아니라 죽고, 다시 띄워도 컬렉션을 여는 것
// 자체가 한도를 넘어 또 죽습니다. 되살릴 방법이 지우는 것뿐입니다.
//
// 납작한 색인은 파일을 주소 공간에 걸어 두기만 합니다. 모자라면 커널이
// 페이지를 버리고, 다음에 다시 읽느라 느려질 뿐입니다.
type IndexKind string

const (
	// IndexFlat은 이진 코드를 파일에 두고 통째로 훑습니다.
	IndexFlat IndexKind = "flat"
	// IndexQdrant는 Qdrant 서버에 맡깁니다.
	IndexQdrant IndexKind = "qdrant"
)

// ParseIndexKind는 설정 값을 읽습니다. 모르는 값은 받지 않습니다.
// 조용히 기본값으로 넘어가면 옮겼다고 믿은 색인이 그대로 남습니다.
func ParseIndexKind(raw string) (IndexKind, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(IndexFlat):
		return IndexFlat, true
	case string(IndexQdrant):
		return IndexQdrant, true
	default:
		return "", false
	}
}
