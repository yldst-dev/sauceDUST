// Package httpapi는 중앙 서버의 HTTP 계층입니다.
// 작업 노드가 결과를 보내고, 사람이 검색하고, 대시보드를 보는 통로입니다.
package httpapi

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"saucedust/internal/app"
	"saucedust/internal/domain"
)

//go:embed web/*
var webFS embed.FS

const maxUploadBytes = 32 << 20

// maxIngestItems는 한 요청에 받을 이미지 수입니다.
//
// 담을 장수 검사는 들어온 수를 더해서 보지만, 한 번에 엄청난 양이 오면
// 그 한 묶음만으로 상한을 크게 넘깁니다. 수집 쪽이 보내는 묶음은 수백
// 건 규모이므로 넉넉하게 잡아도 충분합니다.
const maxIngestItems = 2048

// maxRequestBytes는 검색 요청 본문 전체의 상한입니다.
// 업로드 한 장에 32MB를 주고 멀티파트 껍데기 몫을 조금 더합니다.
const maxRequestBytes = maxUploadBytes + (1 << 20)

type StatsSource interface {
	Ping(ctx context.Context) error
	CountImages(ctx context.Context) (int64, error)
	ListNodes(ctx context.Context, timeout time.Duration) ([]domain.Node, error)
	FleetSummary(ctx context.Context, window time.Duration) (domain.FleetSummary, error)
	CrawlSummary(ctx context.Context, site, scope string) (domain.CrawlSummary, error)
}

type Config struct {
	Bind        string
	Token       string
	NodeTimeout time.Duration
	SourceSite  string
	ScopeKey    string
}

type Deps struct {
	Ingest *app.Ingest
	Search *app.Search
	Stats  StatsSource
	Images app.ImageRepository
	// Index와 Embedder는 준비 상태 확인에만 씁니다. 없으면 그 항목을 건너뜁니다.
	Index    app.VectorIndex
	Embedder app.Embedder
	Log      *slog.Logger
}

type Server struct {
	cfg  Config
	deps Deps
	http *http.Server
}

// minTokenLen은 밖으로 열 때 요구하는 토큰 길이입니다.
// 여러 사람이 나눠 쓰는 값이라 짧으면 그대로 뚫립니다.
const minTokenLen = 16

