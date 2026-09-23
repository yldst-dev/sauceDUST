package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"syscall"

	"saucedust/internal/adapter/filestore"
	"saucedust/internal/adapter/httpapi"
	"saucedust/internal/app"
	"saucedust/internal/config"
	"saucedust/internal/domain"
)

var _ httpapi.Operator = (*webOps)(nil)

type webOps struct {
	rt        *nodeRuntime
	ingest    *app.Ingest
	embedder  app.Embedder
	bots      *botHost
	adminUser string
	adminFile string

	jobMu   sync.Mutex
	running string
	last    string
	jobErr  string
}

func newWebOps(rt *nodeRuntime, ingest *app.Ingest, embedder app.Embedder, bots *botHost, adminUser, adminFile string) *webOps {
	return &webOps{
		rt: rt, ingest: ingest, embedder: embedder, bots: bots,
		adminUser: adminUser, adminFile: adminFile,
	}
}

func (o *webOps) lock()   { o.jobMu.Lock() }
func (o *webOps) unlock() { o.jobMu.Unlock() }

func (o *webOps) Settings() map[string]any {
	file := config.WebFileFrom(o.rt.cfg)
	raw, _ := json.Marshal(file)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	out["node_id"] = o.rt.cfg.NodeID
	out["role"] = string(o.rt.cfg.Role)
	out["bind"] = o.rt.cfg.ControlBind
	out["source_site"] = o.rt.cfg.SourceSite
	out["scope_key"] = o.rt.cfg.ScopeKey
	out["index_kind"] = string(o.rt.cfg.IndexKind)
	out["data_dir"] = o.rt.cfg.DataDir
	out["index_dir"] = o.rt.cfg.IndexDir
	out["thumb_dir"] = o.rt.cfg.ThumbDir
	out["admin_user"] = o.adminUser
	out["password_in_env"] = o.adminFile == "" && o.rt.cfg.AdminPassword != ""
	tgSet, tgHint := false, ""
	if o.bots != nil {
		tgSet, tgHint = o.bots.Status()
	}
	out["telegram_set"] = tgSet
	out["telegram_hint"] = tgHint
	running, last, jobErr := o.Job()
	out["job_running"] = running
	out["job_last"] = last
	out["job_error"] = jobErr
	return out
}

func (o *webOps) SaveSettings(_ context.Context, raw []byte) error {
	file, err := config.MergeWebFile(o.rt.cfg, raw)
	if err != nil {
		return err
	}
	return config.WriteWebFile(o.rt.cfg.DataDir, file)
}

func (o *webOps) Restart() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, append([]string{exe}, os.Args[1:]...), os.Environ())
}

func (o *webOps) Job() (string, string, string) {
	o.lock()
	defer o.unlock()
	return o.running, o.last, o.jobErr
}

func (o *webOps) start(name string, fn func(context.Context) (string, error)) error {
	o.lock()
	if o.running != "" {
		busy := o.running
		o.unlock()
		return fmt.Errorf("이미 %s 작업이 돌고 있습니다", busy)
	}
	o.running = name
	o.jobErr = ""
	o.unlock()
	go func() {
		ctx := context.Background()
		note, err := fn(ctx)
		o.lock()
		o.running = ""
		if err != nil {
			o.jobErr = err.Error()
			o.last = name + " 실패"
			o.rt.log.Error("웹 작업 실패", slog.String("작업", name), slog.String("error", err.Error()))
		} else {
			o.jobErr = ""
			if note == "" {
				note = name + " 완료"
			}
			o.last = note
			o.rt.log.Info("웹 작업 완료", slog.String("작업", name), slog.String("결과", note))
		}
		o.unlock()
	}()
	return nil
}

func (o *webOps) SyncModels(ctx context.Context) (int, error) {
	specs, err := workerModelSpecs()
	if err != nil {
		return 0, err
	}
	for _, spec := range specs {
		m := spec.model()
		if err := o.rt.store.UpsertModel(ctx, m); err != nil {
			return 0, fmt.Errorf("모델 %s를 등록하지 못했습니다: %w", m.ID, err)
		}
	}
	o.rt.log.Info("워커가 싣는 모델을 등록했습니다", slog.Int("개수", len(specs)))
	return len(specs), nil
}

