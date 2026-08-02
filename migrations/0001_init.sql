CREATE TABLE IF NOT EXISTS images (
    id BIGSERIAL PRIMARY KEY,
    source_site TEXT NOT NULL,
    source_post_id TEXT NOT NULL,
    source_url TEXT,
    canonical_url TEXT,
    file_url TEXT,
    preview_url TEXT,
    md5 TEXT,
    phash TEXT,
    dhash TEXT,
    width INTEGER,
    height INTEGER,
    rating TEXT,
    score INTEGER,
    tags TEXT[] NOT NULL DEFAULT '{}',
    artist_tags TEXT[] NOT NULL DEFAULT '{}',
    vector_indexed_at TIMESTAMPTZ,
    indexed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(source_site, source_post_id)
);

ALTER TABLE images ADD COLUMN IF NOT EXISTS artist_tags TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE images ADD COLUMN IF NOT EXISTS vector_indexed_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS index_jobs (
    id BIGSERIAL PRIMARY KEY,
    source_site TEXT NOT NULL,
    status TEXT NOT NULL,
    total INTEGER DEFAULT 0,
    succeeded INTEGER DEFAULT 0,
    failed INTEGER DEFAULT 0,
    started_at TIMESTAMPTZ DEFAULT now(),
    finished_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS index_failures (
    id BIGSERIAL PRIMARY KEY,
    job_id BIGINT REFERENCES index_jobs(id),
    source_site TEXT NOT NULL,
    source_post_id TEXT,
    url TEXT,
    reason TEXT,
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS query_embedding_cache (
    sha256 TEXT PRIMARY KEY,
    vector REAL[] NOT NULL,
    vector_size INTEGER NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now(),
    last_used_at TIMESTAMPTZ DEFAULT now(),
    hits BIGINT NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS crawl_states (
    source_site TEXT NOT NULL,
    scope_key TEXT NOT NULL,
    high_watermark_id BIGINT,
    backfill_before_id BIGINT,
    updated_at TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (source_site, scope_key)
);

CREATE TABLE IF NOT EXISTS crawl_ranges (
    id BIGSERIAL PRIMARY KEY,
    source_site TEXT NOT NULL,
    scope_key TEXT NOT NULL,
    direction TEXT NOT NULL,
    lower_id BIGINT NOT NULL,
    upper_id BIGINT NOT NULL,
    status TEXT NOT NULL,
    worker_id TEXT,
    attempts INTEGER NOT NULL DEFAULT 1,
    leased_at TIMESTAMPTZ DEFAULT now(),
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE(source_site, scope_key, direction, lower_id, upper_id)
);

CREATE TABLE IF NOT EXISTS crawl_direction_stats (
    source_site TEXT NOT NULL,
    scope_key TEXT NOT NULL,
    direction TEXT NOT NULL,
    saved_count BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ DEFAULT now(),
    PRIMARY KEY (source_site, scope_key, direction)
);

CREATE TABLE IF NOT EXISTS crawl_post_retries (
    id BIGSERIAL PRIMARY KEY,
    source_site TEXT NOT NULL,
    scope_key TEXT NOT NULL,
    source_post_id TEXT NOT NULL,
    direction TEXT NOT NULL,
    url TEXT,
    reason TEXT,
    status TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    leased_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT now(),
    updated_at TIMESTAMPTZ DEFAULT now(),
    UNIQUE(source_site, scope_key, source_post_id)
);

CREATE INDEX IF NOT EXISTS idx_images_source ON images(source_site, source_post_id);
CREATE INDEX IF NOT EXISTS idx_images_md5 ON images(md5);
CREATE INDEX IF NOT EXISTS idx_images_rating ON images(rating);
CREATE INDEX IF NOT EXISTS idx_query_embedding_cache_last_used ON query_embedding_cache(last_used_at);
CREATE INDEX IF NOT EXISTS idx_crawl_states_updated_at ON crawl_states(updated_at);
CREATE INDEX IF NOT EXISTS idx_crawl_ranges_status ON crawl_ranges(source_site, scope_key, direction, status);
CREATE INDEX IF NOT EXISTS idx_crawl_ranges_retry_ready ON crawl_ranges(source_site, scope_key, direction, status, attempts, lower_id DESC);
CREATE INDEX IF NOT EXISTS idx_crawl_ranges_running_timeout ON crawl_ranges(source_site, scope_key, direction, leased_at) WHERE status = 'running';
CREATE INDEX IF NOT EXISTS idx_crawl_post_retries_ready ON crawl_post_retries(source_site, scope_key, status, next_attempt_at, attempts);
CREATE INDEX IF NOT EXISTS idx_crawl_post_retries_ordered_ready ON crawl_post_retries(source_site, scope_key, status, attempts, next_attempt_at, id);
CREATE INDEX IF NOT EXISTS idx_crawl_post_retries_running_timeout ON crawl_post_retries(source_site, scope_key, leased_at) WHERE status = 'running';
