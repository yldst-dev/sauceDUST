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
      -- 하한 위쪽이 조금이라도 남아 있으면 다시 씁니다. 걸치는 구간은
      -- 아래에서 둘로 쪼개므로 여기서 통째로 빼면 안 됩니다. 빼면
      -- 하한 위 몫까지 같이 사라집니다.
      AND upper_id >= $7
      AND (
           -- 실패한 구간은 쉬는 시간이 지나야 다시 잡습니다. 쉬지 않고
           -- 다시 잡으면 임대할 때마다 시도 횟수가 올라, 상대가 몇 시간
           -- 멈춘 사이에 백로그 전체가 다섯 번을 채우고 죽습니다.
           (status = 'failed' AND ready_at <= now())
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
		req.NodeID, staleLease, maxAttempts, req.FloorID).
		Scan(&out.ID, &out.LowerID, &out.UpperID, &out.Attempts)
	if err != nil {
		if isNoRows(err) {
			return nil, domain.ErrNoWork
		}
		return nil, fmt.Errorf("구간 재사용에 실패했습니다: %w", err)
	}

	if out.LowerID < req.FloorID {
		if err := s.splitAtFloor(ctx, &out, req); err != nil {
			return nil, err
		}
	}

	out.SourceSite = req.SourceSite
	out.ScopeKey = req.ScopeKey
	out.Direction = domain.DirectionBackfill
	out.NodeID = req.NodeID
	return &out, nil
}

