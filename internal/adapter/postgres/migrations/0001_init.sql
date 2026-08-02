CREATE TABLE IF NOT EXISTS nodes (
    id           TEXT PRIMARY KEY,
    role         TEXT        NOT NULL,
    hostname     TEXT        NOT NULL DEFAULT '',
    platform     TEXT        NOT NULL DEFAULT '',
    version      TEXT        NOT NULL DEFAULT '',
    device       TEXT        NOT NULL DEFAULT 'unknown',
    net_mode     TEXT        NOT NULL DEFAULT 'unknown',
    concurrency  INTEGER     NOT NULL DEFAULT 0,
    status       TEXT        NOT NULL DEFAULT 'online',
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    stopped_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_nodes_live
    ON nodes (heartbeat_at DESC) WHERE status = 'online';

CREATE TABLE IF NOT EXISTS embedding_models (
    id          TEXT PRIMARY KEY,
    kind        TEXT        NOT NULL,
    backend     TEXT        NOT NULL,
    checkpoint  TEXT        NOT NULL DEFAULT '',
    vector_size INTEGER     NOT NULL,
    distance    TEXT        NOT NULL DEFAULT 'cosine',
    collection  TEXT        NOT NULL,
    input_size  INTEGER     NOT NULL DEFAULT 224,
    active      BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT embedding_models_kind_check CHECK (kind IN ('copy', 'semantic'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_embedding_models_active_kind
    ON embedding_models (kind) WHERE active;

CREATE TABLE IF NOT EXISTS node_models (
    node_id     TEXT        NOT NULL REFERENCES nodes (id) ON DELETE CASCADE,
    model_id    TEXT        NOT NULL,
    vector_size INTEGER     NOT NULL,
    device      TEXT        NOT NULL DEFAULT 'unknown',
    reported_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (node_id, model_id)
);

CREATE TABLE IF NOT EXISTS images (
    id             BIGSERIAL PRIMARY KEY,
    source_site    TEXT        NOT NULL,
    source_post_id BIGINT      NOT NULL,
    source_url     TEXT,
    canonical_url  TEXT,
    file_url       TEXT,
    preview_url    TEXT,
    md5            TEXT,
    phash          TEXT,
    dhash          TEXT,
    width          INTEGER,
    height         INTEGER,
    file_size      BIGINT,
    rating         TEXT,
    score          INTEGER,
    tags           TEXT[]      NOT NULL DEFAULT '{}',
    artist_tags    TEXT[]      NOT NULL DEFAULT '{}',
    thumb_path     TEXT,
    indexed_by     TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_site, source_post_id)
);

CREATE INDEX IF NOT EXISTS idx_images_md5 ON images (md5);
CREATE INDEX IF NOT EXISTS idx_images_phash ON images (phash);
CREATE INDEX IF NOT EXISTS idx_images_rating ON images (rating);
CREATE INDEX IF NOT EXISTS idx_images_no_thumb ON images (id) WHERE thumb_path IS NULL;

-- reembed가 훑는 대상입니다. 모델을 바꾸면 축소본이 있는 이미지를 전부
-- 다시 계산하는데, 조건 없이 훑으면 1천만 행을 한 줄씩 봐야 합니다.
-- 조건을 그대로 담은 부분 색인이라야 PostgreSQL이 이 색인을 씁니다.
CREATE INDEX IF NOT EXISTS idx_images_with_thumb
    ON images (id) WHERE thumb_path IS NOT NULL AND thumb_path <> '';

CREATE TABLE IF NOT EXISTS image_vectors (
    image_id   BIGINT      NOT NULL REFERENCES images (id) ON DELETE CASCADE,
    model_id   TEXT        NOT NULL REFERENCES embedding_models (id),
    vector     BYTEA       NOT NULL,
    indexed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (image_id, model_id)
);

CREATE INDEX IF NOT EXISTS idx_image_vectors_pending
    ON image_vectors (model_id, image_id) WHERE indexed_at IS NULL;

CREATE TABLE IF NOT EXISTS crawl_states (
    source_site        TEXT        NOT NULL,
    scope_key          TEXT        NOT NULL,
    high_watermark_id  BIGINT,
    backfill_before_id BIGINT,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (source_site, scope_key)
);

CREATE TABLE IF NOT EXISTS crawl_ranges (
    id          BIGSERIAL PRIMARY KEY,
    source_site TEXT        NOT NULL,
    scope_key   TEXT        NOT NULL,
    direction   TEXT        NOT NULL,
    lower_id    BIGINT      NOT NULL,
    upper_id    BIGINT      NOT NULL,
    status      TEXT        NOT NULL DEFAULT 'running',
    attempts    INTEGER     NOT NULL DEFAULT 0,
    node_id     TEXT,
    leased_at   TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    saved_count INTEGER     NOT NULL DEFAULT 0,
    last_error  TEXT,
    UNIQUE (source_site, scope_key, direction, lower_id, upper_id)
);

CREATE INDEX IF NOT EXISTS idx_crawl_ranges_reusable
    ON crawl_ranges (source_site, scope_key, direction, status, attempts);

CREATE INDEX IF NOT EXISTS idx_crawl_ranges_stale
    ON crawl_ranges (leased_at) WHERE status = 'running';

CREATE INDEX IF NOT EXISTS idx_crawl_ranges_node
    ON crawl_ranges (node_id) WHERE status = 'running';

CREATE TABLE IF NOT EXISTS crawl_post_retries (
    id             BIGSERIAL PRIMARY KEY,
    source_site    TEXT        NOT NULL,
    scope_key      TEXT        NOT NULL,
    source_post_id BIGINT      NOT NULL,
    status         TEXT        NOT NULL DEFAULT 'pending',
    attempts       INTEGER     NOT NULL DEFAULT 0,
    node_id        TEXT,
    ready_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    leased_at      TIMESTAMPTZ,
    last_error     TEXT,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (source_site, scope_key, source_post_id)
);

CREATE INDEX IF NOT EXISTS idx_crawl_post_retries_ready
    ON crawl_post_retries (ready_at) WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS idx_crawl_post_retries_stale
    ON crawl_post_retries (leased_at) WHERE status = 'running';

CREATE INDEX IF NOT EXISTS idx_crawl_post_retries_node
    ON crawl_post_retries (node_id) WHERE status = 'running';

CREATE TABLE IF NOT EXISTS node_metrics (
    node_id     TEXT        NOT NULL,
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    cpu_pct     REAL,
    mem_mb      REAL,
    device_pct  REAL,
    concurrency INTEGER,
    net_mode    TEXT,
    downloaded  BIGINT      NOT NULL DEFAULT 0,
    embedded    BIGINT      NOT NULL DEFAULT 0,
    saved       BIGINT      NOT NULL DEFAULT 0,
    failed      BIGINT      NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, observed_at)
);

CREATE INDEX IF NOT EXISTS idx_node_metrics_recent ON node_metrics (observed_at DESC);

CREATE TABLE IF NOT EXISTS net_probes (
    node_id    TEXT        NOT NULL,
    host       TEXT        NOT NULL,
    mode       TEXT        NOT NULL,
    ok         BOOLEAN     NOT NULL,
    latency_ms INTEGER,
    checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    detail     TEXT,
    PRIMARY KEY (node_id, host, mode)
);

CREATE TABLE IF NOT EXISTS query_cache (
    sha256       TEXT        NOT NULL,
    model_id     TEXT        NOT NULL,
    vector       BYTEA       NOT NULL,
    hits         BIGINT      NOT NULL DEFAULT 1,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (sha256, model_id)
);

CREATE INDEX IF NOT EXISTS idx_query_cache_lru ON query_cache (last_used_at);
