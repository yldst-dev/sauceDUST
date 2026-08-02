package postgres

import (
	"context"
	"time"

	"saucedust/internal/domain"
)

// FleetSummary는 노드별 최근 처리량을 냅니다.
// 카운터는 노드가 켜진 뒤로 계속 쌓이는 값이므로, 구간의 처음과 끝을 빼서
// 그 구간에서 실제로 몇 건을 했는지 구합니다.
func (s *Store) FleetSummary(ctx context.Context, window time.Duration) (domain.FleetSummary, error) {
	const q = `
WITH bounds AS (
    SELECT node_id,
           min(observed_at) AS first_at,
           max(observed_at) AS last_at
    FROM node_metrics
    WHERE observed_at > now() - $1::interval
    GROUP BY node_id
),
edges AS (
    SELECT b.node_id,
           extract(epoch FROM (b.last_at - b.first_at)) AS span,
           first.downloaded AS d0, first.embedded AS e0,
           first.saved AS s0, first.failed AS f0,
           last.downloaded AS d1, last.embedded AS e1,
           last.saved AS s1, last.failed AS f1,
           last.cpu_pct, last.mem_mb
    FROM bounds b
    JOIN node_metrics first ON first.node_id = b.node_id AND first.observed_at = b.first_at
    JOIN node_metrics last  ON last.node_id  = b.node_id AND last.observed_at  = b.last_at
)
SELECT node_id,
       GREATEST(d1 - d0, 0),
       GREATEST(e1 - e0, 0),
       GREATEST(s1 - s0, 0),
       GREATEST(f1 - f0, 0),
       CASE WHEN span > 0 THEN GREATEST(s1 - s0, 0) / span ELSE 0 END,
       COALESCE(cpu_pct, 0),
       COALESCE(mem_mb, 0)
FROM edges`

	rows, err := s.pool.Query(ctx, q, window)
	if err != nil {
		return domain.FleetSummary{}, err
	}
	defer rows.Close()

	out := domain.FleetSummary{Nodes: map[string]domain.NodeThroughput{}}
	for rows.Next() {
		var t domain.NodeThroughput
		if err := rows.Scan(&t.NodeID, &t.Downloaded, &t.Embedded, &t.Saved,
			&t.Failed, &t.PerSecond, &t.CPUPct, &t.MemMB); err != nil {
			return domain.FleetSummary{}, err
		}
		out.Nodes[t.NodeID] = t
		out.TotalPerSecond += t.PerSecond
	}
	return out, rows.Err()
}

func (s *Store) CrawlSummary(ctx context.Context, site, scope string) (domain.CrawlSummary, error) {
	out := domain.CrawlSummary{VectorsByModel: map[string]int64{}}

	const rangeQ = `
SELECT
    count(*) FILTER (WHERE status = 'completed'),
    count(*) FILTER (WHERE status = 'running'),
    count(*) FILTER (WHERE status = 'failed'),
    count(*) FILTER (WHERE status = 'empty')
FROM crawl_ranges
WHERE source_site = $1 AND scope_key = $2`

	err := s.pool.QueryRow(ctx, rangeQ, site, scope).Scan(
		&out.RangesCompleted, &out.RangesRunning, &out.RangesFailed, &out.RangesEmpty)
	if err != nil {
		return out, err
	}

	const retryQ = `
SELECT
    count(*) FILTER (WHERE status = 'pending'),
    count(*) FILTER (WHERE status = 'dead')
FROM crawl_post_retries
WHERE source_site = $1 AND scope_key = $2`

	if err := s.pool.QueryRow(ctx, retryQ, site, scope).Scan(&out.RetryPending, &out.RetryDead); err != nil {
		return out, err
	}

	var high, before *int64
	err = s.pool.QueryRow(ctx, `
SELECT high_watermark_id, backfill_before_id
FROM crawl_states WHERE source_site = $1 AND scope_key = $2`, site, scope).Scan(&high, &before)
	if err != nil && !isNoRows(err) {
		return out, err
	}
	if high != nil {
		out.HighWatermark = *high
	}
	if before != nil {
		out.BackfillBefore = *before
	}

	// 모델별 벡터 수는 2,000만 행을 훑어야 나옵니다. 대시보드가 몇 초마다
	// 부르는 값이라 그때마다 세면 그것만으로 디스크가 바쁩니다. 잠깐
	// 기억해 두고 씁니다. 어림치를 쓰지 않는 이유는 방금 넣은 자료가
	// 통계에 안 잡혀 0으로 보이기 때문입니다.
	if cached, ok := s.cachedVectorCounts(); ok {
		out.VectorsByModel = cached
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
SELECT model_id, count(*) FROM image_vectors GROUP BY model_id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			modelID string
			count   int64
		)
		if err := rows.Scan(&modelID, &count); err != nil {
			return out, err
		}
		out.VectorsByModel[modelID] = count
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	s.rememberVectorCounts(out.VectorsByModel)
	return out, nil
}