// splitAtFloor는 하한을 가로지르는 구간을 둘로 나눕니다.
//
// 실패한 구간 901~1000이 있는데 하한을 950으로 올린 상황입니다. 통째로
// 빼면 950~1000까지 같이 사라집니다. backfill_before_id는 이미 901이라
// 새로 자를 수도 없어 그 몫이 영영 안 모입니다.
//
// 그래서 지금 임대하는 쪽은 하한 위로 줄이고, 하한 아래 몫은 따로 남겨
// 둡니다. 남긴 것은 나중에 하한을 낮추면 다시 잡힙니다.
//
// 부르는 쪽이 이미 그 행을 running으로 잡아 두었으므로, 여기서 두 갈래를
// 한 트랜잭션으로 처리해 중간에 끊겨도 반쪽만 남지 않게 합니다.
func (s *Store) splitAtFloor(ctx context.Context, r *domain.CrawlRange, req domain.LeaseRequest) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("구간을 나누지 못했습니다: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	below := req.FloorID - 1
	const keep = `
INSERT INTO crawl_ranges (source_site, scope_key, direction, lower_id, upper_id,
                          status, attempts, node_id, leased_at)
VALUES ($1, $2, $3, $4, $5, 'failed', 0, NULL, NULL)
ON CONFLICT (source_site, scope_key, direction, lower_id, upper_id) DO NOTHING`
	if _, err := tx.Exec(ctx, keep, req.SourceSite, req.ScopeKey,
		string(domain.DirectionBackfill), r.LowerID, below); err != nil {
		return fmt.Errorf("하한 아래 몫을 남기지 못했습니다: %w", err)
	}

	const shrink = `
UPDATE crawl_ranges SET lower_id = $2
WHERE id = $1 AND status = 'running'`
	tag, err := tx.Exec(ctx, shrink, r.ID, req.FloorID)
	if err != nil {
		return fmt.Errorf("구간을 줄이지 못했습니다: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: 구간 %d는 이미 회수되었습니다", domain.ErrLeaseConflict, r.ID)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("구간 나누기를 마치지 못했습니다: %w", err)
	}
	r.LowerID = req.FloorID
	return nil
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

	// 실패는 시도 횟수에 따라 점점 더 오래 쉬었다가 다시 잡습니다.
	// 상대가 멈춰 있는데 쉬지 않고 다시 잡으면 몇 초 만에 다섯 번을
	// 채우고, 그 구간은 상대가 돌아와도 영영 빠집니다.
	wait := time.Duration(0)
	if status == domain.RangeFailed {
		wait = retryBackoff(r.Attempts)
	}

	const q = `
UPDATE crawl_ranges
SET status = $4, saved_count = $5, last_error = $6, finished_at = now(), node_id = NULL,
    ready_at = now() + $7::interval
WHERE id = $1 AND attempts = $2 AND node_id = $3 AND status = 'running'`

	tag, err := s.pool.Exec(ctx, q, r.ID, r.Attempts, r.NodeID, string(status), saved, detail, wait)
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

// LeaseRetries는 실패한 게시물을 다시 시도하려고 꺼내 옵니다.
//
// floor 아래 게시물은 꺼내지 않습니다. 하한을 올리기 전에 실패해 큐에 남은
// 것들이 계속 다시 색인되면 하한을 둔 뜻이 없어집니다.
func (s *Store) LeaseRetries(ctx context.Context, site, scope, nodeID string, limit int, floor int64) ([]domain.PostRetry, error) {
	const q = `
WITH candidate AS (
    SELECT id
    FROM crawl_post_retries
    WHERE source_site = $1
      AND scope_key = $2
      AND attempts < $5
      AND source_post_id >= $7
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

	rows, err := s.pool.Query(ctx, q, site, scope, nodeID, limit, maxAttempts, staleLease, floor)
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

// retryBackoff는 몇 번째 실패인지에 따라 얼마나 쉴지 정합니다.
// 1분에서 시작해 두 배씩 늘리고 30분에서 멈춥니다.
func retryBackoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 6 {
		attempts = 6
	}
	wait := time.Duration(1<<(attempts-1)) * time.Minute
	if wait > 30*time.Minute {
		wait = 30 * time.Minute
	}
	return wait
}

// RenewRange는 아직 일하고 있다고 알립니다.
//
// 이것이 없으면 살아 있는 노드의 구간을 다른 노드가 빼앗습니다. 기본값인
// 구간 10,000개를 초당 5회로 받으면 33분이 걸리는데 임대 만료는 30분이라,
// 정상 처리만으로도 만료를 넘깁니다. 빼앗기면 같은 구간을 둘이 받아 상대
// 사이트의 속도 한도를 두 배로 쓰고, 느린 노드는 실패한 적도 없이
// 시도 횟수를 채워 그 대역이 영영 빠집니다.
//
// 펜싱 토큰을 함께 봅니다. 이미 회수된 뒤라면 갱신하지 않습니다.
func (s *Store) RenewRange(ctx context.Context, r *domain.CrawlRange) error {
	const q = `
UPDATE crawl_ranges SET leased_at = now()
WHERE id = $1 AND attempts = $2 AND node_id = $3 AND status = 'running'`

	tag, err := s.pool.Exec(ctx, q, r.ID, r.Attempts, r.NodeID)
	if err != nil {
		return fmt.Errorf("임대 갱신에 실패했습니다: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: 구간 %d는 이미 회수되었습니다", domain.ErrLeaseConflict, r.ID)
	}
	return nil
}

// ReleaseRange는 임대를 되돌립니다. 시도 횟수도 함께 돌려놓습니다.
//
// 색인이 차서 못 넣은 것은 그 구간의 잘못이 아닙니다. 실패로 적으면
// 시도 횟수를 깎아 먹고, 다섯 번을 채우면 나중에 자리가 생겨도 다시
// 잡히지 않아 그 구간이 영영 빠집니다.
func (s *Store) ReleaseRange(ctx context.Context, r *domain.CrawlRange) error {
	const q = `
UPDATE crawl_ranges
SET status = 'failed', attempts = GREATEST(attempts - 1, 0), node_id = NULL,
    last_error = NULL, finished_at = NULL, ready_at = now()
WHERE id = $1 AND attempts = $2 AND node_id = $3 AND status = 'running'`

	tag, err := s.pool.Exec(ctx, q, r.ID, r.Attempts, r.NodeID)
	if err != nil {
		return fmt.Errorf("구간 반납에 실패했습니다: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: 구간 %d는 이미 회수되었습니다", domain.ErrLeaseConflict, r.ID)
	}
	return nil
}

// ReleaseRetry는 재시도 항목을 시도 횟수를 쓰지 않고 되돌립니다.
func (s *Store) ReleaseRetry(ctx context.Context, item domain.PostRetry) error {
	const q = `
UPDATE crawl_post_retries
SET status = 'pending', attempts = GREATEST(attempts - 1, 0), node_id = NULL,
    ready_at = now()
WHERE id = $1 AND attempts = $2 AND status = 'running'`

	// 펜싱 토큰을 함께 봅니다. 없으면 이미 다른 노드가 가져간 항목을
	// 되돌려 그쪽 임대를 무효로 만듭니다.
	if _, err := s.pool.Exec(ctx, q, item.ID, item.Attempts); err != nil {
		return fmt.Errorf("재시도 반납에 실패했습니다: %w", err)
	}
	return nil
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

	tag, err := s.pool.Exec(ctx, q, item.ID, item.Attempts, string(status), detail)
	if err == nil && tag.RowsAffected() == 0 {
		// 이미 다른 노드가 가져갔습니다. 조용히 지나가면 그 노드의
		// 결과를 이쪽 결과로 덮어씁니다.
		return fmt.Errorf("%w: 재시도 %d는 이미 회수되었습니다", domain.ErrLeaseConflict, item.ID)
	}
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
