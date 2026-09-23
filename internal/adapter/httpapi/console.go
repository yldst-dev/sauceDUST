package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"saucedust/internal/domain"
)

type ConsoleAdmin interface {
	RangeStats(ctx context.Context, site, scope string) (domain.RangeStats, error)
	ExhaustedRanges(ctx context.Context, site, scope string, limit int) ([]domain.CrawlRange, error)
	ResetExhaustedRanges(ctx context.Context, site, scope string) (int64, error)
	ResetRange(ctx context.Context, id int64) error
	ResetRetryQueue(ctx context.Context, site, scope string) (int64, error)
	CoverageGaps(ctx context.Context, site, scope string, limit int) ([]domain.IDGap, error)
	PlanGapRanges(ctx context.Context, site, scope string, gaps []domain.IDGap, rangeSize int64) (int64, error)
	StaleRunningRanges(ctx context.Context, site, scope string, olderThan time.Duration) (int64, error)
	ReclaimDeadNodeLeases(ctx context.Context, timeout time.Duration) (int64, error)
	ActiveModels(ctx context.Context) ([]domain.EmbeddingModel, error)
	RetryFailedRanges(ctx context.Context, site, scope string) (int64, error)
	RetryPendingQueue(ctx context.Context, site, scope string) (int64, error)
}

var errNoConsole = errors.New("이 서버에서는 구간을 다루지 않습니다")

type jobsView struct {
	Manage         bool            `json:"manage"`
	Total          int64           `json:"total"`
	Completed      int64           `json:"completed"`
	Running        int64           `json:"running"`
	Empty          int64           `json:"empty"`
	Failed         int64           `json:"failed"`
	Exhausted      int64           `json:"exhausted"`
	MissingIDs     int64           `json:"missing_ids"`
	Saved          int64           `json:"saved"`
	StaleRunning   int64           `json:"stale_running"`
	RetryPending   int64           `json:"retry_pending"`
	RetryDead      int64           `json:"retry_dead"`
	HighWatermark  int64           `json:"high_watermark"`
	BackfillBefore int64           `json:"backfill_before"`
	FailedRanges   []rangeItemView `json:"failed_ranges"`
	Gaps           []gapView       `json:"gaps"`
}

type rangeItemView struct {
	ID       int64  `json:"id"`
	Lower    int64  `json:"lower"`
	Upper    int64  `json:"upper"`
	Count    int64  `json:"count"`
	Attempts int    `json:"attempts"`
	Error    string `json:"error"`
}

type gapView struct {
	From  int64 `json:"from"`
	To    int64 `json:"to"`
	Count int64 `json:"count"`
}

type modelView struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Backend    string `json:"backend"`
	VectorSize int    `json:"vector_size"`
	Collection string `json:"collection"`
	InputSize  int    `json:"input_size"`
}

