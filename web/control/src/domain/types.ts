export type PageId = "dash" | "jobs" | "nodes" | "data" | "settings"

export type Stats = {
  images: number
  vectors?: Record<string, number>
  online_nodes: number
  total_nodes: number
  ranges_done: number
  ranges_active: number
  ranges_failed: number
  ranges_empty: number
  ranges_total: number
  ranges_exhausted: number
  retry_queue: number
  saved_per_sec: number
  high_watermark: number
  backfill_before: number
  source_site: string
  scope_key: string
}

export type NodeRow = {
  id: string
  role: string
  status: string
  hostname: string
  platform: string
  device: string
  net_mode: string
  concurrency: number
  version: string
  last_seen_sec: number
  saved: number
  failed: number
  per_second: number
  cpu_pct: number
  mem_mb: number
}

export type Jobs = {
  manage: boolean
  total: number
  completed: number
  running: number
  empty: number
  failed: number
  exhausted: number
  missing_ids: number
  stale_running: number
  retry_pending: number
  retry_dead: number
  failed_ranges: RangeItem[]
  gaps: Gap[]
}

export type RangeItem = {
  id: number
  lower: number
  upper: number
  count: number
  attempts: number
  error: string
}

export type Gap = { from: number; to: number; count: number }

export type ModelRow = {
  id: string
  kind: string
  backend: string
  vector_size: number
  collection: string
  input_size: number
}

export type SearchHit = {
  canonical_url: string
  source_post_id: number
  score: number
}

export type ImageView = {
  indexed: boolean
  canonical_url?: string
  source_post_id?: number
}

export type Settings = {
  backfill_floor: number
  max_indexed: number
  backfill_workers: number
  backfill_range_size: number
  concurrency: number
  min_concurrency: number
  max_concurrency: number
  adaptive: boolean
  rate_per_sec: number
  rate_burst: number
  poll_secs: number
  thumb_size: number
  thumb_quality: number
  thumb_dir: string
  index_tags: string
  user_agent: string
  danbooru_login: string
  telegram_allowed: string
  net_order: string
  fragment_parts: number
  fragment_delay_ms: number
  allow_private: boolean
  direct_fallback: boolean
  embed_batch_size: number
  embed_batch_timeout_ms: number
  heartbeat_secs: number
  node_timeout_secs: number
  control_bind: string
  control_token?: string
  danbooru_api_key?: string
  proxy_url?: string
  telegram_set?: boolean
  telegram_hint?: string
  job_running?: string
  job_last?: string
  job_error?: string
}

export type VerifyItem = { name: string; ok: string; note: string }

export type JobStatus = { running: string; last: string; error: string }
