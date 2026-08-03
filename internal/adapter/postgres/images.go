package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"saucedust/internal/domain"
)

const saveVectorSQL = `
INSERT INTO image_vectors (image_id, model_id, vector, indexed_at)
VALUES ($1, $2, $3, NULL)
ON CONFLICT (image_id, model_id) DO UPDATE SET
    vector     = EXCLUDED.vector,
    indexed_at = NULL`

const imageColumns = `id, source_site, source_post_id, source_url, canonical_url, file_url,
       preview_url, md5, phash, dhash, width, height, file_size, rating, score,
       tags, artist_tags, thumb_path, indexed_by, created_at, updated_at`

const upsertImageSQL = `
INSERT INTO images (source_site, source_post_id, source_url, canonical_url, file_url,
                    preview_url, md5, phash, dhash, width, height, file_size, rating,
                    score, tags, artist_tags, thumb_path, indexed_by, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18, now())
ON CONFLICT (source_site, source_post_id) DO UPDATE SET
    source_url    = EXCLUDED.source_url,
    canonical_url = EXCLUDED.canonical_url,
    file_url      = EXCLUDED.file_url,
    preview_url   = EXCLUDED.preview_url,
    md5           = EXCLUDED.md5,
    phash         = COALESCE(EXCLUDED.phash, images.phash),
    dhash         = COALESCE(EXCLUDED.dhash, images.dhash),
    width         = EXCLUDED.width,
    height        = EXCLUDED.height,
    file_size     = EXCLUDED.file_size,
    rating        = EXCLUDED.rating,
    score         = EXCLUDED.score,
    tags          = EXCLUDED.tags,
    artist_tags   = EXCLUDED.artist_tags,
    thumb_path    = COALESCE(EXCLUDED.thumb_path, images.thumb_path),
    indexed_by    = EXCLUDED.indexed_by,
    updated_at    = now()
RETURNING id`

func (s *Store) UpsertImage(ctx context.Context, img *domain.Image) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, upsertImageSQL,
		img.SourceSite, img.SourcePostID, nullStr(img.SourceURL), nullStr(img.CanonicalURL),
		nullStr(img.FileURL), nullStr(img.PreviewURL), nullStr(img.MD5), nullStr(img.PHash),
		nullStr(img.DHash), img.Width, img.Height, img.FileSize, nullStr(img.Rating),
		img.Score, textArray(img.Tags), textArray(img.ArtistTags),
		nullStr(img.ThumbPath), nullStr(img.IndexedBy),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("이미지 저장에 실패했습니다 (post %d): %w", img.SourcePostID, err)
	}
	img.ID = id
	return id, nil
}

// UpsertImages는 pgx의 파이프라인으로 전부를 한 번의 왕복에 보냅니다.
// 건마다 QueryRow를 부르면 왕복이 건수만큼 늘어납니다.
func (s *Store) UpsertImages(ctx context.Context, imgs []*domain.Image) ([]int64, error) {
	if len(imgs) == 0 {
		return nil, nil
	}

	batch := &pgx.Batch{}
	for _, img := range imgs {
		batch.Queue(upsertImageSQL,
			img.SourceSite, img.SourcePostID, nullStr(img.SourceURL), nullStr(img.CanonicalURL),
			nullStr(img.FileURL), nullStr(img.PreviewURL), nullStr(img.MD5), nullStr(img.PHash),
			nullStr(img.DHash), img.Width, img.Height, img.FileSize, nullStr(img.Rating),
			img.Score, textArray(img.Tags), textArray(img.ArtistTags),
			nullStr(img.ThumbPath), nullStr(img.IndexedBy))
	}

	results := s.pool.SendBatch(ctx, batch)
	defer results.Close()

	ids := make([]int64, len(imgs))
	for i, img := range imgs {
		if err := results.QueryRow().Scan(&ids[i]); err != nil {
			return nil, fmt.Errorf("이미지 저장에 실패했습니다 (post %d): %w", img.SourcePostID, err)
		}
		img.ID = ids[i]
	}
	return ids, results.Close()
}