func (s *Server) console() (ConsoleAdmin, error) {
	admin, ok := s.deps.Stats.(ConsoleAdmin)
	if !ok {
		return nil, errNoConsole
	}
	return admin, nil
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	crawl, err := s.deps.Stats.CrawlSummary(ctx, s.cfg.SourceSite, s.cfg.ScopeKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	view := jobsView{
		Completed:      crawl.RangesCompleted,
		Running:        crawl.RangesRunning,
		Failed:         crawl.RangesFailed,
		RetryPending:   crawl.RetryPending,
		RetryDead:      crawl.RetryDead,
		HighWatermark:  crawl.HighWatermark,
		BackfillBefore: crawl.BackfillBefore,
		FailedRanges:   []rangeItemView{},
		Gaps:           []gapView{},
	}
	admin, err := s.console()
	if err != nil {
		writeJSON(w, http.StatusOK, view)
		return
	}
	stats, err := admin.RangeStats(ctx, s.cfg.SourceSite, s.cfg.ScopeKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	stale, err := admin.StaleRunningRanges(ctx, s.cfg.SourceSite, s.cfg.ScopeKey, 30*time.Minute)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	items, err := admin.ExhaustedRanges(ctx, s.cfg.SourceSite, s.cfg.ScopeKey, 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	gaps, err := admin.CoverageGaps(ctx, s.cfg.SourceSite, s.cfg.ScopeKey, 20)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	view.Manage = true
	view.Total = stats.Total
	view.Completed = stats.Completed
	view.Running = stats.Running
	view.Empty = stats.Empty
	view.Failed = stats.Failed
	view.Exhausted = stats.Exhausted
	view.MissingIDs = stats.MissingIDs
	view.Saved = stats.Saved
	view.StaleRunning = stale
	view.FailedRanges = make([]rangeItemView, 0, len(items))
	for _, item := range items {
		view.FailedRanges = append(view.FailedRanges, rangeItemView{
			ID: item.ID, Lower: item.LowerID, Upper: item.UpperID,
			Count: item.Size(), Attempts: item.Attempts, Error: clipText(item.LastError, 160),
		})
	}
	view.Gaps = make([]gapView, 0, len(gaps))
	for _, gap := range gaps {
		view.Gaps = append(view.Gaps, gapView{From: gap.From, To: gap.To, Count: gap.Size()})
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleJobReset(w http.ResponseWriter, r *http.Request) {
	admin, err := s.console()
	if err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}
	var body struct {
		ID      int64 `json:"id"`
		Retries bool  `json:"retries"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, errors.New("요청을 해석하지 못했습니다"))
		return
	}
	ctx := r.Context()
	var count int64
	if body.ID > 0 {
		if err := admin.ResetRange(ctx, body.ID); err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, domain.ErrNotFound) {
				status = http.StatusNotFound
			}
			writeError(w, status, err)
			return
		}
		count = 1
	} else {
		count, err = admin.ResetExhaustedRanges(ctx, s.cfg.SourceSite, s.cfg.ScopeKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	var retries int64
	if body.Retries {
		retries, err = admin.ResetRetryQueue(ctx, s.cfg.SourceSite, s.cfg.ScopeKey)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]int64{"count": count, "retries": retries})
}

func (s *Server) handleJobRetry(w http.ResponseWriter, r *http.Request) {
	admin, err := s.console()
	if err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}
	var body struct {
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("요청을 해석하지 못했습니다"))
		return
	}
	var count int64
	switch body.Kind {
	case "ranges":
		count, err = admin.RetryFailedRanges(r.Context(), s.cfg.SourceSite, s.cfg.ScopeKey)
	case "queue":
		count, err = admin.RetryPendingQueue(r.Context(), s.cfg.SourceSite, s.cfg.ScopeKey)
	default:
		writeError(w, http.StatusBadRequest, errors.New("재시도 대상을 지정하십시오"))
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"count": count})
}

func (s *Server) handleJobFill(w http.ResponseWriter, r *http.Request) {
	admin, err := s.console()
	if err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}
	gaps, err := admin.CoverageGaps(r.Context(), s.cfg.SourceSite, s.cfg.ScopeKey, 1000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	size := s.cfg.RangeSize
	if size < 1 {
		size = 10000
	}
	count, err := admin.PlanGapRanges(r.Context(), s.cfg.SourceSite, s.cfg.ScopeKey, gaps, size)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"count": count})
}

func (s *Server) handleReclaim(w http.ResponseWriter, r *http.Request) {
	admin, err := s.console()
	if err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}
	count, err := admin.ReclaimDeadNodeLeases(r.Context(), s.cfg.NodeTimeout)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"count": count})
}

type TelegramControl interface {
	Status() (set bool, hint string)
	Set(ctx context.Context, token string) error
}

type Operator interface {
	Settings() map[string]any
	SaveSettings(ctx context.Context, raw []byte) error
	Restart() error
	SyncModels(ctx context.Context) (int, error)
	AddModel(ctx context.Context, raw []byte) error
	StartRebuild(batch int) error
	StartReembed(modelID string, batch, workers int, dry bool) error
	Job() (running, last, err string)
	Verify(ctx context.Context) ([]map[string]string, error)
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ops != nil {
		writeJSON(w, http.StatusOK, s.deps.Ops.Settings())
		return
	}
	tgSet, tgHint := false, ""
	if s.deps.Telegram != nil {
		tgSet, tgHint = s.deps.Telegram.Status()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node_id":         s.cfg.NodeID,
		"role":            s.cfg.Role,
		"bind":            s.cfg.Bind,
		"source_site":     s.cfg.SourceSite,
		"scope_key":       s.cfg.ScopeKey,
		"index_kind":      s.cfg.IndexKind,
		"data_dir":        s.cfg.DataDir,
		"index_dir":       s.cfg.IndexDir,
		"thumb_dir":       s.cfg.ThumbDir,
		"admin_user":      s.cfg.AdminUser,
		"password_in_env": s.cfg.AdminFile == "" && s.cfg.AdminPassword != "",
		"telegram_set":    tgSet,
		"telegram_hint":   tgHint,
	})
}

func (s *Server) handleTelegram(w http.ResponseWriter, r *http.Request) {
	if s.deps.Telegram == nil {
		writeError(w, http.StatusNotImplemented, errors.New("이 서버에서는 텔레그램을 바꾸지 않습니다"))
		return
	}
	var body struct {
		Token string `json:"token"`
		Clear bool   `json:"clear"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("요청을 해석하지 못했습니다"))
		return
	}
	token := ""
	if !body.Clear {
		token = strings.TrimSpace(body.Token)
		if token == "" {
			writeError(w, http.StatusBadRequest, errors.New("봇 토큰을 입력하십시오"))
			return
		}
	}
	if err := s.deps.Telegram.Set(r.Context(), token); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	set, hint := s.deps.Telegram.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"telegram_set":  set,
		"telegram_hint": hint,
	})
}

func (s *Server) requireOps() (Operator, error) {
	if s.deps.Ops == nil {
		return nil, errors.New("이 서버에서는 설정을 바꾸지 않습니다")
	}
	return s.deps.Ops, nil
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	ops, err := s.requireOps()
	if err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("설정을 읽지 못했습니다"))
		return
	}
	if err := ops.SaveSettings(r.Context(), raw); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"restarting": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		if err := ops.Restart(); err != nil {
			s.deps.Log.Error("다시 시작하지 못했습니다", slog.String("error", err.Error()))
		}
	}()
}

