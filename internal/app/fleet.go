package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"sync/atomic"
	"time"

	"saucedust/internal/domain"
)

const Version = "0.2.0"

type FleetConfig struct {
	NodeID         string
	Role           domain.Role
	HeartbeatEvery time.Duration
	NodeTimeout    time.Duration
	MetricsRetain  time.Duration
}

// Fleet은 노드 하나의 생존 신호와 현재 상태를 관리하는 유스케이스입니다.
// control 노드는 응답이 끊긴 다른 노드의 임대 회수도 함께 맡습니다.
type Fleet struct {
	cfg   FleetConfig
	nodes NodeRepository
	log   *slog.Logger

	netMode     atomic.Value
	device      atomic.Value
	concurrency atomic.Int64
	counters    Counters
}

type Counters struct {
	Downloaded atomic.Int64
	Embedded   atomic.Int64
	Saved      atomic.Int64
	Failed     atomic.Int64
}

func NewFleet(cfg FleetConfig, nodes NodeRepository, log *slog.Logger) (*Fleet, error) {
	if cfg.NodeID == "" {
		return nil, fmt.Errorf("노드 식별자가 비어 있습니다")
	}
	if cfg.NodeTimeout <= cfg.HeartbeatEvery {
		return nil, fmt.Errorf("노드 만료 시간은 heartbeat 주기보다 커야 합니다")
	}
	if cfg.MetricsRetain <= 0 {
		cfg.MetricsRetain = 24 * time.Hour
	}

	f := &Fleet{cfg: cfg, nodes: nodes, log: log}
	f.netMode.Store(domain.NetUnknown)
	f.device.Store(domain.DeviceUnknown)
	return f, nil
}

func (f *Fleet) NodeID() string              { return f.cfg.NodeID }
func (f *Fleet) Counters() *Counters         { return &f.counters }
func (f *Fleet) SetNetMode(m domain.NetMode) { f.netMode.Store(m) }
func (f *Fleet) SetDevice(d domain.Device)   { f.device.Store(d) }
func (f *Fleet) SetConcurrency(n int)        { f.concurrency.Store(int64(n)) }
func (f *Fleet) NetMode() domain.NetMode     { return f.netMode.Load().(domain.NetMode) }
func (f *Fleet) Device() domain.Device       { return f.device.Load().(domain.Device) }
func (f *Fleet) Concurrency() int            { return int(f.concurrency.Load()) }

// Register는 노드를 등록하고 이전 실행이 남긴 자기 임대만 회수합니다.
// 다른 노드가 처리 중인 구간은 절대 건드리지 않습니다.
func (f *Fleet) Register(ctx context.Context) error {
	hostname, _ := os.Hostname()

	err := f.nodes.RegisterNode(ctx, domain.Node{
		ID:          f.cfg.NodeID,
		Role:        f.cfg.Role,
		Hostname:    hostname,
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		Version:     Version,
		Device:      f.Device(),
		NetMode:     f.NetMode(),
		Concurrency: f.Concurrency(),
	})
	if err != nil {
		return err
	}

	reclaimed, err := f.nodes.ReclaimOwnLeases(ctx, f.cfg.NodeID)
	if err != nil {
		return err
	}
	if reclaimed > 0 {
		f.log.Info("이전 실행이 남긴 임대를 회수했습니다", slog.Int64("count", reclaimed))
	}
	return nil
}

// Run은 노드를 등록한 뒤 heartbeat와 지표 기록을 주기적으로 수행합니다.
// 등록을 여기서 하는 이유는 호출자가 순서를 잊으면 heartbeat가 계속 실패하기
// 때문입니다. 등록과 갱신은 떼어놓을 수 없는 한 쌍입니다.
// ctx가 끝나면 임대를 반납하고 오프라인으로 표시합니다.
func (f *Fleet) Run(ctx context.Context) error {
	if err := f.Register(ctx); err != nil {
		return err
	}

	beat := time.NewTicker(f.cfg.HeartbeatEvery)
	defer beat.Stop()

	sweep := time.NewTicker(f.cfg.NodeTimeout)
	defer sweep.Stop()
	if f.cfg.Role != domain.RoleControl {
		sweep.Stop()
	}

	for {
		select {
		case <-ctx.Done():
			f.shutdown()
			return nil

		case <-beat.C:
			f.beat(ctx)

		case <-sweep.C:
			f.sweep(ctx)
		}
	}
}

func (f *Fleet) beat(ctx context.Context) {
	if err := f.nodes.Heartbeat(ctx, f.cfg.NodeID, f.NetMode(), f.Concurrency()); err != nil {
		f.log.Warn("heartbeat 실패", slog.String("error", err.Error()))
		return
	}

	cpu, mem := processUsage()
	metrics := domain.NodeMetrics{
		NodeID:      f.cfg.NodeID,
		CPUPct:      cpu,
		MemMB:       mem,
		Concurrency: f.Concurrency(),
		NetMode:     f.NetMode(),
		Downloaded:  f.counters.Downloaded.Load(),
		Embedded:    f.counters.Embedded.Load(),
		Saved:       f.counters.Saved.Load(),
		Failed:      f.counters.Failed.Load(),
	}
	if err := f.nodes.RecordMetrics(ctx, metrics); err != nil {
		f.log.Warn("지표 기록 실패", slog.String("error", err.Error()))
	}
}

func (f *Fleet) sweep(ctx context.Context) {
	n, err := f.nodes.ReclaimDeadNodeLeases(ctx, f.cfg.NodeTimeout)
	if err != nil {
		f.log.Warn("응답 없는 노드 회수 실패", slog.String("error", err.Error()))
		return
	}
	if n > 0 {
		f.log.Info("응답 없는 노드의 임대를 회수했습니다", slog.Int64("count", n))
	}
	if err := f.nodes.PruneMetrics(ctx, f.cfg.MetricsRetain); err != nil {
		f.log.Warn("오래된 지표 정리 실패", slog.String("error", err.Error()))
	}
}

func (f *Fleet) shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := f.nodes.ReclaimOwnLeases(ctx, f.cfg.NodeID); err != nil {
		f.log.Warn("종료 중 임대 반납 실패", slog.String("error", err.Error()))
	}
	if err := f.nodes.MarkNodeStopped(ctx, f.cfg.NodeID); err != nil {
		f.log.Warn("종료 상태 기록 실패", slog.String("error", err.Error()))
	}
}

func processUsage() (cpuPct, memMB float32) {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return 0, float32(stats.Sys) / (1024 * 1024)
}
