package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"saucedust/internal/domain"
)

// BotGateway는 메신저와 주고받는 통로입니다.
// 텔레그램이 기본 구현이지만 유스케이스는 어느 메신저인지 모릅니다.
type BotGateway interface {
	GetUpdates(ctx context.Context, offset int64) ([]domain.BotUpdate, error)
	DownloadFile(ctx context.Context, fileID string) ([]byte, error)
	SendMessage(ctx context.Context, msg domain.BotReply) error
}

// ImageSearcher는 이미지로 원본을 찾습니다.
// control 노드에서는 로컬 검색기를, 다른 노드에서는 중앙 API 클라이언트를 꽂습니다.
type ImageSearcher interface {
	ByImage(ctx context.Context, image []byte) (*SearchResult, error)
}

type BotConfig struct {
	// AllowedUsers가 비어 있지 않으면 그 사람들만 쓸 수 있습니다.
	// 토큰을 아는 누구나 검색하는 것을 막습니다.
	AllowedUsers []int64
	// MaxImageBytes보다 큰 이미지는 받지 않습니다.
	MaxImageBytes int64
	// ErrorBackoff는 통로가 막혔을 때 쉬는 시간입니다.
	ErrorBackoff time.Duration
}

// Bot은 이미지를 받아 원본을 찾아 돌려주는 유스케이스입니다.
type Bot struct {
	cfg     BotConfig
	gateway BotGateway
	search  ImageSearcher
	log     *slog.Logger
	allowed map[int64]bool
	offset  int64
}

func NewBot(cfg BotConfig, gateway BotGateway, search ImageSearcher, log *slog.Logger) (*Bot, error) {
	switch {
	case gateway == nil:
		return nil, errors.New("메신저 통로가 없습니다")
	case search == nil:
		return nil, errors.New("검색기가 없습니다")
	}
	if cfg.MaxImageBytes <= 0 {
		cfg.MaxImageBytes = 32 << 20
	}
	if cfg.ErrorBackoff <= 0 {
		cfg.ErrorBackoff = 5 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}

	allowed := make(map[int64]bool, len(cfg.AllowedUsers))
	for _, id := range cfg.AllowedUsers {
		allowed[id] = true
	}

	return &Bot{cfg: cfg, gateway: gateway, search: search, log: log, allowed: allowed}, nil
}

func (b *Bot) Run(ctx context.Context) error {
	if len(b.allowed) == 0 {
		b.log.Warn("봇 사용자 제한이 없습니다. 토큰을 아는 누구나 검색할 수 있습니다")
	}
	b.log.Info("봇을 시작합니다")

	for {
		if ctx.Err() != nil {
			return nil
		}

		updates, err := b.gateway.GetUpdates(ctx, b.offset)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			b.log.Warn("메시지를 받지 못했습니다", slog.String("error", err.Error()))
			if !sleepCtx(ctx, b.cfg.ErrorBackoff) {
				return nil
			}
			continue
		}

		for _, update := range updates {
			// 처리에 실패해도 offset은 올립니다. 그러지 않으면 같은 메시지에
			// 영원히 걸려 다른 요청을 못 받습니다.
			if update.ID >= b.offset {
				b.offset = update.ID + 1
			}
			if update.Message == nil {
				continue
			}
			b.handle(ctx, *update.Message)
		}
	}
}

func (b *Bot) handle(ctx context.Context, msg domain.BotMessage) {
	if !b.permitted(msg) {
		b.log.Info("허용되지 않은 사용자입니다",
			slog.Int64("sender", msg.SenderID), slog.String("name", msg.Sender))
		b.reply(ctx, domain.BotReply{
			ChatID: msg.ChatID,
			Text:   "This bot is only available to approved users.",
		})
		return
	}

	switch {
	case msg.IsCommand("/start"), msg.IsCommand("/help"):
		b.reply(ctx, domain.BotReply{ChatID: msg.ChatID, Text: helpText})
		return
	case !msg.HasImage():
		b.reply(ctx, domain.BotReply{
			ChatID: msg.ChatID,
			Text:   "Send the picture you want to find as a photo or an image file.",
		})
		return
	}

	image, err := b.gateway.DownloadFile(ctx, msg.FileID)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		b.log.Warn("이미지를 받지 못했습니다", slog.String("error", err.Error()))
		b.reply(ctx, domain.BotReply{
			ChatID: msg.ChatID, Text: "Could not download the image. Please send it again.",
		})
		return
	}
	if int64(len(image)) > b.cfg.MaxImageBytes {
		b.reply(ctx, domain.BotReply{
			ChatID: msg.ChatID, Text: "The image is too large.",
		})
		return
	}

	b.reply(ctx, domain.BotReply{ChatID: msg.ChatID, Text: "Searching..."})

	result, err := b.search.ByImage(ctx, image)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		b.log.Warn("검색에 실패했습니다", slog.String("error", err.Error()))
		b.reply(ctx, domain.BotReply{
			ChatID: msg.ChatID, Text: "Search failed. Please try again in a moment.",
		})
		return
	}

	b.reply(ctx, FormatSearchReply(msg.ChatID, result))
}

