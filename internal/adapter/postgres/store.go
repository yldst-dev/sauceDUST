// Package postgres는 PostgreSQL 저장소 어댑터입니다.
// 구간 임대와 노드 조정처럼 여러 노드가 다투는 부분을 여기서 처리합니다.
package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// maxPoolConns는 커넥션 수 상한입니다. PostgreSQL 기본 max_connections가
// 100이므로 이보다 훨씬 큰 값은 의미가 없고, int32를 넘기면 뒤집힙니다.
const maxPoolConns = 500

type Store struct {
	pool *pgxpool.Pool

	// vectorCount는 모델별 벡터 수를 잠깐 기억해 둡니다.
	// 세려면 2,000만 행을 훑어야 하는데 대시보드가 몇 초마다 부릅니다.
	vectorCount struct {
		mu      sync.Mutex
		byModel map[string]int64
		at      time.Time
	}
}

type Options struct {
	URL            string
	MaxConns       int
	AcquireTimeout time.Duration
	// Schema를 지정하면 그 스키마만 씁니다. 비우면 기본값을 씁니다.
	// 시험에서 패키지마다 따로 쓰려고 둔 것입니다. 같은 데이터베이스를 나눠 쓰면
	// 병렬로 돌 때 서로의 표를 지워 시험이 불안정해집니다.
	Schema string
}

func Open(ctx context.Context, opts Options) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(opts.URL)
	if err != nil {
		return nil, fmt.Errorf("DATABASE_URL을 해석하지 못했습니다: %w", err)
	}
	if opts.MaxConns > 0 {
		// pgx가 int32를 쓰므로 상한을 여기서 다시 확인합니다.
		// 설정에서 이미 묶지만, 이 함수만 보고도 안전함이 드러나야 합니다.
		conns := opts.MaxConns
		if conns > maxPoolConns {
			conns = maxPoolConns
		}
		cfg.MaxConns = int32(conns)
	}
	if opts.AcquireTimeout > 0 {
		cfg.ConnConfig.ConnectTimeout = opts.AcquireTimeout
	}
	if opts.Schema != "" {
		if err := validSchemaName(opts.Schema); err != nil {
			return nil, err
		}
		if err := createSchema(ctx, opts); err != nil {
			return nil, err
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = opts.Schema
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("PostgreSQL 연결에 실패했습니다: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("PostgreSQL 응답이 없습니다: %w", err)
	}
	return &Store{pool: pool}, nil
}

// validSchemaName은 이름을 문자와 숫자, 밑줄로 제한합니다.
// 스키마 이름은 값으로 넘길 수 없어 문자열로 붙여야 하므로 여기서 막습니다.
func validSchemaName(name string) error {
	if len(name) == 0 || len(name) > 63 {
		return fmt.Errorf("스키마 이름 길이가 잘못되었습니다: %q", name)
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return fmt.Errorf("스키마 이름에 쓸 수 없는 글자가 있습니다: %q", name)
		}
	}
	return nil
}

func createSchema(ctx context.Context, opts Options) error {
	conn, err := pgx.Connect(ctx, opts.URL)
	if err != nil {
		return fmt.Errorf("스키마를 만들려고 연결하지 못했습니다: %w", err)
	}
	defer conn.Close(ctx)

	// 이름은 위에서 검증했습니다.
	if _, err := conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+opts.Schema); err != nil {
		return fmt.Errorf("스키마 %s를 만들지 못했습니다: %w", opts.Schema, err)
	}
	return nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Migrate(ctx context.Context) error {
	entries, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(entries)

	for _, name := range entries {
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("마이그레이션 %s 적용에 실패했습니다: %w", name, err)
		}
	}
	return nil
}
