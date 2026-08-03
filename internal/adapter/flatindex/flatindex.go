// Package flatindex는 벡터 색인을 납작한 파일 둘로 들고 있습니다.
//
// Qdrant는 장수에 비례하는 몫을 반드시 램에 붙들고 있어야 합니다. 모자라면
// 느려지는 것이 아니라 죽고, 다시 띄워도 컬렉션을 여는 것 자체가 한도를
// 넘어 또 죽습니다. 되살릴 방법이 지우는 것밖에 없습니다.
//
// 여기서는 이진 코드만 파일에 두고 통째로 훑습니다. 파일을 주소 공간에
// 걸어 두므로 힙에 올라가는 것이 없습니다. 커널이 페이지 캐시로 들고
// 있다가 메모리가 모자라면 알아서 버립니다. 그러면 죽는 대신 다시 읽느라
// 느려질 뿐입니다. 램이 요구 조건에서 캐시로 바뀝니다.
//
// 1,190만 장이면 코드가 1.06 GiB입니다. 두 코어로 한 질의에 40밀리초쯤
// 걸립니다. 그래프를 안 타므로 빠뜨리는 점이 없고, 정확도는 재점수가
// 정합니다.
//
// 원본 벡터는 여기 두지 않습니다. 이미 PostgreSQL에 들어 있으므로,
// 훑어서 추린 후보 몇십 건만 꺼내 다시 점수를 매깁니다. 그래서 이 파일들은
// 언제든 다시 만들 수 있는 파생물입니다. 깨지면 다시 만들면 됩니다.
package flatindex

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"saucedust/internal/domain"
)

// Vectors는 원본 벡터를 아이디로 가져옵니다.
//
// 훑기는 이진 코드로 후보만 추립니다. 부호만 남긴 것이라 그대로 쓰면
// 1등 정답률이 89.7퍼센트까지 떨어집니다. 원본으로 다시 점수를 매기면
// 96퍼센트대로 올라오므로 이 단계는 뺄 수 없습니다.
type Vectors interface {
	VectorsByIDs(ctx context.Context, modelID string, imageIDs []int64) (map[int64][]float32, error)
}

const (
	// oversample은 재점수에 넘길 후보를 몇 배로 뽑을지입니다.
	// 4배에서 int8과 사실상 같아졌고 2배면 95.1퍼센트였습니다.
	oversample = 4
	// minCandidates는 적어도 이만큼은 뽑는다는 하한입니다.
	// 열 건을 찾을 때 마흔 건을 보게 됩니다.
	minCandidates = 40
	idSize        = 8
	metaName      = "meta.json"
	codesName     = "codes.bin"
	idsName       = "ids.bin"
)

type Options struct {
	// Dir은 색인 파일을 둘 곳입니다.
	Dir string
	// Source는 재점수에 쓸 원본 벡터를 꺼내 옵니다.
	Source Vectors
	// ReadOnly면 폴더를 함께 쥡니다. 세어 보기만 하는 명령이 도는 동안
	// 중앙 노드가 멈추면 곤란하므로 그런 쪽은 이것을 켭니다.
	ReadOnly bool
	Log      *slog.Logger
}

type Store struct {
	dir      string
	source   Vectors
	readOnly bool
	log      *slog.Logger
	lock     *dirLock

	mu    sync.RWMutex
	colls map[string]*collection
}

// meta는 컬렉션이 어떤 모델의 것인지 적어 둡니다.
//
// 차원이 다른 벡터가 섞이면 오류 없이 검색 결과만 조용히 이상해집니다.
// 열 때마다 대조해서 어긋나면 아예 막습니다.
type meta struct {
	ModelID    string `json:"model_id"`
	VectorSize int    `json:"vector_size"`
	Distance   string `json:"distance"`
}

type collection struct {
	meta      meta
	codeBytes int

	// mu는 붙이는 쪽과 읽는 쪽을 가릅니다. 파일이 커지면 걸어 둔 자리를
	// 다시 잡아야 하는데, 그때 읽고 있던 슬라이스가 살아 있으면 죽습니다.
	mu     sync.RWMutex
	codes  *os.File
	ids    *os.File
	reader codeReader
	count  int
}

