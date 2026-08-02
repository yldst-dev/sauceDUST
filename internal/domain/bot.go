package domain

import (
	"fmt"
	"strings"
)

// 봇이 주고받는 값들입니다. 어느 메신저인지는 도메인이 알 필요가 없어
// 전송 형식이 아니라 뜻만 담습니다.

type BotUpdate struct {
	ID      int64
	Message *BotMessage
}

type BotMessage struct {
	ChatID   int64
	SenderID int64
	Sender   string
	Text     string
	Caption  string
	// FileID가 비어 있으면 이미지가 오지 않은 것입니다.
	FileID string
}

func (m BotMessage) HasImage() bool { return m.FileID != "" }

func (m BotMessage) IsCommand(name string) bool {
	text := strings.ToLower(strings.TrimSpace(m.Text))
	return text == name || strings.HasPrefix(text, name+" ") || strings.HasPrefix(text, name+"@")
}

type BotButton struct {
	Label string
	URL   string
}

type BotReply struct {
	ChatID         int64
	Text           string
	Buttons        []BotButton
	DisablePreview bool
}

// 텔레그램 메시지 길이 상한보다 여유를 둡니다.
const botTextLimit = 3900

// ellipsis는 잘렸다는 표시입니다. 이 크기까지 미리 빼고 자릅니다.
const ellipsis = "…"

func (r BotReply) Trimmed() BotReply {
	if len(r.Text) <= botTextLimit {
		return r
	}
	// 글자 중간에서 자르면 깨지므로 룬 경계까지만 담습니다.
	// 한 글자 지울 때마다 문자열을 다시 만들면 긴 답에서 제곱으로 느려지므로
	// 앞에서부터 한 번만 훑습니다.
	// range가 주는 i는 항상 글자의 첫 바이트라 그 자리에서 자르면 깨지지 않습니다.
	budget := botTextLimit - len(ellipsis)
	end := 0
	for i := range r.Text {
		if i > budget {
			break
		}
		end = i
	}
	r.Text = r.Text[:end] + ellipsis
	return r
}

// RatingLabel은 등급 약자를 사람이 읽을 말로 바꿉니다.
func RatingLabel(rating string) string {
	switch strings.ToLower(strings.TrimSpace(rating)) {
	case "g", "general":
		return "전체 이용가"
	case "s", "sensitive":
		return "다소 민감"
	case "q", "questionable":
		return "선정적"
	case "e", "explicit":
		return "노골적"
	case "":
		return "등급 없음"
	default:
		return rating
	}
}

// PostLabel은 검색 결과 한 건을 한 줄로 요약합니다.
func PostLabel(img Image) string {
	label := fmt.Sprintf("%s #%d", img.SourceSite, img.SourcePostID)
	if len(img.ArtistTags) > 0 {
		label += " · " + strings.Join(img.ArtistTags, ", ")
	}
	return label
}
