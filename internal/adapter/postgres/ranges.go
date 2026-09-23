package postgres

import (
	"context"
	"fmt"
	"time"

	"saucedust/internal/domain"
)

// 수집 구간은 시도 횟수가 상한을 넘으면 더 이상 배정되지 않습니다.
// 일시적인 통신 장애로 상한을 소진하면 그 ID 구간의 이미지는 영영 들어오지
// 않습니다. 오래 돌릴수록 이런 구멍이 조금씩 쌓이므로 되살릴 방법이 필요합니다.

// RangeStats는 구간 상태별 집계와 남은 구멍을 알려줍니다.
func (s *Store) RangeStats(ctx context.Context, site, scope string) (domain.RangeStats, error) {
	const q = `
SELECT
    count(*)                                             AS total,
    count(*) FILTER (WHERE status = 'completed')         AS completed,
    count(*) FILTER (WHERE status = 'running')           AS running,
    count(*) FILTER (WHERE status = 'empty')             AS empty,
    count(*) FILTER (WHERE status = 'failed')            AS failed,
    count(*) FILTER (WHERE status = 'failed' AND attempts >= $3) AS exhausted,
    COALESCE(sum(saved_count), 0)                        AS saved,
    COALESCE(sum(upper_id - lower_id + 1)
             FILTER (WHERE status = 'failed'), 0)        AS missing_ids
FROM crawl_ranges
WHERE source_site = $1 AND scope_key = $2`

	var out domain.RangeStats
	err := s.pool.QueryRow(ctx, q, site, scope, maxAttempts).Scan(
		&out.Total, &out.Completed, &out.Running, &out.Empty,
		&out.Failed, &out.Exhausted, &out.Saved, &out.MissingIDs)
	if err != nil {
		return out, fmt.Errorf("구간 통계 조회에 실패했습니다: %w", err)
	}
	return out, nil
}