func New(opts Options) (*Store, error) {
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, errors.New("색인 파일을 둘 곳이 비어 있습니다")
	}
	if opts.Source == nil {
		return nil, errors.New("재점수에 쓸 원본 벡터 조회가 없습니다")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if err := os.MkdirAll(opts.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("색인 폴더를 만들지 못했습니다: %w", err)
	}
	lock, err := lockDir(opts.Dir, opts.ReadOnly)
	if err != nil {
		return nil, err
	}
	return &Store{
		dir:      opts.Dir,
		source:   opts.Source,
		readOnly: opts.ReadOnly,
		log:      opts.Log,
		lock:     lock,
		colls:    map[string]*collection{},
	}, nil
}

// Ping은 색인 폴더에 쓸 수 있는지 봅니다.
func (s *Store) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(s.dir)
	if err != nil {
		return fmt.Errorf("색인 폴더를 열지 못했습니다: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("색인 자리가 폴더가 아닙니다: %s", s.dir)
	}
	return nil
}

func (s *Store) EnsureCollection(ctx context.Context, m domain.EmbeddingModel) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.Collection == "" {
		return errors.New("컬렉션 이름이 비어 있습니다")
	}
	if m.VectorSize <= 0 {
		return fmt.Errorf("모델 %s의 벡터 차원이 잘못되었습니다: %d", m.ID, m.VectorSize)
	}
	// 부호만 남기는 방식이라 각도로 견주는 거리에서만 뜻이 있습니다.
	// 길이로 견주는 거리는 부호에 담기지 않으므로 조용히 틀립니다.
	if d := strings.ToLower(m.Distance); d != "" && d != "cosine" && d != "dot" {
		return fmt.Errorf("납작한 색인은 %s 거리를 다루지 못합니다. 모델 %s", m.Distance, m.ID)
	}

	_, err := s.open(m)
	return err
}

