package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"saucedust/internal/adapter/danbooru"
	"saucedust/internal/adapter/netpath"
	"saucedust/internal/domain"
)

// TestMeasureRealImageSizes는 실제 Danbooru 이미지를 받아 와 크기를 잽니다.
//
// 1천만 장을 모으겠다고 정하기 전에 디스크가 얼마나 필요한지 알아야 합니다.
// 어림짐작 대신 실제 값으로 계산합니다.
//
// 바깥 네트워크를 쓰므로 평소 시험에서는 돌지 않습니다.
// SAUCEDUST_TEST_LIVE=1을 넣으면 돕니다.
func TestMeasureRealImageSizes(t *testing.T) {
	if os.Getenv("SAUCEDUST_TEST_LIVE") == "" {
		t.Skip("바깥 네트워크가 필요합니다. SAUCEDUST_TEST_LIVE=1을 넣으십시오")
	}

	chain, err := netpath.New(netpath.Options{
		Order:          []domain.NetMode{domain.NetDirect, domain.NetFragment},
		FragmentParts:  3,
		RequestTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	client, err := danbooru.New(chain, danbooru.Options{
		UserAgent:     "saucedust/0.2 (capacity measurement)",
		RatePerSecond: 3,
		Burst:         3,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	latest, err := client.LatestPostID(ctx)
	if err != nil {
		t.Fatalf("최신 게시물 번호를 받지 못했습니다: %v", err)
	}
	t.Logf("현재 최신 게시물 번호: %d", latest)

	// 모델 비교에 쓰려면 후보가 많아야 순위가 의미를 갖습니다.
	want := 60
	if raw := os.Getenv("SAUCEDUST_TEST_SAMPLE_COUNT"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			want = n
		}
	}

	// Danbooru는 한 번에 200개까지만 줍니다. 필요한 만큼 넘겨 가며 받습니다.
	var posts []domain.SourcePost
	cursor := latest - int64(want)*3
	for len(posts) < want {
		page, err := client.PostsAfter(ctx, cursor, "rating:g", 200)
		if err != nil {
			t.Fatalf("게시물 목록을 받지 못했습니다: %v", err)
		}
		if len(page) == 0 {
			break
		}
		posts = append(posts, page...)
		cursor = page[len(page)-1].PostID
	}
	if len(posts) == 0 {
		t.Fatal("게시물이 하나도 오지 않았습니다")
	}
	if len(posts) > want {
		posts = posts[:want]
	}
	t.Logf("게시물 %d건을 받았습니다", len(posts))

	var (
		sizes    []int
		total    int64
		skipped  int
		failures int
	)
	for _, post := range posts {
		if post.SkipReason() != "" {
			skipped++
			continue
		}
		data, err := client.Download(ctx, post.DownloadURL(), 32<<20)
		if err != nil {
			failures++
			continue
		}
		sizes = append(sizes, len(data))
		total += int64(len(data))

		// 축소본 크기는 Python 쪽에서 재야 합니다. 받은 이미지를 넘겨 줍니다.
		if dir := os.Getenv("SAUCEDUST_TEST_DUMP_DIR"); dir != "" {
			name := filepath.Join(dir, fmt.Sprintf("%d.bin", post.PostID))
			if err := os.WriteFile(name, data, 0o600); err != nil {
				t.Fatalf("이미지를 저장하지 못했습니다: %v", err)
			}
		}
	}

	if len(sizes) < 10 {
		t.Fatalf("잰 것이 %d장뿐입니다. 건너뜀 %d, 실패 %d", len(sizes), skipped, failures)
	}
	sort.Ints(sizes)

	avg := total / int64(len(sizes))
	t.Logf("잰 이미지 %d장 (건너뜀 %d, 실패 %d)", len(sizes), skipped, failures)
	t.Logf("내려받은 크기: 평균 %d KB, 중앙값 %d KB, 최소 %d KB, 최대 %d KB",
		avg>>10, int64(sizes[len(sizes)/2])>>10, int64(sizes[0])>>10,
		int64(sizes[len(sizes)-1])>>10)
	t.Logf("1천만 장 내려받기: %.1f TB (저장하지는 않습니다)",
		float64(avg)*1e7/(1<<40))
}