// ExhaustedRanges는 시도 상한을 넘겨 더 이상 배정되지 않는 구간을 돌려줍니다.
func (s *Store) ExhaustedRanges(ctx context.Context, site, scope string, limit int) ([]domain.CrawlRange, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.pool.Query(ctx, `
SELECT id, lower_id, upper_id, attempts, COALESCE(last_error, '')
FROM crawl_ranges
WHERE source_site = $1 AND scope_key = $2
  AND status = 'failed' AND attempts >= $3
ORDER BY upper_id DESC
LIMIT $4`, site, scope, maxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.CrawlRange
	for rows.Next() {
		item := domain.CrawlRange{
			SourceSite: site, ScopeKey: scope, Direction: domain.DirectionBackfill,
		}
		if err := rows.Scan(&item.ID, &item.LowerID, &item.UpperID,
			&item.Attempts, &item.LastError); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// ResetExhaustedRanges는 시도 횟수를 0으로 돌려 다시 배정되게 합니다.
// 통신 장애가 지나간 뒤 구멍을 메우는 데 씁니다.
func (s *Store) ResetExhaustedRanges(ctx context.Context, site, scope string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
UPDATE crawl_ranges
SET attempts = 0, last_error = NULL, node_id = NULL
WHERE source_site = $1 AND scope_key = $2
  AND status = 'failed' AND attempts >= $3`, site, scope, maxAttempts)
	if err != nil {
		return 0, fmt.Errorf("구간 초기화에 실패했습니다: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ResetRange는 구간 하나를 지정해 다시 배정되게 합니다.
func (s *Store) ResetRange(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `
UPDATE crawl_ranges
SET status = 'failed', attempts = 0, last_error = NULL, node_id = NULL
WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: 구간 %d", domain.ErrNotFound, id)
	}
	return nil
}

// ResetRetryQueue는 죽은 재시도 항목을 다시 대기로 돌립니다.
func (s *Store) RetryFailedRanges(ctx context.Context, site, scope string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
UPDATE crawl_ranges
SET attempts = 0, ready_at = now(), last_error = NULL, node_id = NULL, leased_at = NULL
WHERE source_site = $1 AND scope_key = $2 AND status = 'failed'`, site, scope)
	if err != nil {
		return 0, fmt.Errorf("실패 구간을 재시도하지 못했습니다: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) RetryPendingQueue(ctx context.Context, site, scope string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
UPDATE crawl_post_retries
SET status = 'pending',
    attempts = CASE WHEN status = 'dead' OR attempts >= $3 THEN 0 ELSE attempts END,
    ready_at = now(),
    node_id = NULL,
    leased_at = NULL,
    last_error = NULL
WHERE source_site = $1 AND scope_key = $2
  AND status IN ('pending', 'dead')`, site, scope, maxAttempts)
	if err != nil {
		return 0, fmt.Errorf("재시도 대기를 돌리지 못했습니다: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s *Store) ResetRetryQueue(ctx context.Context, site, scope string) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
UPDATE crawl_post_retries
SET status = 'pending', attempts = 0, ready_at = now(), node_id = NULL, last_error = NULL
WHERE source_site = $1 AND scope_key = $2
  AND (status = 'dead' OR attempts >= $3)`, site, scope, maxAttempts)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// CoverageGaps는 아직 구간이 만들어지지 않은 ID 대역을 찾습니다.
//
// 구간은 진행점에서 아래로 잘라 나가므로 원칙적으로 빈틈이 없어야 하지만,
// 설정을 바꾸거나 수동으로 손대면 틈이 생길 수 있습니다. 수집이 정말로
// 빠짐없이 됐는지 확인하는 최종 점검입니다.
func (s *Store) CoverageGaps(ctx context.Context, site, scope string, limit int) ([]domain.IDGap, error) {
	if limit <= 0 {
		limit = 20
	}

	const q = `
WITH ordered AS (
    SELECT lower_id, upper_id,
           lag(lower_id) OVER (ORDER BY upper_id DESC) AS prev_lower
    FROM crawl_ranges
    WHERE source_site = $1 AND scope_key = $2
)
SELECT upper_id + 1 AS gap_from, prev_lower - 1 AS gap_to
FROM ordered
WHERE prev_lower IS NOT NULL AND prev_lower > upper_id + 1
ORDER BY gap_from DESC
LIMIT $3`

	rows, err := s.pool.Query(ctx, q, site, scope, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.IDGap
	for rows.Next() {
		var gap domain.IDGap
		if err := rows.Scan(&gap.From, &gap.To); err != nil {
			return nil, err
		}
		out = append(out, gap)
	}
	return out, rows.Err()
}

// PlanGapRanges는 빈 대역을 배정 가능한 구간으로 만들어 넣습니다.
// 이미 있는 구간과 겹치면 아무 일도 하지 않습니다.
func (s *Store) PlanGapRanges(ctx context.Context, site, scope string, gaps []domain.IDGap, rangeSize int64) (int64, error) {
	if len(gaps) == 0 {
		return 0, nil
	}
	if rangeSize < 1 {
		rangeSize = 10_000
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var created int64
	for _, gap := range gaps {
		for upper := gap.To; upper >= gap.From; upper -= rangeSize {
			lower := upper - rangeSize + 1
			if lower < gap.From {
				lower = gap.From
			}

			tag, err := tx.Exec(ctx, `
INSERT INTO crawl_ranges (source_site, scope_key, direction, lower_id, upper_id,
                          status, attempts)
VALUES ($1, $2, $3, $4, $5, 'failed', 0)
ON CONFLICT (source_site, scope_key, direction, lower_id, upper_id) DO NOTHING`,
				site, scope, string(domain.DirectionBackfill), lower, upper)
			if err != nil {
				return 0, fmt.Errorf("빈 구간 생성에 실패했습니다: %w", err)
			}
			created += tag.RowsAffected()

			if lower == gap.From {
				break
			}
		}
	}
	return created, tx.Commit(ctx)
}

// StaleRunningRanges는 오래 붙잡혀 있는 구간을 알려줍니다.
// 노드가 살아 있는데도 진행이 없으면 여기에 나타납니다.
func (s *Store) StaleRunningRanges(ctx context.Context, site, scope string, olderThan time.Duration) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM crawl_ranges
WHERE source_site = $1 AND scope_key = $2 AND status = 'running'
  AND leased_at < now() - $3::interval`, site, scope, olderThan).Scan(&n)
	return n, err
}
