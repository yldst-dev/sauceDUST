package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"saucedust/internal/domain"
)

func (s *Store) RegisterNode(ctx context.Context, n domain.Node) error {
	const q = `
INSERT INTO nodes (id, role, hostname, platform, version, device, net_mode, concurrency,
                   status, started_at, heartbeat_at, stopped_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'online', now(), now(), NULL)
ON CONFLICT (id) DO UPDATE SET
    role        = EXCLUDED.role,
    hostname    = EXCLUDED.hostname,
    platform    = EXCLUDED.platform,
    version     = EXCLUDED.version,
    device      = EXCLUDED.device,
    net_mode    = EXCLUDED.net_mode,
    concurrency = EXCLUDED.concurrency,
    status      = 'online',
    started_at  = now(),
    heartbeat_at= now(),
    stopped_at  = NULL`

	_, err := s.pool.Exec(ctx, q, n.ID, string(n.Role), n.Hostname, n.Platform,
		n.Version, string(n.Device), string(n.NetMode), n.Concurrency)
	if err != nil {
		return fmt.Errorf("노드 등록에 실패했습니다: %w", err)
	}
	return nil
}

func (s *Store) Heartbeat(ctx context.Context, nodeID string, netMode domain.NetMode, concurrency int) error {
	const q = `
UPDATE nodes
SET heartbeat_at = now(), status = 'online', net_mode = $2, concurrency = $3
WHERE id = $1`

	tag, err := s.pool.Exec(ctx, q, nodeID, string(netMode), concurrency)
	if err != nil {
		return fmt.Errorf("heartbeat 갱신에 실패했습니다: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: 노드 %s", domain.ErrNotFound, nodeID)
	}
	return nil
}