// vectorCountTTL은 모델별 벡터 수를 기억해 두는 시간입니다.
//
// 대시보드가 몇 초마다 부르는데 셀 때마다 2,000만 행을 훑습니다. 몇 초
// 늦은 값을 보여 주는 것이 그때마다 디스크를 훑는 것보다 낫습니다.
const vectorCountTTL = 15 * time.Second

func (s *Store) cachedVectorCounts() (map[string]int64, bool) {
	s.vectorCount.mu.Lock()
	defer s.vectorCount.mu.Unlock()

	if s.vectorCount.at.IsZero() || time.Since(s.vectorCount.at) > vectorCountTTL {
		return nil, false
	}
	out := make(map[string]int64, len(s.vectorCount.byModel))
	for k, v := range s.vectorCount.byModel {
		out[k] = v
	}
	return out, true
}

func (s *Store) rememberVectorCounts(counts map[string]int64) {
	s.vectorCount.mu.Lock()
	defer s.vectorCount.mu.Unlock()

	s.vectorCount.byModel = make(map[string]int64, len(counts))
	for k, v := range counts {
		s.vectorCount.byModel[k] = v
	}
	s.vectorCount.at = time.Now()
}

// BestPaths는 이 노드에서 호스트별로 통했던 가장 빠른 경로를 돌려줍니다.
// 켤 때마다 막힌 경로부터 다시 더듬지 않기 위한 기억입니다.
// 오래된 기록은 회선 사정이 달라졌을 수 있으므로 쓰지 않습니다.
func (s *Store) BestPaths(ctx context.Context, nodeID string, maxAge time.Duration) (map[string]domain.NetMode, error) {
	const q = `
SELECT DISTINCT ON (host) host, mode
FROM net_probes
WHERE node_id = $1 AND ok AND checked_at > now() - $2::interval
ORDER BY host, latency_ms NULLS LAST`

	rows, err := s.pool.Query(ctx, q, nodeID, maxAge)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]domain.NetMode{}
	for rows.Next() {
		var host, mode string
		if err := rows.Scan(&host, &mode); err != nil {
			return nil, err
		}
		out[host] = domain.NetMode(mode)
	}
	return out, rows.Err()
}

// RecordProbe는 네트워크 경로 측정 결과를 남깁니다. 대시보드에서 어떤 경로가
// 어느 호스트에 통하는지 한눈에 보기 위한 기록입니다.
func (s *Store) RecordProbe(ctx context.Context, nodeID, host string, mode domain.NetMode, ok bool, latency time.Duration, detail string) error {
	const q = `
INSERT INTO net_probes (node_id, host, mode, ok, latency_ms, checked_at, detail)
VALUES ($1, $2, $3, $4, $5, now(), $6)
ON CONFLICT (node_id, host, mode) DO UPDATE SET
    ok         = EXCLUDED.ok,
    latency_ms = EXCLUDED.latency_ms,
    checked_at = now(),
    detail     = EXCLUDED.detail`

	_, err := s.pool.Exec(ctx, q, nodeID, host, string(mode), ok,
		int(latency.Milliseconds()), detail)
	return err
}