// ExistingPostIDs는 이미 다 끝난 게시물만 냅니다.
//
// "행이 있다"와 "다 끝났다"는 다릅니다. 메타데이터를 쓴 뒤 벡터를 넣기
// 전에 끊기면 행만 남습니다. 존재만 보고 건너뛰면 그 이미지는 벡터 없이
// 영영 남아 검색에 안 걸립니다. 오류도 남지 않아 알아챌 수도 없습니다.
//
// 그래서 넘겨받은 모델 전부의 벡터가 있는 것만 끝났다고 봅니다. 모델
// 목록이 비어 있으면 예전처럼 존재만 봅니다.
func (s *Store) ExistingPostIDs(ctx context.Context, site string, postIDs []int64, modelIDs []string) (map[int64]int64, error) {
	out := make(map[int64]int64, len(postIDs))
	if len(postIDs) == 0 {
		return out, nil
	}

	const q = `
SELECT i.source_post_id, i.id
FROM images i
WHERE i.source_site = $1 AND i.source_post_id = ANY($2)
  AND ($3::text[] IS NULL OR cardinality($3::text[]) = 0 OR NOT EXISTS (
      SELECT 1 FROM unnest($3::text[]) AS want(model_id)
      WHERE NOT EXISTS (
          SELECT 1 FROM image_vectors v
          WHERE v.image_id = i.id AND v.model_id = want.model_id
      )
  ))`

	rows, err := s.pool.Query(ctx, q, site, postIDs, modelIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var postID, id int64
		if err := rows.Scan(&postID, &id); err != nil {
			return nil, err
		}
		out[postID] = id
	}
	return out, rows.Err()
}

func (s *Store) ImageByID(ctx context.Context, id int64) (*domain.Image, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+imageColumns+` FROM images WHERE id = $1`, id)
	return scanImage(row)
}

func (s *Store) ImageBySource(ctx context.Context, site string, postID int64) (*domain.Image, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+imageColumns+` FROM images WHERE source_site = $1 AND source_post_id = $2`,
		site, postID)
	return scanImage(row)
}

func (s *Store) ImagesByIDs(ctx context.Context, ids []int64) (map[int64]domain.Image, error) {
	out := make(map[int64]domain.Image, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `SELECT `+imageColumns+` FROM images WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		img, err := scanImage(rows)
		if err != nil {
			return nil, err
		}
		out[img.ID] = *img
	}
	return out, rows.Err()
}

func (s *Store) CountImages(ctx context.Context) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM images`).Scan(&n)
	return n, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanImage(row scanner) (*domain.Image, error) {
	var (
		img                                               domain.Image
		sourceURL, canonicalURL, fileURL, previewURL, md5 *string
		phash, dhash, rating, thumbPath, indexedBy        *string
		width, height, score                              *int
		fileSize                                          *int64
	)

	err := row.Scan(&img.ID, &img.SourceSite, &img.SourcePostID, &sourceURL, &canonicalURL,
		&fileURL, &previewURL, &md5, &phash, &dhash, &width, &height, &fileSize,
		&rating, &score, &img.Tags, &img.ArtistTags, &thumbPath, &indexedBy,
		&img.CreatedAt, &img.UpdatedAt)
	if err != nil {
		if isNoRows(err) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}

	img.SourceURL = derefStr(sourceURL)
	img.CanonicalURL = derefStr(canonicalURL)
	img.FileURL = derefStr(fileURL)
	img.PreviewURL = derefStr(previewURL)
	img.MD5 = derefStr(md5)
	img.PHash = derefStr(phash)
	img.DHash = derefStr(dhash)
	img.Rating = derefStr(rating)
	img.ThumbPath = derefStr(thumbPath)
	img.IndexedBy = derefStr(indexedBy)
	img.Width = derefInt(width)
	img.Height = derefInt(height)
	img.Score = derefInt(score)
	if fileSize != nil {
		img.FileSize = *fileSize
	}
	return &img, nil
}

func (s *Store) SaveVectors(ctx context.Context, imageID int64, vectors []domain.Vector) error {
	if len(vectors) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, v := range vectors {
		batch.Queue(saveVectorSQL, imageID, v.ModelID, domain.EncodeVector(v.Values))
	}
	if err := s.pool.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("벡터 백업에 실패했습니다 (image %d): %w", imageID, err)
	}
	return nil
}

// SaveVectorBatch는 여러 이미지의 벡터를 한 번의 왕복으로 저장합니다.
func (s *Store) SaveVectorBatch(ctx context.Context, items []domain.StoredVector) error {
	if len(items) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, item := range items {
		batch.Queue(saveVectorSQL, item.ImageID, item.ModelID, domain.EncodeVector(item.Values))
	}
	if err := s.pool.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("벡터 %d건 백업에 실패했습니다: %w", len(items), err)
	}
	return nil
}

// MarkIndexedBatch는 여러 이미지의 색인 완료를 한 번에 표시합니다.
func (s *Store) MarkIndexedBatch(ctx context.Context, imageIDs []int64, modelIDs []string) error {
	if len(imageIDs) == 0 || len(modelIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
UPDATE image_vectors SET indexed_at = now()
WHERE image_id = ANY($1) AND model_id = ANY($2)`, imageIDs, modelIDs)
	return err
}

func (s *Store) MarkVectorsIndexed(ctx context.Context, imageID int64, modelIDs []string) error {
	if len(modelIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
UPDATE image_vectors SET indexed_at = now()
WHERE image_id = $1 AND model_id = ANY($2)`, imageID, modelIDs)
	return err
}

