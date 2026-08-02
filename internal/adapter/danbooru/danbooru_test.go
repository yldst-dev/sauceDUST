package danbooru

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"saucedust/internal/domain"
)

// fakeDanbooru는 id 조건을 실제로 해석해서 응답합니다.
// 커서 방식 순회가 구간을 빠뜨리거나 중복하지 않는지 확인하기 위해서입니다.
type fakeDanbooru struct {
	total    int64
	requests int
}

var (
	reGTE = regexp.MustCompile(`id:>=(\d+)`)
	reLT  = regexp.MustCompile(`id:<(\d+)`)
	reGT  = regexp.MustCompile(`id:>(\d+)`)
)

func (f *fakeDanbooru) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests++

	if strings.HasPrefix(r.URL.Path, "/posts/") {
		raw := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/posts/"), ".json")
		id, _ := strconv.ParseInt(raw, 10, 64)
		writeJSON(w, makePost(id))
		return
	}

	tags := r.URL.Query().Get("tags")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = pageSize
	}

	lower := int64(1)
	upper := f.total
	if m := reGTE.FindStringSubmatch(tags); m != nil {
		lower, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if m := reLT.FindStringSubmatch(tags); m != nil {
		v, _ := strconv.ParseInt(m[1], 10, 64)
		upper = v - 1
	}
	if m := reGT.FindStringSubmatch(tags); m != nil {
		v, _ := strconv.ParseInt(m[1], 10, 64)
		lower = v + 1
	}
	if upper > f.total {
		upper = f.total
	}

	var out []apiPost
	if strings.Contains(tags, "order:id_asc") {
		for id := lower; id <= upper && len(out) < limit; id++ {
			out = append(out, makePost(id))
		}
	} else {
		for id := upper; id >= lower && len(out) < limit; id-- {
			out = append(out, makePost(id))
		}
	}
	writeJSON(w, out)
}

