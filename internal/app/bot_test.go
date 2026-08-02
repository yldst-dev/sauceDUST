package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"saucedust/internal/domain"
)

type fakeGateway struct {
	mu sync.Mutex

	queued   [][]domain.BotUpdate
	sent     []domain.BotReply
	files    map[string][]byte
	fileErr  error
	pollErr  error
	pollHits int
	offsets  []int64
}

func newGateway(updates ...domain.BotUpdate) *fakeGateway {
	return &fakeGateway{
		queued: [][]domain.BotUpdate{updates},
		files:  map[string][]byte{"photo-1": []byte("image-bytes")},
	}
}

func (f *fakeGateway) GetUpdates(ctx context.Context, offset int64) ([]domain.BotUpdate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.pollHits++
	f.offsets = append(f.offsets, offset)

	if f.pollErr != nil && f.pollHits == 1 {
		return nil, f.pollErr
	}
	if len(f.queued) == 0 {
		// 더 줄 것이 없으면 컨텍스트가 끝날 때까지 기다리는 척합니다.
		<-ctx.Done()
		return nil, ctx.Err()
	}
	batch := f.queued[0]
	f.queued = f.queued[1:]
	return batch, nil
}

func (f *fakeGateway) DownloadFile(_ context.Context, fileID string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fileErr != nil {
		return nil, f.fileErr
	}
	data, ok := f.files[fileID]
	if !ok {
		return nil, errors.New("없는 파일입니다")
	}
	return data, nil
}

func (f *fakeGateway) SendMessage(_ context.Context, msg domain.BotReply) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, msg)
	return nil
}

func (f *fakeGateway) replies() []domain.BotReply {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]domain.BotReply(nil), f.sent...)
}

type fakeSearcher struct {
	result *SearchResult
	err    error
	calls  int
	mu     sync.Mutex
}

func (f *fakeSearcher) ByImage(context.Context, []byte) (*SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.result, f.err
}

func imageMessage(chatID, sender int64) domain.BotUpdate {
	return domain.BotUpdate{
		ID: 1,
		Message: &domain.BotMessage{
			ChatID: chatID, SenderID: sender, FileID: "photo-1",
		},
	}
}