func (s *Server) handleSyncModels(w http.ResponseWriter, r *http.Request) {
	ops, err := s.requireOps()
	if err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}
	n, err := ops.SyncModels(r.Context())
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
}

func (s *Server) handleAddModel(w http.ResponseWriter, r *http.Request) {
	ops, err := s.requireOps()
	if err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("모델 정보를 읽지 못했습니다"))
		return
	}
	if err := ops.AddModel(r.Context(), raw); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRebuild(w http.ResponseWriter, r *http.Request) {
	ops, err := s.requireOps()
	if err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}
	var body struct {
		Batch int `json:"batch"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	if err := ops.StartRebuild(body.Batch); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) handleReembed(w http.ResponseWriter, r *http.Request) {
	ops, err := s.requireOps()
	if err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}
	var body struct {
		Model   string `json:"model"`
		Batch   int    `json:"batch"`
		Workers int    `json:"workers"`
		DryRun  bool   `json:"dry_run"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	if err := ops.StartReembed(body.Model, body.Batch, body.Workers, body.DryRun); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	ops, err := s.requireOps()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"running": "", "last": "", "error": ""})
		return
	}
	running, last, jobErr := ops.Job()
	writeJSON(w, http.StatusOK, map[string]string{"running": running, "last": last, "error": jobErr})
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	ops, err := s.requireOps()
	if err != nil {
		writeError(w, http.StatusNotImplemented, err)
		return
	}
	items, err := ops.Verify(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	admin, err := s.console()
	if err != nil {
		writeJSON(w, http.StatusOK, []modelView{})
		return
	}
	models, err := admin.ActiveModels(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]modelView, 0, len(models))
	for _, m := range models {
		out = append(out, modelView{
			ID: m.ID, Kind: string(m.Kind), Backend: m.Backend,
			VectorSize: m.VectorSize, Collection: m.Collection, InputSize: m.InputSize,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
