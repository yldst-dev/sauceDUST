package flatindex

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"saucedust/internal/domain"
)

// openCollection은 컬렉션 폴더를 열거나 새로 만듭니다.
func openCollection(dir string, m domain.EmbeddingModel, readOnly bool, log *slog.Logger) (*collection, error) {
	want := meta{ModelID: m.ID, VectorSize: m.VectorSize, Distance: distanceName(m.Distance)}
	got, err := readMeta(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if readOnly {
			return nil, fmt.Errorf("컬렉션 %s가 아직 없습니다", m.Collection)
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("컬렉션 폴더를 만들지 못했습니다: %w", err)
		}
		if err := writeMeta(dir, want); err != nil {
			return nil, err
		}
		got = want
	case err != nil:
		return nil, err
	default:
		if err := got.check(want, m.Collection); err != nil {
			return nil, err
		}
	}
	return openWithMeta(dir, got, readOnly, log)
}

// openExisting은 이름만으로 이미 있는 컬렉션을 엽니다.
//
// 세어 보기만 하는 명령은 모델 정보 없이 들어옵니다. 컬렉션 설명이
// 폴더에 함께 있으므로 그것을 읽으면 됩니다.
func openExisting(dir string, readOnly bool, log *slog.Logger) (*collection, error) {
	got, err := readMeta(dir)
	if err != nil {
		return nil, err
	}
	return openWithMeta(dir, got, readOnly, log)
}

func openWithMeta(dir string, m meta, readOnly bool, log *slog.Logger) (*collection, error) {
	if m.VectorSize <= 0 {
		return nil, fmt.Errorf("컬렉션 설명의 차원이 %d입니다", m.VectorSize)
	}
	c := &collection{meta: m, codeBytes: (m.VectorSize + 7) / 8}
	if err := c.openFiles(dir, readOnly, log); err != nil {
		return nil, err
	}
	return c, nil
}

// openFiles는 코드와 아이디 파일을 열고 자리 수를 셉니다.
//
// 두 파일 길이가 어긋나면 짧은 쪽에 맞춰 자릅니다. 붙이는 중에 갑자기
// 꺼지면 한쪽만 늘어난 채 남습니다. 그대로 두면 자리마다 다른 그림의
// 코드와 아이디가 짝지어져, 오류 없이 검색 결과만 틀립니다.
//
// 잘라 낸 몫은 잃지 않습니다. PostgreSQL은 Upsert가 끝난 뒤에야 색인
// 완료로 적으므로, 못 들어간 것은 다음에 다시 옵니다.
func (c *collection) openFiles(dir string, readOnly bool, log *slog.Logger) (err error) {
	flag := os.O_RDWR | os.O_CREATE
	if readOnly {
		flag = os.O_RDONLY
	}

	// 경로는 설정에서 온 색인 폴더와 컬렉션 이름으로만 만들어집니다.
	codes, err := os.OpenFile(filepath.Join(dir, codesName), flag, 0o600) // #nosec G304
	if err != nil {
		return fmt.Errorf("코드 파일을 열지 못했습니다: %w", err)
	}
	ids, err := os.OpenFile(filepath.Join(dir, idsName), flag, 0o600) // #nosec G304
	if err != nil {
		_ = codes.Close()
		return fmt.Errorf("아이디 파일을 열지 못했습니다: %w", err)
	}
	// 중간에 어디서 빠져나가든 열어 둔 것을 놓습니다. 성공하면 구조체가
	// 들고 가므로 닫지 않습니다.
	defer func() {
		if err != nil {
			_ = codes.Close()
			_ = ids.Close()
		}
	}()

	codeInfo, err := codes.Stat()
	if err != nil {
		return err
	}
	idInfo, err := ids.Stat()
	if err != nil {
		return err
	}

	slots := int(codeInfo.Size() / int64(c.codeBytes))
	if byID := int(idInfo.Size() / idSize); byID < slots {
		slots = byID
	}
	if codeInfo.Size() != int64(slots)*int64(c.codeBytes) || idInfo.Size() != int64(slots)*idSize {
		// 읽기 전용이면 자를 수 없습니다. 어긋난 꼬리는 어차피 자리 수
		// 밖이라 안 읽히므로 그대로 두고 알리기만 합니다.
		log.Warn("색인 파일 끝이 어긋납니다. 못 들어간 몫은 다시 옵니다",
			slog.String("폴더", dir), slog.Int("쓸 자리", slots),
			slog.Bool("읽기 전용", readOnly))
		if !readOnly {
			if err = codes.Truncate(int64(slots) * int64(c.codeBytes)); err != nil {
				return err
			}
			if err = ids.Truncate(int64(slots) * idSize); err != nil {
				return err
			}
		}
	}

	reader, err := openCodes(codes, slots*c.codeBytes)
	if err != nil {
		return err
	}

	c.codes, c.ids, c.reader, c.count = codes, ids, reader, slots
	return nil
}