// PendingVectors는 색인에 아직 반영되지 않은 벡터를 돌려줍니다.
// 색인을 잃어버렸을 때 PostgreSQL만으로 다시 세우는 경로입니다.
func (s *Store) PendingVectors(ctx context.Context, modelID string, limit int) ([]domain.StoredVector, error) {
	rows, err := s.pool.Query(ctx, `
SELECT image_id, vector FROM image_vectors
WHERE model_id = $1 AND indexed_at IS NULL
ORDER BY image_id
LIMIT $2`, modelID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []domain.StoredVector
	for rows.Next() {
		var (
			imageID int64
			raw     []byte
		)
		if err := rows.Scan(&imageID, &raw); err != nil {
			return nil, err
		}
		values, err := domain.DecodeVector(raw)
		if err != nil {
			return nil, fmt.Errorf("image %d 벡터를 해석하지 못했습니다: %w", imageID, err)
		}
		out = append(out, domain.StoredVector{ImageID: imageID, ModelID: modelID, Values: values})
	}
	return out, rows.Err()
}

// VectorsByIDs는 주어진 이미지들의 원본 벡터를 돌려줍니다.
//
// 납작한 색인이 재점수에 씁니다. 훑기는 부호만 남긴 이진 코드로 후보를
// 추리는데, 그대로 쓰면 1등 정답률이 89.7퍼센트까지 떨어집니다. 추린
// 몇십 건만 원본으로 다시 재면 96퍼센트대로 올라옵니다.
//
// 없는 아이디는 그냥 빠집니다. 색인 파일은 파생물이라 이미 지운 그림을
// 아직 들고 있을 수 있는데, 그것을 오류로 만들면 검색 전체가 실패합니다.
func (s *Store) VectorsByIDs(ctx context.Context, modelID string, imageIDs []int64) (map[int64][]float32, error) {
	if len(imageIDs) == 0 {
		return map[int64][]float32{}, nil
	}
	rows, err := s.pool.Query(ctx, `
SELECT image_id, vector FROM image_vectors
WHERE model_id = $1 AND image_id = ANY($2)`, modelID, imageIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64][]float32, len(imageIDs))
	for rows.Next() {
		var (
			imageID int64
			raw     []byte
		)
		if err := rows.Scan(&imageID, &raw); err != nil {
			return nil, err
		}
		values, err := domain.DecodeVector(raw)
		if err != nil {
			return nil, fmt.Errorf("image %d 벡터를 해석하지 못했습니다: %w", imageID, err)
		}
		out[imageID] = values
	}
	return out, rows.Err()
}