func runBot(t *testing.T, cfg BotConfig, gw *fakeGateway, search ImageSearcher) {
	t.Helper()

	bot, err := NewBot(cfg, gw, search, quietLogger())
	if err != nil {
		t.Fatalf("봇 생성 실패: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := bot.Run(ctx); err != nil {
		t.Fatalf("실행 실패: %v", err)
	}
}

func hit(postID int64, score float32, distance int, exact bool) Hit {
	return Hit{
		Image: domain.Image{
			SourceSite: "danbooru", SourcePostID: postID,
			CanonicalURL: "https://danbooru.example/posts/" + itoa(postID),
			Rating:       "g", Tags: []string{"1girl", "solo"},
			ArtistTags: []string{"someone"},
		},
		Score: score, HashDistance: distance, Exact: exact,
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

func TestBotAnswersImageWithSearchResult(t *testing.T) {
	gw := newGateway(imageMessage(100, 7))
	search := &fakeSearcher{result: &SearchResult{
		ExactMatch: true,
		Hits:       []Hit{hit(12345, 0.98, 0, true)},
	}}

	runBot(t, BotConfig{}, gw, search)

	replies := gw.replies()
	if len(replies) != 1 {
		t.Fatalf("답장이 %d건입니다", len(replies))
	}
	if replies[0].ChatID != 100 {
		t.Fatalf("대화방이 %d입니다", replies[0].ChatID)
	}
	if !strings.Contains(replies[0].Text, "원본을 찾았습니다") {
		t.Fatalf("답장 내용이 %q입니다", replies[0].Text)
	}
	if !strings.Contains(replies[0].Text, "12345") {
		t.Fatal("게시물 번호가 빠졌습니다")
	}
	if len(replies[0].Buttons) == 0 {
		t.Fatal("원본 링크 단추가 없습니다")
	}
}

// 결과가 없어도 사용자에게 알려야 합니다. 조용히 있으면 안 됩니다.
func TestBotAnswersWhenNothingFound(t *testing.T) {
	gw := newGateway(imageMessage(100, 7))
	search := &fakeSearcher{result: &SearchResult{}}

	runBot(t, BotConfig{}, gw, search)

	replies := gw.replies()
	if len(replies) != 1 || !strings.Contains(replies[0].Text, "찾지 못했습니다") {
		t.Fatalf("답장이 %+v입니다", replies)
	}
	if len(replies[0].Buttons) != 0 {
		t.Fatal("결과가 없는데 단추가 붙었습니다")
	}
}

func TestBotAnswersHelp(t *testing.T) {
	gw := newGateway(domain.BotUpdate{
		ID:      1,
		Message: &domain.BotMessage{ChatID: 100, SenderID: 7, Text: "/start"},
	})
	search := &fakeSearcher{}

	runBot(t, BotConfig{}, gw, search)

	if search.calls != 0 {
		t.Fatal("명령어에 검색을 돌리면 안 됩니다")
	}
	replies := gw.replies()
	if len(replies) != 1 || !strings.Contains(replies[0].Text, "그림을 보내면") {
		t.Fatalf("답장이 %+v입니다", replies)
	}
}

func TestBotAsksForImageWhenTextOnly(t *testing.T) {
	gw := newGateway(domain.BotUpdate{
		ID:      1,
		Message: &domain.BotMessage{ChatID: 100, SenderID: 7, Text: "안녕"},
	})
	search := &fakeSearcher{}

	runBot(t, BotConfig{}, gw, search)

	replies := gw.replies()
	if len(replies) != 1 || !strings.Contains(replies[0].Text, "보내 주십시오") {
		t.Fatalf("답장이 %+v입니다", replies)
	}
}

// 허용 목록이 있으면 그 사람만 쓸 수 있어야 합니다.
// 토큰을 아는 누구나 검색하면 자원이 낭비되고 수집 대상이 노출됩니다.
func TestBotRejectsUnknownSender(t *testing.T) {
	gw := newGateway(imageMessage(100, 999))
	search := &fakeSearcher{result: &SearchResult{Hits: []Hit{hit(1, 0.9, 0, true)}}}

	runBot(t, BotConfig{AllowedUsers: []int64{7, 8}}, gw, search)

	if search.calls != 0 {
		t.Fatal("허용되지 않은 사용자에게 검색을 돌렸습니다")
	}
	replies := gw.replies()
	if len(replies) != 1 || !strings.Contains(replies[0].Text, "허가된 사용자") {
		t.Fatalf("답장이 %+v입니다", replies)
	}
}

func TestBotAllowsListedSender(t *testing.T) {
	gw := newGateway(imageMessage(100, 8))
	search := &fakeSearcher{result: &SearchResult{Hits: []Hit{hit(1, 0.9, 0, true)}}}

	runBot(t, BotConfig{AllowedUsers: []int64{7, 8}}, gw, search)

	if search.calls != 1 {
		t.Fatalf("검색이 %d번 불렸습니다", search.calls)
	}
}

// 검색이 실패해도 사용자는 답을 받아야 합니다.
func TestBotAnswersOnSearchFailure(t *testing.T) {
	gw := newGateway(imageMessage(100, 7))
	search := &fakeSearcher{err: errors.New("워커가 죽었습니다")}

	runBot(t, BotConfig{}, gw, search)

	replies := gw.replies()
	if len(replies) != 1 || !strings.Contains(replies[0].Text, "검색에 실패") {
		t.Fatalf("답장이 %+v입니다", replies)
	}
}

func TestBotAnswersOnDownloadFailure(t *testing.T) {
	gw := newGateway(imageMessage(100, 7))
	gw.fileErr = errors.New("파일이 사라졌습니다")
	search := &fakeSearcher{}

	runBot(t, BotConfig{}, gw, search)

	if search.calls != 0 {
		t.Fatal("이미지를 못 받았는데 검색을 돌렸습니다")
	}
	replies := gw.replies()
	if len(replies) != 1 || !strings.Contains(replies[0].Text, "받지 못했습니다") {
		t.Fatalf("답장이 %+v입니다", replies)
	}
}

// 처리에 실패해도 offset은 올려야 합니다.
// 안 그러면 같은 메시지에 영원히 걸려 다른 요청을 못 받습니다.
func TestBotAdvancesOffsetEvenOnFailure(t *testing.T) {
	gw := newGateway(domain.BotUpdate{
		ID:      42,
		Message: &domain.BotMessage{ChatID: 1, SenderID: 7, FileID: "없는파일"},
	})
	search := &fakeSearcher{}

	runBot(t, BotConfig{}, gw, search)

	gw.mu.Lock()
	defer gw.mu.Unlock()
	if len(gw.offsets) < 2 {
		t.Fatalf("조회가 %d번뿐입니다", len(gw.offsets))
	}
	if gw.offsets[1] != 43 {
		t.Fatalf("두 번째 조회 offset이 %d입니다. 43을 기대했습니다", gw.offsets[1])
	}
}

// 통로가 잠깐 막혀도 봇이 죽으면 안 됩니다.
func TestBotSurvivesPollFailure(t *testing.T) {
	gw := newGateway(imageMessage(100, 7))
	gw.pollErr = errors.New("연결이 끊겼습니다")
	search := &fakeSearcher{result: &SearchResult{Hits: []Hit{hit(1, 0.9, 0, true)}}}

	runBot(t, BotConfig{ErrorBackoff: 10 * time.Millisecond}, gw, search)

	if search.calls != 1 {
		t.Fatalf("복구 후 검색이 %d번입니다", search.calls)
	}
}

func TestNewBotRejectsMissingDeps(t *testing.T) {
	if _, err := NewBot(BotConfig{}, nil, &fakeSearcher{}, quietLogger()); err == nil {
		t.Error("통로 없이 만들면 안 됩니다")
	}
	if _, err := NewBot(BotConfig{}, newGateway(), nil, quietLogger()); err == nil {
		t.Error("검색기 없이 만들면 안 됩니다")
	}
}

// 답장 형식 검증 ------------------------------------------------------------

func TestFormatShowsRunnersUp(t *testing.T) {
	reply := FormatSearchReply(1, &SearchResult{
		Hits: []Hit{
			hit(100, 0.98, 0, true),
			hit(200, 0.81, 20, false),
			hit(300, 0.79, 25, false),
		},
	})

	if !strings.Contains(reply.Text, "다음 후보") {
		t.Fatal("다음 후보 목록이 없습니다")
	}
	for _, want := range []string{"100", "200", "300"} {
		if !strings.Contains(reply.Text, want) {
			t.Errorf("게시물 %s가 빠졌습니다", want)
		}
	}
}

func TestFormatMarksUncertainMatch(t *testing.T) {
	reply := FormatSearchReply(1, &SearchResult{Hits: []Hit{hit(100, 0.6, 30, false)}})
	if !strings.Contains(reply.Text, "원본이 아닐 수 있습니다") {
		t.Fatalf("확신 없는 결과를 그렇게 표시해야 합니다: %q", reply.Text)
	}
}

// 빈 URL을 단추로 넣으면 텔레그램이 메시지 전체를 거부합니다.
func TestFormatSkipsEmptyURLs(t *testing.T) {
	reply := FormatSearchReply(1, &SearchResult{Hits: []Hit{{
		Image: domain.Image{
			SourceSite: "danbooru", SourcePostID: 1,
			CanonicalURL: "https://danbooru.example/posts/1",
			SourceURL:    "", PreviewURL: "not-a-url",
		},
		Score: 0.9, HashDistance: 0, Exact: true,
	}}})

	if len(reply.Buttons) != 1 {
		t.Fatalf("단추가 %d개입니다. 올바른 URL 1개만 남아야 합니다: %+v",
			len(reply.Buttons), reply.Buttons)
	}
	for _, b := range reply.Buttons {
		if !strings.HasPrefix(b.URL, "https://") {
			t.Fatalf("잘못된 URL이 단추가 됐습니다: %q", b.URL)
		}
	}
}

func TestReplyTrimsLongText(t *testing.T) {
	long := domain.BotReply{ChatID: 1, Text: strings.Repeat("가", 5000)}
	trimmed := long.Trimmed()

	if len(trimmed.Text) > 3900 {
		t.Fatalf("길이가 %d입니다", len(trimmed.Text))
	}
	if !strings.HasSuffix(trimmed.Text, "…") {
		t.Fatal("잘렸다는 표시가 없습니다")
	}
	// 룬 경계를 지켜야 글자가 깨지지 않습니다.
	for _, r := range trimmed.Text {
		if r == '�' {
			t.Fatal("글자가 깨졌습니다")
		}
	}
}

func TestReplyKeepsShortText(t *testing.T) {
	short := domain.BotReply{ChatID: 1, Text: "짧은 답장"}
	if short.Trimmed().Text != "짧은 답장" {
		t.Fatal("짧은 글은 그대로 두어야 합니다")
	}
}

func TestRatingLabel(t *testing.T) {
	cases := map[string]string{
		"g": "전체 이용가", "s": "다소 민감", "q": "선정적", "e": "노골적",
		"": "등급 없음", "이상한값": "이상한값",
	}
	for in, want := range cases {
		if got := domain.RatingLabel(in); got != want {
			t.Errorf("%q에서 %q를 얻었습니다. %q를 기대했습니다", in, got, want)
		}
	}
}

func TestIsCommandMatchesVariants(t *testing.T) {
	cases := map[string]bool{
		"/start": true, "/start ": true, "/start@saucebot": true,
		"/started": false, "start": false, "": false,
	}
	for text, want := range cases {
		msg := domain.BotMessage{Text: text}
		if got := msg.IsCommand("/start"); got != want {
			t.Errorf("%q에서 %v를 얻었습니다. %v를 기대했습니다", text, got, want)
		}
	}
}
