package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"saucedust/internal/domain"
)

type fakeNodes struct {
	mu         sync.Mutex
	registered []string
	beats      int
	sweeps     int
	stopped    []string
	reclaimed  []string
	metrics    []domain.NodeMetrics
	beatErr    error
}

func (f *fakeNodes) RegisterNode(_ context.Context, n domain.Node) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registered = append(f.registered, n.ID)
	return nil
}

func (f *fakeNodes) Heartbeat(_ context.Context, _ string, _ domain.NetMode, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beats++
	return f.beatErr
}

func (f *fakeNodes) MarkNodeStopped(_ context.Context, nodeID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = append(f.stopped, nodeID)
	return nil
}

func (f *fakeNodes) ListNodes(context.Context, time.Duration) ([]domain.Node, error) {
	return nil, nil
}

func (f *fakeNodes) ReclaimOwnLeases(_ context.Context, nodeID string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reclaimed = append(f.reclaimed, nodeID)
	return 0, nil
}

func (f *fakeNodes) ReclaimDeadNodeLeases(context.Context, time.Duration) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sweeps++
	return 0, nil
}

func (f *fakeNodes) RecordMetrics(_ context.Context, m domain.NodeMetrics) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metrics = append(f.metrics, m)
	return nil
}

func (f *fakeNodes) PruneMetrics(context.Context, time.Duration) error { return nil }

func (f *fakeNodes) snapshot() fakeNodes {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeNodes{
		registered: append([]string(nil), f.registered...),
		beats:      f.beats,
		sweeps:     f.sweeps,
		stopped:    append([]string(nil), f.stopped...),
		reclaimed:  append([]string(nil), f.reclaimed...),
		metrics:    append([]domain.NodeMetrics(nil), f.metrics...),
	}
}

func newTestFleet(t *testing.T, nodes NodeRepository, role domain.Role) *Fleet {
	t.Helper()
	fleet, err := NewFleet(FleetConfig{
		NodeID: "test-node", Role: role,
		HeartbeatEvery: 20 * time.Millisecond,
		NodeTimeout:    60 * time.Millisecond,
	}, nodes, quietLogger())
	if err != nil {
		t.Fatalf("생성 실패: %v", err)
	}
	return fleet
}

// Run은 등록을 스스로 해야 합니다. 호출자가 잊으면 heartbeat가 계속 실패합니다.
func TestRunRegistersBeforeBeating(t *testing.T) {
	nodes := &fakeNodes{}
	fleet := newTestFleet(t, nodes, domain.RoleWorker)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	if err := fleet.Run(ctx); err != nil {
		t.Fatalf("실행 실패: %v", err)
	}

	got := nodes.snapshot()
	if len(got.registered) != 1 || got.registered[0] != "test-node" {
		t.Fatalf("등록 기록이 %v입니다", got.registered)
	}
	if got.beats < 2 {
		t.Fatalf("heartbeat가 %d번뿐입니다", got.beats)
	}
	if len(got.metrics) < 2 {
		t.Fatalf("지표 기록이 %d번뿐입니다", len(got.metrics))
	}
}

// 종료할 때 임대를 반납하고 오프라인으로 표시해야 다른 노드가 이어받습니다.
func TestRunReleasesOnShutdown(t *testing.T) {
	nodes := &fakeNodes{}
	fleet := newTestFleet(t, nodes, domain.RoleWorker)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	fleet.Run(ctx)

	got := nodes.snapshot()
	if len(got.stopped) != 1 {
		t.Fatalf("종료 표시가 %v입니다", got.stopped)
	}
	// 시작 시 한 번, 종료 시 한 번 반납합니다.
	if len(got.reclaimed) != 2 {
		t.Fatalf("임대 반납이 %d번입니다. 2번을 기대했습니다", len(got.reclaimed))
	}
}

// 죽은 노드 회수는 control 노드만 합니다. worker가 하면 서로 뺏습니다.
func TestOnlyControlSweepsDeadNodes(t *testing.T) {
	run := func(role domain.Role) int {
		nodes := &fakeNodes{}
		fleet := newTestFleet(t, nodes, role)

		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		fleet.Run(ctx)

		return nodes.snapshot().sweeps
	}

	if got := run(domain.RoleWorker); got != 0 {
		t.Fatalf("worker가 %d번 회수했습니다. 하면 안 됩니다", got)
	}
	if got := run(domain.RoleControl); got < 1 {
		t.Fatalf("control이 %d번 회수했습니다. 최소 1번은 해야 합니다", got)
	}
}

// heartbeat가 실패해도 노드를 죽이면 안 됩니다. 잠깐 DB가 느릴 수 있습니다.
func TestHeartbeatFailureDoesNotStop(t *testing.T) {
	nodes := &fakeNodes{beatErr: errors.New("연결이 끊겼습니다")}
	fleet := newTestFleet(t, nodes, domain.RoleWorker)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := fleet.Run(ctx); err != nil {
		t.Fatalf("heartbeat 실패로 멈추면 안 됩니다: %v", err)
	}
	if nodes.snapshot().beats < 2 {
		t.Fatal("실패해도 계속 시도해야 합니다")
	}
}

func TestFleetRejectsBadConfig(t *testing.T) {
	cases := map[string]FleetConfig{
		"식별자 없음": {Role: domain.RoleWorker, HeartbeatEvery: time.Second, NodeTimeout: 2 * time.Second},
		"만료가 더 짧음": {NodeID: "a", Role: domain.RoleWorker,
			HeartbeatEvery: 30 * time.Second, NodeTimeout: 10 * time.Second},
	}
	for name, cfg := range cases {
		if _, err := NewFleet(cfg, &fakeNodes{}, quietLogger()); err == nil {
			t.Errorf("%s는 거부해야 합니다", name)
		}
	}
}

func TestCountersFlowIntoMetrics(t *testing.T) {
	nodes := &fakeNodes{}
	fleet := newTestFleet(t, nodes, domain.RoleWorker)

	fleet.Counters().Saved.Add(42)
	fleet.Counters().Failed.Add(3)
	fleet.SetNetMode(domain.NetFragment)
	fleet.SetConcurrency(9)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	fleet.Run(ctx)

	got := nodes.snapshot()
	if len(got.metrics) == 0 {
		t.Fatal("지표가 기록되지 않았습니다")
	}
	last := got.metrics[len(got.metrics)-1]
	switch {
	case last.Saved != 42:
		t.Fatalf("저장 수가 %d입니다", last.Saved)
	case last.Failed != 3:
		t.Fatalf("실패 수가 %d입니다", last.Failed)
	case last.NetMode != domain.NetFragment:
		t.Fatalf("경로가 %q입니다", last.NetMode)
	case last.Concurrency != 9:
		t.Fatalf("동시성이 %d입니다", last.Concurrency)
	}
}
