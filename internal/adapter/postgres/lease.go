package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"saucedust/internal/domain"
)

const (
	staleLease  = 30 * time.Minute
	maxAttempts = 5
)

// AcquireBackfillRange는 백필 구간 하나를 이 노드 앞으로 임대합니다.
// 먼저 실패했거나 임대가 만료된 구간을 재사용하고, 없으면 새 구간을 잘라냅니다.
// 반환된 Attempts는 완료 보고 시 fencing 토큰으로 씁니다.
func (s *Store) AcquireBackfillRange(ctx context.Context, req domain.LeaseRequest) (*domain.CrawlRange, error) {
	if req.RangeSize < 1 {
		return nil, fmt.Errorf("구간 크기는 1 이상이어야 합니다")
	}
	if req.FloorID < 1 {
		req.FloorID = 1
	}

	if r, err := s.reuseRange(ctx, req); err == nil {
		return r, nil
	} else if !errors.Is(err, domain.ErrNoWork) {
		return nil, err
	}
	return s.carveRange(ctx, req)
}

func (s *Store) reuseRange(ctx context.Context, req domain.LeaseRequest) (*domain.CrawlRange, error) {
	const q = `
WITH candidate AS (
    SELECT id
    FROM crawl_ranges
    WHERE source_site = $1
      AND scope_key = $2
      AND direction = $3
      AND attempts < $6
      AND (status = 'failed'
           OR (status = 'running' AND leased_at < now() - $5::interval))
    ORDER BY attempts, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE crawl_ranges r
SET status     = 'running',
    attempts   = r.attempts + 1,
    node_id    = $4,
    leased_at  = now(),
    last_error = NULL
FROM candidate c
WHERE r.id = c.id
RETURNING r.id, r.lower_id, r.upper_id, r.attempts`

	var out domain.CrawlRange
	err := s.pool.QueryRow(ctx, q, req.SourceSite, req.ScopeKey, string(domain.DirectionBackfill),
		req.NodeID, staleLease, maxAttempts).
		Scan(&out.ID, &out.LowerID, &out.UpperID, &out.Attempts)
	if err != nil {
		if isNoRows(err) {
			return nil, domain.ErrNoWork
		}
		return nil, fmt.Errorf("구간 재사용에 실패했습니다: %w", err)
	}

	out.SourceSite = req.SourceSite
	out.ScopeKey = req.ScopeKey
	out.Direction = domain.DirectionBackfill
	out.NodeID = req.NodeID
	return &out, nil
}