func (s *Store) MarkNodeStopped(ctx context.Context, nodeID string) error {
	const q = `UPDATE nodes SET status = 'offline', stopped_at = now() WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, nodeID)
	return err
}

func (s *Store) ListNodes(ctx context.Context, timeout time.Duration) ([]domain.Node, error) {
	const q = `
SELECT id, role, hostname, platform, version, device, net_mode, concurrency,
       CASE WHEN status = 'online' AND heartbeat_at > now() - $1::interval
            THEN 'online' ELSE 'offline' END AS effective_status,
       started_at, heartbeat_at
FROM nodes
ORDER BY role, id`

	rows, err := s.pool.Query(ctx, q, timeout)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.Node
	for rows.Next() {
		var n domain.Node
		if err := rows.Scan(&n.ID, &n.Role, &n.Hostname, &n.Platform, &n.Version,
			&n.Device, &n.NetMode, &n.Concurrency, &n.Status, &n.StartedAt, &n.HeartbeatAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ReclaimOwnLeases는 자기 노드가 남긴 임대만 회수합니다. 다른 노드가 처리 중인
// 구간은 건드리지 않습니다.
func (s *Store) ReclaimOwnLeases(ctx context.Context, nodeID string) (int64, error) {
	return s.reclaim(ctx, `node_id = $1`, `node_id = $1`, nodeID)
}

// ReclaimDeadNodeLeases는 heartbeat가 끊긴 노드의 임대를 회수합니다.
func (s *Store) ReclaimDeadNodeLeases(ctx context.Context, timeout time.Duration) (int64, error) {
	const cond = `node_id IS NOT NULL AND node_id NOT IN (
        SELECT id FROM nodes WHERE status = 'online' AND heartbeat_at > now() - $1::interval
    )`
	return s.reclaim(ctx, cond, cond, timeout)
}

func (s *Store) reclaim(ctx context.Context, rangeCond, retryCond string, arg any) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	rangeTag, err := tx.Exec(ctx, fmt.Sprintf(`
UPDATE crawl_ranges
SET status = 'failed', node_id = NULL, last_error = '노드 임대 회수'
WHERE status = 'running' AND (%s)`, rangeCond), arg)
	if err != nil {
		return 0, fmt.Errorf("구간 임대 회수에 실패했습니다: %w", err)
	}

	retryTag, err := tx.Exec(ctx, fmt.Sprintf(`
UPDATE crawl_post_retries
SET status = 'pending', node_id = NULL, leased_at = NULL
WHERE status = 'running' AND (%s)`, retryCond), arg)
	if err != nil {
		return 0, fmt.Errorf("재시도 임대 회수에 실패했습니다: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return rangeTag.RowsAffected() + retryTag.RowsAffected(), nil
}

// UpsertModel은 모델을 등록합니다. 같은 용도의 활성 모델은 하나뿐이므로,
// 새 모델을 활성으로 넣으면 이전 것을 자동으로 내립니다.
// 내려간 모델의 벡터는 그대로 남아 있어 되돌릴 수 있습니다.
func (s *Store) UpsertModel(ctx context.Context, m domain.EmbeddingModel) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if m.Active {
		_, err := tx.Exec(ctx, `
UPDATE embedding_models SET active = FALSE
WHERE kind = $1 AND active AND id <> $2`, string(m.Kind), m.ID)
		if err != nil {
			return fmt.Errorf("이전 모델을 내리지 못했습니다: %w", err)
		}
	}

	const q = `
INSERT INTO embedding_models (id, kind, backend, checkpoint, vector_size, distance,
                              collection, input_size, active)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (id) DO UPDATE SET
    kind        = EXCLUDED.kind,
    backend     = EXCLUDED.backend,
    checkpoint  = EXCLUDED.checkpoint,
    vector_size = EXCLUDED.vector_size,
    distance    = EXCLUDED.distance,
    collection  = EXCLUDED.collection,
    input_size  = EXCLUDED.input_size,
    active      = EXCLUDED.active`

	_, err = tx.Exec(ctx, q, m.ID, string(m.Kind), m.Backend, m.Checkpoint,
		m.VectorSize, m.Distance, m.Collection, m.InputSize, m.Active)
	if err != nil {
		return fmt.Errorf("모델 등록에 실패했습니다: %w", err)
	}
	return tx.Commit(ctx)
}

// DeactivateModel은 모델을 비활성으로 내립니다. 벡터는 남습니다.
func (s *Store) DeactivateModel(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE embedding_models SET active = FALSE WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: 모델 %s", domain.ErrNotFound, id)
	}
	return nil
}

func (s *Store) ActiveModels(ctx context.Context) ([]domain.EmbeddingModel, error) {
	const q = `
SELECT id, kind, backend, checkpoint, vector_size, distance, collection, input_size, active
FROM embedding_models
WHERE active
ORDER BY kind`

	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.EmbeddingModel
	for rows.Next() {
		var m domain.EmbeddingModel
		if err := rows.Scan(&m.ID, &m.Kind, &m.Backend, &m.Checkpoint, &m.VectorSize,
			&m.Distance, &m.Collection, &m.InputSize, &m.Active); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// VerifyNodeModels는 노드가 보고한 모델이 등록된 활성 모델과 같은지 확인합니다.
// 하나라도 다르면 벡터가 서로 비교 불가능해지므로 작업을 시작하지 않습니다.
func (s *Store) VerifyNodeModels(ctx context.Context, nodeID string, reported []domain.EmbeddingModel) error {
	active, err := s.ActiveModels(ctx)
	if err != nil {
		return err
	}
	if len(active) == 0 {
		return fmt.Errorf("활성 임베딩 모델이 등록되어 있지 않습니다")
	}

	byID := make(map[string]domain.EmbeddingModel, len(reported))
	for _, m := range reported {
		byID[m.ID] = m
	}

	for _, want := range active {
		got, ok := byID[want.ID]
		if !ok {
			return fmt.Errorf("%w: 노드 %s에 모델 %s가 없습니다",
				domain.ErrModelMismatch, nodeID, want.ID)
		}
		if got.VectorSize != want.VectorSize {
			return fmt.Errorf("%w: 노드 %s의 모델 %s 벡터 크기가 %d입니다. 기준은 %d입니다",
				domain.ErrModelMismatch, nodeID, want.ID, got.VectorSize, want.VectorSize)
		}
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM node_models WHERE node_id = $1`, nodeID); err != nil {
		return err
	}
	for _, m := range reported {
		_, err := tx.Exec(ctx, `
INSERT INTO node_models (node_id, model_id, vector_size, device, reported_at)
VALUES ($1, $2, $3, $4, now())`, nodeID, m.ID, m.VectorSize, m.Backend)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) RecordMetrics(ctx context.Context, m domain.NodeMetrics) error {
	const q = `
INSERT INTO node_metrics (node_id, observed_at, cpu_pct, mem_mb, device_pct,
                          concurrency, net_mode, downloaded, embedded, saved, failed)
VALUES ($1, now(), $2, $3, $4, $5, $6, $7, $8, $9, $10)
ON CONFLICT (node_id, observed_at) DO NOTHING`

	_, err := s.pool.Exec(ctx, q, m.NodeID, m.CPUPct, m.MemMB, m.DevicePct,
		m.Concurrency, string(m.NetMode), m.Downloaded, m.Embedded, m.Saved, m.Failed)
	return err
}

func (s *Store) PruneMetrics(ctx context.Context, keep time.Duration) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM node_metrics WHERE observed_at < now() - $1::interval`, keep)
	return err
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