func New(cfg Config, deps Deps) (*Server, error) {
	if cfg.Bind == "" {
		return nil, errors.New("서버 주소가 비어 있습니다")
	}
	if err := checkExposure(cfg.Bind, cfg.Token); err != nil {
		return nil, err
	}
	if cfg.NodeTimeout <= 0 {
		cfg.NodeTimeout = 90 * time.Second
	}
	if deps.Stats == nil {
		return nil, errors.New("통계 조회기가 없습니다")
	}
	if deps.Images == nil {
		return nil, errors.New("이미지 조회기가 없습니다")
	}
	if deps.Log == nil {
		deps.Log = slog.Default()
	}

	s := &Server{cfg: cfg, deps: deps}
	s.http = &http.Server{
		Addr:              cfg.Bind,
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	return s, nil
}

func (s *Server) Addr() string { return s.cfg.Bind }

// Handler는 구성된 라우터를 돌려줍니다. 시험에서 실제 포트를 열지 않고
// 경로와 인증을 확인할 때 씁니다.
func (s *Server) Handler() http.Handler { return s.http.Handler }

func (s *Server) Run(ctx context.Context) error {
	errc := make(chan error, 1)
	go func() {
		s.deps.Log.Info("중앙 서버를 시작합니다", slog.String("bind", s.cfg.Bind))
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
			return
		}
		errc <- nil
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// 준비 상태는 감시 도구가 인증 없이 볼 수 있어야 합니다.
	// 여기서 새어 나가는 것은 각 부품이 살아 있는지 여부뿐입니다.
	mux.HandleFunc("GET /ready", s.handleReady)

	mux.Handle("POST /v1/ingest", s.authed(s.handleIngest))
	mux.Handle("POST /v1/search", s.authed(s.handleSearch))
	mux.Handle("GET /v1/nodes", s.authed(s.handleNodes))
	mux.Handle("GET /v1/stats", s.authed(s.handleStats))
	mux.Handle("GET /v1/images/{id}", s.authed(s.handleImageByID))
	mux.Handle("GET /v1/images/source/{site}/{postID}", s.authed(s.handleImageBySource))

	dashboard, err := newDashboardHandler()
	if err != nil {
		s.deps.Log.Warn("대시보드를 준비하지 못했습니다", slog.String("error", err.Error()))
	} else {
		mux.Handle("GET /", dashboard)
	}
	return logging(s.deps.Log, mux)
}

// checkExposure는 밖에서 닿는 주소에 토큰 없이 뜨는 것을 막습니다.
//
// authed가 토큰이 빈 동안은 아무것도 검사하지 않기 때문에, 이 확인이 없으면
// 바인드 주소만 바꾸고 토큰을 빠뜨렸을 때 /v1/ingest가 그대로 열립니다.
// 아무나 자료를 밀어 넣을 수 있고, 넣은 쪽을 나중에 가려낼 방법도 없습니다.
//
// 되돌아오는 주소에서는 그냥 두어 혼자 쓸 때 번거롭지 않게 합니다.
func checkExposure(bind, token string) error {
	if bindIsLoopback(bind) {
		return nil
	}
	if token == "" {
		return fmt.Errorf("%s로 열려면 SAUCEDUST_CONTROL_TOKEN이 있어야 합니다."+
			" 토큰이 없으면 닿을 수 있는 누구나 자료를 넣고 검색할 수 있습니다", bind)
	}
	if len(token) < minTokenLen {
		return fmt.Errorf("SAUCEDUST_CONTROL_TOKEN이 %d자입니다. 밖으로 열 때는 %d자 이상이어야 합니다",
			len(token), minTokenLen)
	}
	return nil
}

// bindIsLoopback은 그 주소가 이 컴퓨터 안에서만 닿는지 봅니다.
//
// 판단이 서지 않으면 아니라고 답합니다. 틀렸을 때 한쪽은 시작을 막을 뿐이고
// 다른 쪽은 인증 없는 서버를 여는 것이라, 안전한 쪽으로 기웁니다.
func bindIsLoopback(bind string) bool {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return false
	}
	switch host {
	case "":
		// ":8000"은 모든 주소에 붙는다는 뜻입니다.
		return false
	case "localhost":
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// authed는 토큰이 설정되어 있을 때만 검사합니다.
// 혼자 쓰는 단일 노드 환경에서 토큰 없이 시작할 수 있게 하기 위해서입니다.
// 밖으로 열 때 토큰이 있는지는 checkExposure가 시작 전에 확인합니다.
func (s *Server) authed(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" {
			supplied := r.Header.Get("authorization")
			expected := "Bearer " + s.cfg.Token
			if subtle.ConstantTimeCompare([]byte(supplied), []byte(expected)) != 1 {
				writeError(w, http.StatusUnauthorized, errors.New("인증 토큰이 필요합니다"))
				return
			}
		}
		next(w, r)
	})
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ingest == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("이 노드는 결과를 받지 않습니다"))
		return
	}

	var req IngestRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("요청을 해석하지 못했습니다: %w", err))
		return
	}
	if len(req.Items) == 0 {
		writeJSON(w, http.StatusOK, IngestResponse{})
		return
	}
	if len(req.Items) > maxIngestItems {
		writeError(w, http.StatusRequestEntityTooLarge,
			fmt.Errorf("한 번에 %d건이 왔습니다. %d건까지만 받습니다",
				len(req.Items), maxIngestItems))
		return
	}

	batch := make([]domain.IndexedImage, 0, len(req.Items))
	for _, item := range req.Items {
		decoded, err := item.Decode()
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		batch = append(batch, decoded)
	}

	if err := s.deps.Ingest.Submit(r.Context(), batch); err != nil {
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, domain.ErrModelMismatch):
			status = http.StatusConflict
		case errors.Is(err, domain.ErrIndexFull):
			// 다시 보내도 소용없습니다. 보낸 쪽이 재시도 큐에 넣지 않고
			// 멈추도록 뜻이 분명한 코드를 돌려줍니다.
			status = http.StatusInsufficientStorage
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, IngestResponse{Accepted: len(batch)})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	if s.deps.Search == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("검색을 쓸 수 없습니다"))
		return
	}

	// 본문 전체에 상한을 겁니다. ParseMultipartForm은 메모리 몫을 넘는
	// 만큼을 디스크로 흘리므로, 상한이 없으면 요청 하나로 디스크를
	// 채울 수 있습니다.
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)

	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("업로드를 읽지 못했습니다: %w", err))
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("file 항목이 필요합니다"))
		return
	}
	defer file.Close()

	image, err := io.ReadAll(io.LimitReader(file, maxUploadBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	result, err := s.deps.Search.ByImage(r.Context(), image)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	resp := SearchResponse{ExactMatch: result.ExactMatch}
	for _, hit := range result.Hits {
		resp.Hits = append(resp.Hits, SearchHitView{
			SourceSite:   hit.Image.SourceSite,
			SourcePostID: hit.Image.SourcePostID,
			CanonicalURL: hit.Image.CanonicalURL,
			SourceURL:    hit.Image.SourceURL,
			PreviewURL:   hit.Image.PreviewURL,
			Rating:       hit.Image.Rating,
			Tags:         hit.Image.Tags,
			ArtistTags:   hit.Image.ArtistTags,
			Score:        hit.Score,
			HashDistance: hit.HashDistance,
			Exact:        hit.Exact,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleReady는 각 부품이 실제로 응답하는지 확인합니다.
// 하나라도 죽어 있으면 503을 내어 배치 도구가 알아채게 합니다.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	view := ReadyView{Checks: map[string]string{}}

	// 서로 독립이므로 함께 확인합니다.
	type probe struct {
		name string
		fn   func(context.Context) error
	}
	probes := []probe{{"postgres", s.deps.Stats.Ping}}
	if s.deps.Index != nil {
		probes = append(probes, probe{"qdrant", s.deps.Index.Ping})
	}
	if s.deps.Embedder != nil {
		probes = append(probes, probe{"embedder", s.deps.Embedder.Health})
	}

	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	for _, p := range probes {
		wg.Add(1)
		go func(p probe) {
			defer wg.Done()
			status := "ok"
			if err := p.fn(ctx); err != nil {
				// 인증 없이 볼 수 있는 곳이라 원문을 그대로 내보내면
				// 안 됩니다. 접속 문자열에 내부 호스트와 계정 이름이
				// 들어 있습니다. 살았는지 여부만 알립니다.
				status = "죽음"
				s.deps.Log.Warn("준비 상태 확인 실패",
					slog.String("part", p.name), slog.String("error", err.Error()))
			}
			mu.Lock()
			defer mu.Unlock()
			view.Checks[p.name] = status
		}(p)
	}
	wg.Wait()

	view.Ready = true
	for _, status := range view.Checks {
		if status != "ok" {
			view.Ready = false
		}
	}

	code := http.StatusOK
	if !view.Ready {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, view)
}

func (s *Server) handleImageByID(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("이미지 번호가 숫자가 아닙니다"))
		return
	}

	img, err := s.deps.Images.ImageByID(r.Context(), id)
	s.writeImage(w, img, err)
}

func (s *Server) handleImageBySource(w http.ResponseWriter, r *http.Request) {
	postID, err := strconv.ParseInt(r.PathValue("postID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("게시물 번호가 숫자가 아닙니다"))
		return
	}

	img, err := s.deps.Images.ImageBySource(r.Context(), r.PathValue("site"), postID)
	s.writeImage(w, img, err)
}

// writeImage는 조회 결과 하나를 응답합니다.
// 찾지 못한 것은 오류가 아니라 404입니다.
func (s *Server) writeImage(w http.ResponseWriter, img *domain.Image, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeJSON(w, http.StatusNotFound, ImageView{Indexed: false})
	case err != nil:
		writeError(w, http.StatusInternalServerError, err)
	default:
		writeJSON(w, http.StatusOK, newImageView(*img))
	}
}

func (s *Server) handleNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.deps.Stats.ListNodes(r.Context(), s.cfg.NodeTimeout)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	summary, err := s.deps.Stats.FleetSummary(r.Context(), 5*time.Minute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	out := make([]NodeView, 0, len(nodes))
	for _, n := range nodes {
		view := NodeView{
			ID: n.ID, Role: string(n.Role), Status: n.Status,
			Hostname: n.Hostname, Platform: n.Platform,
			Device: string(n.Device), NetMode: string(n.NetMode),
			Concurrency: n.Concurrency, Version: n.Version,
			LastSeenSec: time.Since(n.HeartbeatAt).Seconds(),
		}
		if m, ok := summary.Nodes[n.ID]; ok {
			view.Downloaded = m.Downloaded
			view.Embedded = m.Embedded
			view.Saved = m.Saved
			view.Failed = m.Failed
			view.PerSecond = m.PerSecond
			view.CPUPct = m.CPUPct
			view.MemMB = m.MemMB
		}
		out = append(out, view)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	images, err := s.deps.Stats.CountImages(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	nodes, err := s.deps.Stats.ListNodes(ctx, s.cfg.NodeTimeout)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	fleet, err := s.deps.Stats.FleetSummary(ctx, 5*time.Minute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	crawl, err := s.deps.Stats.CrawlSummary(ctx, s.cfg.SourceSite, s.cfg.ScopeKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	var online int
	for _, n := range nodes {
		if n.Status == "online" {
			online++
		}
	}

	writeJSON(w, http.StatusOK, StatsView{
		Images:         images,
		Vectors:        crawl.VectorsByModel,
		OnlineNodes:    online,
		TotalNodes:     len(nodes),
		RangesDone:     crawl.RangesCompleted,
		RangesActive:   crawl.RangesRunning,
		RangesFailed:   crawl.RangesFailed,
		RetryQueue:     crawl.RetryPending,
		SavedPerSec:    fleet.TotalPerSecond,
		HighWatermark:  crawl.HighWatermark,
		BackfillBefore: crawl.BackfillBefore,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	// 이미 상태 줄을 보냈으므로 여기서 실패해도 알릴 방법이 없습니다.
	// 연결이 끊긴 경우이며, 서버가 할 일은 없습니다.
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, ErrorResponse{Error: err.Error()})
}

func logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Debug("요청 처리",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Duration("took", time.Since(start)))
	})
}
