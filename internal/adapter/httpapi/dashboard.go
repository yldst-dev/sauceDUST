package httpapi

import (
	"io/fs"
	"net/http"
)

// newDashboardHandler는 바이너리에 내장한 화면을 그대로 내보냅니다.
// 외부 파일도, CDN도 필요 없으므로 노드에 바이너리 하나만 올리면 됩니다.
func newDashboardHandler() (http.Handler, error) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}
	files := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 대시보드는 항상 최신 상태를 보여야 하므로 캐시를 막습니다.
		w.Header().Set("cache-control", "no-store")
		files.ServeHTTP(w, r)
	}), nil
}