func (b *Bot) permitted(msg domain.BotMessage) bool {
	if len(b.allowed) == 0 {
		return true
	}
	return b.allowed[msg.SenderID]
}

func (b *Bot) reply(ctx context.Context, msg domain.BotReply) {
	if ctx.Err() != nil {
		return
	}
	if err := b.gateway.SendMessage(ctx, msg.Trimmed()); err != nil {
		b.log.Warn("답장을 보내지 못했습니다", slog.String("error", err.Error()))
	}
}

const helpText = `Send a picture to find where it came from.

A photo or an image file both work.
Cropped or recompressed copies can still match.`

// FormatSearchReply는 검색 결과를 사람이 읽을 답장으로 바꿉니다.
//
// 순수 함수입니다. 통로도 저장소도 건드리지 않으므로 결과 표현을 바꿀 때
// 이것만 보면 됩니다.
func FormatSearchReply(chatID int64, result *SearchResult) domain.BotReply {
	reply := domain.BotReply{ChatID: chatID, DisablePreview: false}

	if result == nil || len(result.Hits) == 0 {
		reply.Text = "No matching picture was found.\nIt may not have been collected yet."
		return reply
	}

	top := result.Hits[0]
	var b strings.Builder

	if top.Exact {
		b.WriteString("Found the original.\n\n")
	} else {
		b.WriteString("Similar picture. It may not be the original.\n\n")
	}

	b.WriteString(fmt.Sprintf("%s\n", domain.PostLabel(top.Image)))
	b.WriteString(fmt.Sprintf("Match %.1f%%", top.Score*100))
	if top.HashDistance >= 0 {
		b.WriteString(fmt.Sprintf(" · hash distance %d bits", top.HashDistance))
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("Rating %s\n", ratingText(top.Image.Rating)))

	if tags := visibleTags(top.Image.Tags, 12); tags != "" {
		b.WriteString("\n" + tags + "\n")
	}

	if others := runnersUp(result.Hits); others != "" {
		b.WriteString("\nOther candidates\n" + others)
	}

	reply.Text = b.String()
	reply.Buttons = replyButtons(top.Image)
	return reply
}

func visibleTags(tags []string, limit int) string {
	if len(tags) == 0 {
		return ""
	}
	if len(tags) > limit {
		return strings.Join(tags[:limit], ", ") + fmt.Sprintf(" and %d more", len(tags)-limit)
	}
	return strings.Join(tags, ", ")
}

func runnersUp(hits []Hit) string {
	if len(hits) < 2 {
		return ""
	}
	var b strings.Builder
	for _, hit := range hits[1:] {
		b.WriteString(fmt.Sprintf("  %s (%.1f%%)\n",
			domain.PostLabel(hit.Image), hit.Score*100))
	}
	return b.String()
}

// replyButtons는 URL이 있는 것만 단추로 만듭니다.
// 빈 URL을 넣으면 텔레그램이 메시지 전체를 거부합니다.
func replyButtons(img domain.Image) []domain.BotButton {
	candidates := []domain.BotButton{
		{Label: "Original post", URL: img.CanonicalURL},
		{Label: "Artist source", URL: img.SourceURL},
		{Label: "Preview", URL: img.PreviewURL},
	}

	out := make([]domain.BotButton, 0, len(candidates))
	for _, b := range candidates {
		if strings.HasPrefix(b.URL, "http://") || strings.HasPrefix(b.URL, "https://") {
			out = append(out, b)
		}
	}
	return out
}

func ratingText(rating string) string {
	switch strings.ToLower(strings.TrimSpace(rating)) {
	case "g", "general":
		return "General"
	case "s", "sensitive":
		return "Sensitive"
	case "q", "questionable":
		return "Questionable"
	case "e", "explicit":
		return "Explicit"
	case "":
		return "Unrated"
	default:
		return rating
	}
}