// append는 자리를 이어 붙입니다.
//
// 코드를 먼저 쓰고 아이디를 나중에 씁니다. 반대로 하면 갑자기 꺼졌을 때
// 아이디만 있는 자리가 생기고, 그 자리는 남의 코드를 가리키게 됩니다.
// 이 순서면 코드만 있는 자리가 생기는데, 그것은 열 때 잘려 나갑니다.
func (c *collection) append(points []domain.VectorPoint) error {
	codeBuf := make([]byte, 0, len(points)*c.codeBytes)
	idBuf := make([]byte, 0, len(points)*idSize)
	for _, p := range points {
		if len(p.Vector) != c.meta.VectorSize {
			return fmt.Errorf("%w: image %d의 벡터 차원이 %d입니다. 기준은 %d입니다",
				domain.ErrModelMismatch, p.ImageID, len(p.Vector), c.meta.VectorSize)
		}
		// 아이디는 PostgreSQL의 증가하는 번호라 음수가 될 수 없습니다.
		// 음수가 왔다면 어딘가 잘못된 것이므로 파일에 넣기 전에 막습니다.
		if p.ImageID < 0 {
			return fmt.Errorf("image 아이디가 음수입니다: %d", p.ImageID)
		}
		codeBuf = append(codeBuf, encode(p.Vector, c.codeBytes)...)
		idBuf = binary.LittleEndian.AppendUint64(idBuf, uint64(p.ImageID))
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	at := int64(c.count)
	if _, err := c.codes.WriteAt(codeBuf, at*int64(c.codeBytes)); err != nil {
		return fmt.Errorf("코드를 쓰지 못했습니다: %w", err)
	}
	if err := c.codes.Sync(); err != nil {
		return fmt.Errorf("코드를 디스크에 내리지 못했습니다: %w", err)
	}
	if _, err := c.ids.WriteAt(idBuf, at*idSize); err != nil {
		return fmt.Errorf("아이디를 쓰지 못했습니다: %w", err)
	}
	if err := c.ids.Sync(); err != nil {
		return fmt.Errorf("아이디를 디스크에 내리지 못했습니다: %w", err)
	}

	// 파일이 커졌으므로 걸어 둔 자리를 다시 잡습니다. 읽는 쪽은 이 잠금
	// 밖으로 슬라이스를 들고 나가지 않으므로 여기서 풀어도 안전합니다.
	next := c.count + len(points)
	reader, err := openCodes(c.codes, next*c.codeBytes)
	if err != nil {
		return err
	}
	old := c.reader
	c.reader, c.count = reader, next
	if old != nil {
		return old.close()
	}
	return nil
}

func (c *collection) verify(m domain.EmbeddingModel) error {
	return c.meta.check(
		meta{ModelID: m.ID, VectorSize: m.VectorSize, Distance: distanceName(m.Distance)},
		m.Collection)
}

// check는 이미 있는 컬렉션이 지금 모델과 같은 것인지 봅니다.
// 다르면 벡터가 섞여 검색이 조용히 망가지므로 시작을 막습니다.
func (got meta) check(want meta, name string) error {
	if got.VectorSize != want.VectorSize {
		return fmt.Errorf("%w: 컬렉션 %s의 차원이 %d인데 모델 %s는 %d입니다",
			domain.ErrModelMismatch, name, got.VectorSize, want.ModelID, want.VectorSize)
	}
	if got.Distance != want.Distance {
		return fmt.Errorf("%w: 컬렉션 %s의 거리가 %s인데 모델 %s는 %s입니다",
			domain.ErrModelMismatch, name, got.Distance, want.ModelID, want.Distance)
	}
	if got.ModelID != want.ModelID {
		return fmt.Errorf("%w: 컬렉션 %s가 모델 %s의 것인데 %s로 열려 합니다",
			domain.ErrModelMismatch, name, got.ModelID, want.ModelID)
	}
	return nil
}

func (c *collection) close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var first error
	if c.reader != nil {
		if err := c.reader.close(); err != nil {
			first = err
		}
		c.reader = nil
	}
	for _, f := range []*os.File{c.codes, c.ids} {
		if f == nil {
			continue
		}
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
	}
	c.codes, c.ids = nil, nil
	return first
}

func readMeta(dir string) (meta, error) {
	// 경로는 설정에서 온 색인 폴더와 컬렉션 이름으로만 만들어집니다.
	raw, err := os.ReadFile(filepath.Join(dir, metaName)) // #nosec G304 -- 설정에서 온 색인 폴더와 컬렉션 이름뿐입니다
	if err != nil {
		return meta{}, err
	}
	var m meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return meta{}, fmt.Errorf("컬렉션 설명을 읽지 못했습니다: %w", err)
	}
	return m, nil
}

// writeMeta는 임시 파일에 쓰고 바꿔 끼웁니다.
// 쓰다 말고 꺼지면 반쪽짜리 설명이 남아 다음에 열지 못합니다.
func writeMeta(dir string, m meta) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, metaName+".tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil { // #nosec G306 -- 색인 폴더 안 고정 이름입니다
		return fmt.Errorf("컬렉션 설명을 쓰지 못했습니다: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, metaName)); err != nil {
		return fmt.Errorf("컬렉션 설명을 바꿔 끼우지 못했습니다: %w", err)
	}
	return nil
}

func distanceName(raw string) string {
	switch strings.ToLower(raw) {
	case "", "cosine":
		return "cosine"
	case "dot":
		return "dot"
	default:
		return strings.ToLower(raw)
	}
}