func makePost(id int64) apiPost {
	return apiPost{
		ID:              id,
		MD5:             fmt.Sprintf("%032x", id),
		FileURL:         fmt.Sprintf("https://cdn.example/%d.jpg", id),
		LargeFileURL:    fmt.Sprintf("https://cdn.example/large/%d.jpg", id),
		PreviewFileURL:  fmt.Sprintf("https://cdn.example/preview/%d.jpg", id),
		Source:          "https://www.pixiv.net/artworks/1",
		Rating:          "g",
		Score:           int(id % 100),
		ImageWidth:      1000,
		ImageHeight:     1400,
		FileSize:        123456,
		FileExt:         "jpg",
		TagString:       "1girl solo blue_archive",
		TagStringArtist: "someartist",
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("content-type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func newTestClient(t *testing.T, fake *fakeDanbooru) *Client {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	client, err := New(server.Client(), Options{BaseURL: server.URL, Retries: 2})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}
	return client
}

// 한 페이지에 담기지 않는 구간도 빠짐없이, 중복 없이 가져와야 합니다.
func TestPostsInRangeCoversEveryID(t *testing.T) {
	fake := &fakeDanbooru{total: 10_000}
	client := newTestClient(t, fake)

	const lower, upper = 4_001, 4_650
	posts, err := client.PostsInRange(context.Background(), lower, upper, "")
	if err != nil {
		t.Fatalf("수집 실패: %v", err)
	}

	seen := map[int64]int{}
	for _, p := range posts {
		if p.PostID < lower || p.PostID > upper {
			t.Fatalf("구간 밖 게시물이 왔습니다: %d", p.PostID)
		}
		seen[p.PostID]++
	}
	for id := int64(lower); id <= upper; id++ {
		switch seen[id] {
		case 0:
			t.Fatalf("게시물 %d가 빠졌습니다", id)
		case 1:
		default:
			t.Fatalf("게시물 %d가 %d번 중복되었습니다", id, seen[id])
		}
	}
	if fake.requests < 4 {
		t.Fatalf("요청이 %d번뿐입니다. 페이지 순회가 동작하지 않은 것 같습니다", fake.requests)
	}
}

func TestPostsInRangeSinglePage(t *testing.T) {
	client := newTestClient(t, &fakeDanbooru{total: 500})

	posts, err := client.PostsInRange(context.Background(), 10, 19, "")
	if err != nil {
		t.Fatalf("수집 실패: %v", err)
	}
	if len(posts) != 10 {
		t.Fatalf("게시물이 %d개입니다. 10개를 기대했습니다", len(posts))
	}
}

func TestPostsInRangeRejectsInvertedRange(t *testing.T) {
	client := newTestClient(t, &fakeDanbooru{total: 500})
	if _, err := client.PostsInRange(context.Background(), 100, 10, ""); err == nil {
		t.Fatal("뒤집힌 구간은 거부해야 합니다")
	}
}

func TestPostsAfterReturnsAscending(t *testing.T) {
	client := newTestClient(t, &fakeDanbooru{total: 1_000})

	posts, err := client.PostsAfter(context.Background(), 990, "", 50)
	if err != nil {
		t.Fatalf("수집 실패: %v", err)
	}
	if len(posts) != 10 {
		t.Fatalf("게시물이 %d개입니다. 10개를 기대했습니다", len(posts))
	}
	for i := range posts {
		if posts[i].PostID != int64(991+i) {
			t.Fatalf("%d번째가 %d입니다. %d를 기대했습니다", i, posts[i].PostID, 991+i)
		}
	}
}

func TestLatestPostID(t *testing.T) {
	client := newTestClient(t, &fakeDanbooru{total: 12_345})

	id, err := client.LatestPostID(context.Background())
	if err != nil {
		t.Fatalf("조회 실패: %v", err)
	}
	if id != 12_345 {
		t.Fatalf("최신 id가 %d입니다. 12345를 기대했습니다", id)
	}
}

func TestDownloadURLPrefersLargest(t *testing.T) {
	post := makePost(7).toDomain("https://danbooru.example")

	if got := post.DownloadURL(); got != "https://cdn.example/large/7.jpg" {
		t.Fatalf("고른 URL이 %q입니다", got)
	}
	if post.SkipReason() != "" {
		t.Fatalf("건너뛸 이유가 없어야 하는데 %q입니다", post.SkipReason())
	}
	if post.CanonicalURL != "https://danbooru.example/posts/7" {
		t.Fatalf("정규 URL이 %q입니다", post.CanonicalURL)
	}
}

func TestSkipReasonRejectsUnsupported(t *testing.T) {
	cases := map[string]domain.SourcePost{
		"삭제됨":         {IsDeleted: true, LargeURL: "https://x/a.jpg"},
		"차단됨":         {IsBanned: true, LargeURL: "https://x/a.jpg"},
		"URL 없음":      {},
		"지원 안 하는 확장자": {LargeURL: "https://x/a.gif", FileURL: "https://x/a.mp4"},
	}
	for name, post := range cases {
		if post.SkipReason() == "" {
			t.Errorf("%s는 건너뛰어야 합니다", name)
		}
	}
}

func TestDownloadRejectsOversize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 4096))
	}))
	defer server.Close()

	client, err := New(server.Client(), Options{BaseURL: server.URL})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}
	if _, err := client.Download(context.Background(), server.URL+"/big.jpg", 1024); err == nil {
		t.Fatal("크기 제한을 넘으면 실패해야 합니다")
	}
}

func TestGetRetriesOnServerError(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writeJSON(w, []apiPost{makePost(1)})
	}))
	defer server.Close()

	client, err := New(server.Client(), Options{BaseURL: server.URL, Retries: 3})
	if err != nil {
		t.Fatalf("클라이언트 생성 실패: %v", err)
	}

	posts, err := client.PostsAfter(context.Background(), 0, "", 1)
	if err != nil {
		t.Fatalf("재시도 후에도 실패했습니다: %v", err)
	}
	if len(posts) != 1 || hits != 3 {
		t.Fatalf("게시물 %d개, 요청 %d회입니다", len(posts), hits)
	}
}

func TestGetDoesNotRetryClientError(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client, _ := New(server.Client(), Options{BaseURL: server.URL, Retries: 3})
	if _, err := client.PostsAfter(context.Background(), 0, "", 1); err == nil {
		t.Fatal("404는 실패로 처리해야 합니다")
	}
	if hits != 1 {
		t.Fatalf("요청이 %d회입니다. 404는 재시도하지 않아야 합니다", hits)
	}
}