func (s *Store) carveRange(ctx context.Context, req domain.LeaseRequest) (*domain.CrawlRange, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
INSERT INTO crawl_states (source_site, scope_key)
VALUES ($1, $2)
ON CONFLICT (source_site, scope_key) DO NOTHING`, req.SourceSite, req.ScopeKey); err != nil {
		return nil, err
	}

	var before *int64
	err = tx.QueryRow(ctx, `
SELECT backfill_before_id
FROM crawl_states
WHERE source_site = $1 AND scope_key = $2
FOR UPDATE`, req.SourceSite, req.ScopeKey).Scan(&before)
	if err != nil {
		return nil, fmt.Errorf("크롤 상태를 잠그지 못했습니다: %w", err)
	}
	if before == nil {
		return nil, domain.ErrNoWork
	}

	upper := *before - 1
	if upper < req.FloorID {
		return nil, domain.ErrNoWork
	}
	lower := upper - req.RangeSize + 1
	if lower < req.FloorID {
		lower = req.FloorID
	}

	var out domain.CrawlRange
	err = tx.QueryRow(ctx, `
INSERT INTO crawl_ranges (source_site, scope_key, direction, lower_id, upper_id,
                          status, attempts, node_id, leased_at)
VALUES ($1, $2, $3, $4, $5, 'running', 1, $6, now())
ON CONFLICT (source_site, scope_key, direction, lower_id, upper_id) DO UPDATE SET
    status    = 'running',
    attempts  = crawl_ranges.attempts + 1,
    node_id   = EXCLUDED.node_id,
    leased_at = now()
RETURNING id, attempts`, req.SourceSite, req.ScopeKey, string(domain.DirectionBackfill),
		lower, upper, req.NodeID).Scan(&out.ID, &out.Attempts)
	if err != nil {
		return nil, fmt.Errorf("새 구간 생성에 실패했습니다: %w", err)
	}

	if _, err := tx.Exec(ctx, `
UPDATE crawl_states
SET backfill_before_id = $3, updated_at = now()
WHERE source_site = $1 AND scope_key = $2`, req.SourceSite, req.ScopeKey, lower); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	out.SourceSite = req.SourceSite
	out.ScopeKey = req.ScopeKey
	out.Direction = domain.DirectionBackfill
	out.LowerID = lower
	out.UpperID = upper
	out.NodeID = req.NodeID
	return &out, nil
}

// FinishRange는 임대한 노드가 attempts 토큰과 함께 보고할 때만 반영됩니다.
// 이미 회수되어 다른 노드가 가져간 구간에는 아무 영향을 주지 않습니다.
func (s *Store) FinishRange(ctx context.Context, r *domain.CrawlRange, status domain.RangeStatus, saved int, cause error) error {
	var detail *string
	if cause != nil {
		msg := cause.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		detail = &msg
	}

	const q = `
UPDATE crawl_ranges
SET status = $4, saved_count = $5, last_error = $6, finished_at = now(), node_id = NULL
WHERE id = $1 AND attempts = $2 AND node_id = $3 AND status = 'running'`

	tag, err := s.pool.Exec(ctx, q, r.ID, r.Attempts, r.NodeID, string(status), saved, detail)
	if err != nil {
		return fmt.Errorf("구간 완료 처리에 실패했습니다: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: 구간 %d는 이미 회수되었습니다", domain.ErrLeaseConflict, r.ID)
	}
	return nil
}

func (s *Store) InitCatchupState(ctx context.Context, site, scope string, latestID int64) error {
	const q = `
INSERT INTO crawl_states (source_site, scope_key, high_watermark_id, backfill_before_id)
VALUES ($1, $2, $3, $3)
ON CONFLICT (source_site, scope_key) DO UPDATE SET
    backfill_before_id = COALESCE(crawl_states.backfill_before_id, EXCLUDED.backfill_before_id),
    high_watermark_id  = GREATEST(COALESCE(crawl_states.high_watermark_id, 0), EXCLUDED.high_watermark_id),
    updated_at         = now()`

	_, err := s.pool.Exec(ctx, q, site, scope, latestID)
	return err
}

func (s *Store) HighWatermark(ctx context.Context, site, scope string) (int64, error) {
	var wm *int64
	err := s.pool.QueryRow(ctx,
		`SELECT high_watermark_id FROM crawl_states WHERE source_site = $1 AND scope_key = $2`,
		site, scope).Scan(&wm)
	if err != nil {
		if isNoRows(err) {
			return 0, nil
		}
		return 0, err
	}
	if wm == nil {
		return 0, nil
	}
	return *wm, nil
}

func (s *Store) AdvanceWatermark(ctx context.Context, site, scope string, id int64) error {
	_, err := s.pool.Exec(ctx, `
UPDATE crawl_states
SET high_watermark_id = GREATEST(COALESCE(high_watermark_id, 0), $3), updated_at = now()
WHERE source_site = $1 AND scope_key = $2`, site, scope, id)
	return err
}

func (s *Store) EnqueueRetries(ctx context.Context, site, scope string, postIDs []int64, delay time.Duration) error {
	if len(postIDs) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, id := range postIDs {
		batch.Queue(`
INSERT INTO crawl_post_retries (source_site, scope_key, source_post_id, status, ready_at)
VALUES ($1, $2, $3, 'pending', now() + $4::interval)
ON CONFLICT (source_site, scope_key, source_post_id) DO UPDATE SET
    status   = 'pending',
    ready_at = EXCLUDED.ready_at
WHERE crawl_post_retries.status <> 'succeeded'`, site, scope, id, delay)
	}
	return s.pool.SendBatch(ctx, batch).Close()
}

func (s *Store) LeaseRetries(ctx context.Context, site, scope, nodeID string, limit int) ([]domain.PostRetry, error) {
	const q = `
WITH candidate AS (
    SELECT id
    FROM crawl_post_retries
    WHERE source_site = $1
      AND scope_key = $2
      AND attempts < $5
      AND ((status = 'pending' AND ready_at <= now())
           OR (status = 'running' AND leased_at < now() - $6::interval))
    ORDER BY ready_at
    FOR UPDATE SKIP LOCKED
    LIMIT $4
)
UPDATE crawl_post_retries r
SET status = 'running', attempts = r.attempts + 1, node_id = $3, leased_at = now()
FROM candidate c
WHERE r.id = c.id
RETURNING r.id, r.source_post_id, r.attempts`

	rows, err := s.pool.Query(ctx, q, site, scope, nodeID, limit, maxAttempts, staleLease)
	if err != nil {
		return nil, fmt.Errorf("재시도 임대에 실패했습니다: %w", err)
	}
	defer rows.Close()

	var out []domain.PostRetry
	for rows.Next() {
		item := domain.PostRetry{SourceSite: site, ScopeKey: scope}
		if err := rows.Scan(&item.ID, &item.SourcePostID, &item.Attempts); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) FinishRetry(ctx context.Context, item domain.PostRetry, status domain.RetryStatus, cause error) error {
	var detail *string
	if cause != nil {
		msg := cause.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		detail = &msg
	}

	const q = `
UPDATE crawl_post_retries
SET status = $3, last_error = $4, node_id = NULL
WHERE id = $1 AND attempts = $2 AND status = 'running'`

	_, err := s.pool.Exec(ctx, q, item.ID, item.Attempts, string(status), detail)
	return err
}

func (s *Store) RescheduleRetry(ctx context.Context, item domain.PostRetry, delay time.Duration, cause error) error {
	var detail *string
	if cause != nil {
		msg := cause.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		detail = &msg
	}

	const q = `
UPDATE crawl_post_retries
SET status = 'pending', ready_at = now() + $3::interval, last_error = $4, node_id = NULL
WHERE id = $1 AND attempts = $2 AND status = 'running'`

	_, err := s.pool.Exec(ctx, q, item.ID, item.Attempts, delay, detail)
	return err
}