func (o *webOps) AddModel(ctx context.Context, raw []byte) error {
	var body struct {
		ID         string `json:"id"`
		Kind       string `json:"kind"`
		Backend    string `json:"backend"`
		Checkpoint string `json:"checkpoint"`
		VectorSize int    `json:"vector_size"`
		InputSize  int    `json:"input_size"`
		Collection string `json:"collection"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return errors.New("모델 정보를 해석하지 못했습니다")
	}
	if body.ID == "" || body.VectorSize <= 0 {
		return errors.New("모델 이름과 차원은 필수입니다")
	}
	kind := domain.ModelKind(body.Kind)
	if kind != domain.ModelCopy && kind != domain.ModelSemantic {
		kind = domain.ModelCopy
	}
	input := body.InputSize
	if input <= 0 {
		input = 224
	}
	collection := body.Collection
	if collection == "" {
		collection = body.ID
	}
	return o.rt.store.UpsertModel(ctx, domain.EmbeddingModel{
		ID: body.ID, Kind: kind, Backend: body.Backend, Checkpoint: body.Checkpoint,
		VectorSize: body.VectorSize, Distance: "cosine", Collection: collection,
		InputSize: input, Active: true,
	})
}

func (o *webOps) StartRebuild(batch int) error {
	if batch < 1 {
		batch = 512
	}
	if o.ingest == nil {
		return errors.New("색인을 다시 채울 수집기가 없습니다")
	}
	return o.start("색인 다시 채우기", func(ctx context.Context) (string, error) {
		n, err := o.ingest.RebuildIndex(ctx, batch)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("벡터 %d개를 색인에 넣었습니다", n), nil
	})
}

func (o *webOps) StartReembed(modelID string, batch, workers int, dry bool) error {
	if batch < 1 {
		batch = 256
	}
	if workers < 1 {
		workers = 8
	}
	return o.start("벡터 다시 계산", func(ctx context.Context) (string, error) {
		models, err := o.rt.activeModels(ctx, o.embedder)
		if err != nil {
			return "", err
		}
		if modelID != "" {
			models = filterModels(models, modelID)
			if len(models) == 0 {
				return "", fmt.Errorf("활성 모델 중에 %q가 없습니다", modelID)
			}
		}
		if dry {
			parts := ""
			for _, m := range models {
				n, err := o.rt.store.CountThumbsMissingVector(ctx, m.ID)
				if err != nil {
					return "", err
				}
				if parts != "" {
					parts += ", "
				}
				parts += fmt.Sprintf("%s %d건", m.ID, n)
			}
			if parts == "" {
				parts = "다시 계산할 모델이 없습니다"
			}
			return parts, nil
		}
		if err := checkReembedRoom(ctx, o.rt, models); err != nil {
			return "", err
		}
		index, err := o.rt.newIndex(false)
		if err != nil {
			return "", err
		}
		thumbs, err := filestore.NewThumbStore(o.rt.cfg.ThumbDir)
		if err != nil {
			return "", err
		}
		job, err := app.NewReembed(app.ReembedDeps{
			Images: o.rt.store, Vector: o.rt.store, Index: index, Thumbs: thumbs,
			Embedder: o.embedder, Log: o.rt.log, Workers: workers, BatchSize: batch,
		})
		if err != nil {
			return "", err
		}
		note := ""
		for _, m := range models {
			result, err := job.Run(ctx, m)
			if err != nil {
				return "", err
			}
			if note != "" {
				note += ", "
			}
			note += fmt.Sprintf("%s 완료 %d, 파일 없음 %d, 실패 %d", m.ID, result.Done, result.Missing, result.Failed)
		}
		return note, nil
	})
}

func (o *webOps) Verify(ctx context.Context) ([]map[string]string, error) {
	items := make([]map[string]string, 0, 5)
	add := func(name, note string, err error) {
		item := map[string]string{"name": name, "note": note, "ok": "true"}
		if err != nil {
			item["ok"] = "false"
			item["note"] = err.Error()
		}
		items = append(items, item)
	}
	if err := o.rt.store.Ping(ctx); err != nil {
		add("PostgreSQL", "", err)
	} else {
		n, err := o.rt.store.CountImages(ctx)
		add("PostgreSQL", fmt.Sprintf("이미지 %d건", n), err)
	}
	models, err := o.rt.store.ActiveModels(ctx)
	if err != nil {
		add("등록된 모델", "", err)
	} else if len(models) == 0 {
		add("등록된 모델", "", errors.New("활성 모델이 없습니다"))
	} else {
		add("등록된 모델", fmt.Sprintf("%d개", len(models)), nil)
	}
	if o.embedder == nil {
		add("임베딩 워커", "", errors.New("임베딩 워커가 연결되지 않았습니다"))
	} else if info, err := o.embedder.Describe(ctx); err != nil {
		add("임베딩 워커", "", err)
	} else {
		add("임베딩 워커", fmt.Sprintf("장치 %s, 모델 %d개", info.Device, len(info.Models)), nil)
	}
	if index, err := o.rt.newIndex(false); err != nil {
		add("벡터 색인", "", err)
	} else if err := index.Ping(ctx); err != nil {
		add("벡터 색인", "", err)
	} else {
		add("벡터 색인", string(o.rt.cfg.IndexKind), nil)
	}
	chain, err := o.rt.newNetChain()
	if err != nil {
		add("수집 대상", "", err)
	} else if source, err := o.rt.newSource(chain); err != nil {
		add("수집 대상", "", err)
	} else if latest, err := source.LatestPostID(ctx); err != nil {
		add("수집 대상", "", err)
	} else {
		auth := "익명"
		if source.Authenticated() {
			auth = "인증됨"
		}
		add("수집 대상", fmt.Sprintf("최신 %d, %s", latest, auth), nil)
	}
	return items, nil
}
