package app

import (
	"context"
	"testing"
	"time"
)

// 상한에 걸려 잘라 냈으면 워터마크를 올리면 안 됩니다.
//
// 따라잡기는 최신 200개를 받아 옵니다. 상한 때문에 앞의 다섯 개만
// 처리했는데 워터마크를 200개 전부의 최댓값으로 올리면, 나머지 195개는
// 다시는 안 봅니다. 워터마크는 되돌아가지 않으므로 영구 구멍입니다.
//
// -limit은 문서가 첫 실행으로 안내하는 명령입니다. 처음 써 보는 사람이
// 바로 밟는 길입니다.
func TestCatchupDoesNotSkipTrimmedPosts(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxImages = 5
	cfg.BackfillWorkers = 0

	lease := &fakeLease{frontier: 1, watermark: 1000}
	source := &rangeSource{latest: 1200}
	source.fakeSource.failURL = map[string]error{}
	for i := int64(1001); i <= 1200; i++ {
		source.newPosts = append(source.newPosts, makeSourcePost(i))
	}

	crawler, _ := newCrawler(t, cfg, source, lease)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	crawler.Run(ctx)

	got := lease.snapshot()
	saved := crawler.Saved()
	if saved != cfg.MaxImages {
		t.Fatalf("%d장에서 멈췄습니다. %d장을 기대했습니다", saved, cfg.MaxImages)
	}

	for _, mark := range got.advanced {
		if mark > 1000+saved {
			t.Errorf("워터마크를 %d까지 올렸는데 %d장만 처리했습니다."+
				" %d번부터 %d번까지 %d개가 영영 빠집니다",
				mark, saved, 1000+saved+1, mark, mark-(1000+saved))
		}
	}
}

// 따라잡기가 저장한 것도 세야 합니다.
//
// 세지 않으면 -limit이 따라잡기만으로는 절대 멈추지 않고, 자를 때 쓰는
// 남은 몫이 언제나 상한 그대로라 실제로는 상한을 몇 배로 넘길 수 있습니다.
func TestCatchupSavesAreCounted(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxImages = 5
	cfg.BackfillWorkers = 0

	lease := &fakeLease{frontier: 1, watermark: 1000}
	source := &rangeSource{latest: 1200}
	source.fakeSource.failURL = map[string]error{}
	for i := int64(1001); i <= 1200; i++ {
		source.newPosts = append(source.newPosts, makeSourcePost(i))
	}

	crawler, h := newCrawler(t, cfg, source, lease)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	crawler.Run(ctx)

	if got := crawler.Saved(); got == 0 {
		t.Errorf("싱크에 %d건이 들어갔는데 Saved()가 0입니다. 따라잡기를 세지 않습니다",
			h.sink.count())
	}
	if got := crawler.Saved(); got != int64(h.sink.count()) {
		t.Errorf("Saved()가 %d인데 실제로 들어간 것은 %d건입니다", got, h.sink.count())
	}
}

// 상한을 채우면 따라잡기만으로도 멈춰야 합니다.
func TestCatchupAloneRespectsTheLimit(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxImages = 5
	cfg.BackfillWorkers = 0
	cfg.PollEvery = 5 * time.Millisecond

	lease := &fakeLease{frontier: 1, watermark: 1000}
	source := &rangeSource{latest: 2000}
	source.fakeSource.failURL = map[string]error{}
	for i := int64(1001); i <= 1200; i++ {
		source.newPosts = append(source.newPosts, makeSourcePost(i))
	}

	crawler, h := newCrawler(t, cfg, source, lease)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	crawler.Run(ctx)

	if h.sink.count() > int(cfg.MaxImages)*2 {
		t.Errorf("상한이 %d인데 %d건을 저장했습니다", cfg.MaxImages, h.sink.count())
	}
}