// open은 컬렉션을 열어 둡니다. 이미 열려 있으면 그것을 냅니다.
func (s *Store) open(m domain.EmbeddingModel) (*collection, error) {
	s.mu.RLock()
	c, ok := s.colls[m.Collection]
	s.mu.RUnlock()
	if ok {
		return c, c.verify(m)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.colls[m.Collection]; ok {
		return c, c.verify(m)
	}

	c, err := openCollection(filepath.Join(s.dir, m.Collection), m, s.readOnly, s.log)
	if err != nil {
		return nil, err
	}
	s.colls[m.Collection] = c
	return c, nil
}

// lookup은 이름만으로 컬렉션을 찾습니다. 아직 안 열었으면 엽니다.
//
// 세어 보기만 하는 명령은 모델 정보 없이 들어와서 EnsureCollection을
// 부르지 않습니다. 그때 "안 열었습니다"로 답하면 컬렉션이 멀쩡히
// 있는데도 없다고 보고합니다.
func (s *Store) lookup(name string) (*collection, error) {
	s.mu.RLock()
	c, ok := s.colls[name]
	s.mu.RUnlock()
	if ok {
		return c, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.colls[name]; ok {
		return c, nil
	}
	c, err := openExisting(filepath.Join(s.dir, name), s.readOnly, s.log)
	if err != nil {
		return nil, fmt.Errorf("컬렉션 %s를 열지 못했습니다: %w", name, err)
	}
	s.colls[name] = c
	return c, nil
}

func (s *Store) Upsert(ctx context.Context, name string, points []domain.VectorPoint) error {
	if len(points) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// 함께 쥔 채로 붙이면 다른 프로세스와 같은 자리에 씁니다.
	if s.readOnly {
		return errors.New("읽기 전용으로 연 색인에는 넣을 수 없습니다")
	}
	c, err := s.lookup(name)
	if err != nil {
		return err
	}
	return c.append(points)
}

func (s *Store) Count(ctx context.Context, name string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	c, err := s.lookup(name)
	if err != nil {
		return 0, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return int64(c.count), nil
}

func (s *Store) Search(ctx context.Context, name string, vector []float32, limit int) ([]domain.VectorMatch, error) {
	if limit <= 0 {
		return nil, nil
	}
	c, err := s.lookup(name)
	if err != nil {
		return nil, err
	}
	if len(vector) != c.meta.VectorSize {
		return nil, fmt.Errorf("%w: 질의 벡터 차원이 %d입니다. 컬렉션 %s는 %d입니다",
			domain.ErrModelMismatch, len(vector), name, c.meta.VectorSize)
	}

	want := limit * oversample
	if want < minCandidates {
		want = minCandidates
	}

	ids, err := c.candidates(encode(vector, c.codeBytes), want)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	originals, err := s.source.VectorsByIDs(ctx, c.meta.ModelID, ids)
	if err != nil {
		return nil, fmt.Errorf("재점수용 원본 벡터를 가져오지 못했습니다: %w", err)
	}
	return rescore(vector, ids, originals, limit), nil
}

// candidates는 훑어서 후보 이미지 아이디를 냅니다.
//
// 같은 이미지가 두 자리에 들어 있을 수 있습니다. 벡터를 다시 계산해
// 넣으면 예전 자리가 남기 때문입니다. 재점수는 아이디로 하므로 여기서
// 겹치는 것을 걸러 둡니다. 안 걸러 내면 같은 그림이 결과를 다 채웁니다.
func (c *collection) candidates(code []byte, want int) ([]int64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.count == 0 {
		return nil, nil
	}

	found, err := scan(c.reader, code, c.count, c.codeBytes, want)
	if err != nil {
		return nil, err
	}

	seen := make(map[int64]struct{}, len(found))
	ids := make([]int64, 0, len(found))
	for _, f := range found {
		id, err := c.idAt(f.slot)
		if err != nil {
			return nil, err
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

// idAt은 자리에 든 이미지 아이디를 냅니다.
func (c *collection) idAt(slot int) (int64, error) {
	var raw [idSize]byte
	if _, err := c.ids.ReadAt(raw[:], int64(slot)*idSize); err != nil {
		return 0, fmt.Errorf("아이디 파일 %d번 자리를 읽지 못했습니다: %w", slot, err)
	}
	// append가 음수를 막으므로 최상위 비트는 항상 0입니다.
	stored := binary.LittleEndian.Uint64(raw[:])
	if stored > math.MaxInt64 {
		return 0, fmt.Errorf("아이디 파일 %d번 자리가 망가졌습니다: %d", slot, stored)
	}
	return int64(stored), nil
}

// rescore는 원본 벡터로 다시 점수를 매겨 상위 limit개를 냅니다.
//
// 원본을 못 찾은 아이디는 버립니다. 색인 파일은 파생물이라 PostgreSQL이
// 지운 것을 아직 들고 있을 수 있습니다. 그것을 결과에 넣으면 없는 그림을
// 답으로 내게 됩니다.
func rescore(query []float32, ids []int64, originals map[int64][]float32, limit int) []domain.VectorMatch {
	out := make([]domain.VectorMatch, 0, len(ids))
	for _, id := range ids {
		v, ok := originals[id]
		if !ok || len(v) != len(query) {
			continue
		}
		out = append(out, domain.VectorMatch{ImageID: id, Score: cosine(query, v)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ImageID < out[j].ImageID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func cosine(a, b []float32) float32 {
	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

// encode는 벡터를 부호 비트만 남겨 줄입니다.
//
// 차원마다 1비트라 768차원이 96바이트가 됩니다. 각도로 견주는 거리에서는
// 부호만으로도 방향이 대충 남아서, 넓게 뽑아 재점수하면 되찾을 수 있습니다.
func encode(v []float32, codeBytes int) []byte {
	code := make([]byte, codeBytes)
	for i, x := range v {
		if x > 0 {
			code[i/8] |= 1 << (i % 8)
		}
	}
	return code
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for name, c := range s.colls {
		if err := c.close(); err != nil && first == nil {
			first = fmt.Errorf("컬렉션 %s를 닫지 못했습니다: %w", name, err)
		}
	}
	s.colls = map[string]*collection{}
	if err := s.lock.close(); err != nil && first == nil {
		first = err
	}
	s.lock = nil
	return first
}

// 실수로 포트에서 벗어나지 않도록 컴파일 때 확인합니다.
var _ interface {
	EnsureCollection(context.Context, domain.EmbeddingModel) error
	Upsert(context.Context, string, []domain.VectorPoint) error
	Search(context.Context, string, []float32, int) ([]domain.VectorMatch, error)
	Count(context.Context, string) (int64, error)
	Ping(context.Context) error
} = (*Store)(nil)