// VectorsMissing은 주어진 이미지 중 해당 모델의 벡터가 없는 것만 돌려줍니다.
// 모델을 새로 추가했을 때 다시 계산할 대상을 고르는 데 씁니다.
func (s *Store) VectorsMissing(ctx context.Context, imageIDs []int64, modelID string) ([]int64, error) {
	if len(imageIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
SELECT candidate FROM unnest($1::bigint[]) AS candidate
WHERE NOT EXISTS (
    SELECT 1 FROM image_vectors v
    WHERE v.image_id = candidate AND v.model_id = $2
)`, imageIDs, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ThumbsMissingVector는 축소본은 있는데 이 모델의 벡터가 없는 이미지를 냅니다.
//
// 모델을 바꾸면 쌓인 것을 전부 다시 계산해야 합니다. 원본은 저장하지 않지만
// 축소본은 남겨 두므로 Danbooru를 다시 훑지 않아도 됩니다. 이 조회가 그
// 대상을 찾아 줍니다.
//
// nextCursor는 다음에 넘길 자리입니다. 0이면 더 볼 것이 없다는 뜻입니다.
// 걸러진 결과가 비어 있어도 nextCursor가 0이 아니면 계속 넘겨야 합니다.
// 이 묶음이 전부 이미 계산된 것이었을 뿐 뒤에 남아 있을 수 있습니다.
//
// 두 번에 나눠 묻습니다. 한 문장으로 쓰면 PostgreSQL이 병합 안티 조인을
// 골라, 한 묶음을 얻으려고 이미 계산된 벡터를 전부 훑습니다. 1백만 행에서
// 재보니 256건을 찾는 데 47만 행을 읽고 60밀리초가 걸렸습니다. 게다가 뒤로
// 갈수록 훑을 것이 늘어 전체가 제곱으로 커집니다. 나눠 물으면 색인으로
// 256번만 찔러 보므로 2밀리초이고 묶음마다 일정합니다.
func (s *Store) ThumbsMissingVector(ctx context.Context, modelID string,
	afterID int64, limit int) (refs []domain.ThumbRef, nextCursor int64, err error) {
	if limit <= 0 {
		limit = 200
	}

	// 1단계. 축소본이 있는 것만 순서대로 한 묶음 가져옵니다.
	// 조건이 부분 색인과 같아야 색인을 씁니다.
	rows, err := s.pool.Query(ctx, `
SELECT id, source_site, source_post_id, thumb_path
FROM images
WHERE id > $1 AND thumb_path IS NOT NULL AND thumb_path <> ''
ORDER BY id
LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("다시 계산할 이미지를 찾지 못했습니다: %w", err)
	}
	defer rows.Close()

	page := make([]domain.ThumbRef, 0, limit)
	for rows.Next() {
		var item domain.ThumbRef
		if err := rows.Scan(&item.ImageID, &item.SourceSite,
			&item.SourcePostID, &item.ThumbPath); err != nil {
			return nil, 0, err
		}
		page = append(page, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(page) == 0 {
		return nil, 0, nil
	}
	nextCursor = page[len(page)-1].ImageID

	// 2단계. 그중 이 모델의 벡터가 없는 것만 남깁니다.
	// 묶음이 작아 기본키로 한 건씩 찔러 보는 것이 가장 빠릅니다.
	ids := make([]int64, 0, len(page))
	for _, item := range page {
		ids = append(ids, item.ImageID)
	}
	missing, err := s.VectorsMissing(ctx, ids, modelID)
	if err != nil {
		return nil, 0, err
	}
	if len(missing) == 0 {
		return nil, nextCursor, nil
	}

	want := make(map[int64]struct{}, len(missing))
	for _, id := range missing {
		want[id] = struct{}{}
	}
	for _, item := range page {
		if _, ok := want[item.ImageID]; ok {
			refs = append(refs, item)
		}
	}
	return refs, nextCursor, nil
}

// CountThumbsMissingVector는 남은 일이 얼마나 되는지 셉니다.
// 며칠 걸릴 수도 있는 작업이라 시작 전에 규모를 알려 줍니다.
func (s *Store) CountThumbsMissingVector(ctx context.Context, modelID string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `
SELECT count(*) FROM images
WHERE thumb_path IS NOT NULL AND thumb_path <> ''
  AND NOT EXISTS (
      SELECT 1 FROM image_vectors v
      WHERE v.image_id = images.id AND v.model_id = $1
  )`, modelID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("다시 계산할 수를 세지 못했습니다: %w", err)
	}
	return n, nil
}

// CachedQuery는 벡터와 함께 지각 해시도 돌려줍니다.
//
// 해시를 빼면 캐시가 맞았을 때 재정렬을 못 합니다. 같은 이미지를 두 번
// 검색했을 때 첫 번째는 "같은 그림 맞음"이라 하고 두 번째는 모르겠다고
// 하는, 재현하기 어려운 형태로 드러납니다.
func (s *Store) CachedQuery(ctx context.Context, sha, modelID string) ([]float32, string, bool, error) {
	var (
		raw   []byte
		phash string
	)
	err := s.pool.QueryRow(ctx, `
UPDATE query_cache SET hits = hits + 1, last_used_at = now()
WHERE sha256 = $1 AND model_id = $2
RETURNING vector, phash`, sha, modelID).Scan(&raw, &phash)
	if err != nil {
		if isNoRows(err) {
			return nil, "", false, nil
		}
		return nil, "", false, err
	}
	values, err := domain.DecodeVector(raw)
	if err != nil {
		return nil, "", false, err
	}
	return values, phash, true, nil
}

func (s *Store) SaveQuery(ctx context.Context, sha, modelID string,
	vector []float32, phash string) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO query_cache (sha256, model_id, vector, phash)
VALUES ($1, $2, $3, $4)
ON CONFLICT (sha256, model_id) DO UPDATE SET
    last_used_at = now(),
    phash        = EXCLUDED.phash`,
		sha, modelID, domain.EncodeVector(vector), phash)
	return err
}

// textArray는 빈 슬라이스를 NULL 대신 빈 배열로 보냅니다.
// tags와 artist_tags는 NOT NULL이라 nil을 그대로 보내면 저장이 실패합니다.
// 작가 태그가 없는 게시물이 드물지 않아 실제로 자주 걸립니다.
func textArray(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
