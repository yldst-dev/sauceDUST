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

func (s *Store) ExistingPostIDs(ctx context.Context, site string, postIDs []int64) (map[int64]int64, error) {
	out := make(map[int64]int64, len(postIDs))
	if len(postIDs) == 0 {
		return out, nil
	}

	rows, err := s.pool.Query(ctx, `
SELECT source_post_id, id FROM images
WHERE source_site = $1 AND source_post_id = ANY($2)`, site, postIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var postID, imageID int64
		if err := rows.Scan(&postID, &imageID); err != nil {
			return nil, err
		}
		out[postID] = imageID
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

// PendingVectors는 Qdrant에 아직 반영되지 않은 벡터를 돌려줍니다.
// Qdrant를 잃어버렸을 때 PostgreSQL만으로 색인을 재구축하는 경로입니다.
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

func (s *Store) CachedQuery(ctx context.Context, sha, modelID string) ([]float32, bool, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `
UPDATE query_cache SET hits = hits + 1, last_used_at = now()
WHERE sha256 = $1 AND model_id = $2
RETURNING vector`, sha, modelID).Scan(&raw)
	if err != nil {
		if isNoRows(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	values, err := domain.DecodeVector(raw)
	if err != nil {
		return nil, false, err
	}
	return values, true, nil
}

func (s *Store) SaveQuery(ctx context.Context, sha, modelID string, vector []float32) error {
	_, err := s.pool.Exec(ctx, `
INSERT INTO query_cache (sha256, model_id, vector)
VALUES ($1, $2, $3)
ON CONFLICT (sha256, model_id) DO UPDATE SET last_used_at = now()`,
		sha, modelID, domain.EncodeVector(vector))
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
