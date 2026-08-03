package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// joinTicket은 수집 노드가 중앙에 붙는 데 필요한 값을 한 덩어리로 묶은 것입니다.
//
// 세 값을 따로 옮겨 적게 하면 하나를 빠뜨리거나 줄바꿈에서 잘립니다.
// 그러면 설치는 성공했다고 나오고 서비스만 조용히 못 붙습니다. 하나로
// 묶어 통째로 붙여 넣게 합니다.
//
// 안에 비밀이 들어 있습니다. 감춘 것이 아니라 옮기기 쉽게 만든 것뿐이라,
// 토큰과 같은 무게로 다루십시오.
type joinTicket struct {
	ControlURL string `json:"control_url"`
	Token      string `json:"token"`
	DBPassword string `json:"db_password"`
}

func (t joinTicket) encode() (string, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeJoinTicket(blob string) (joinTicket, error) {
	var ticket joinTicket

	blob = strings.TrimSpace(blob)
	raw, err := base64.RawURLEncoding.DecodeString(blob)
	if err != nil {
		return ticket, fmt.Errorf("-join 값을 읽지 못했습니다. 중앙이 알려 준 것을 그대로 붙여 넣으십시오: %w", err)
	}
	if err := json.Unmarshal(raw, &ticket); err != nil {
		return ticket, fmt.Errorf("-join 값의 내용을 해석하지 못했습니다: %w", err)
	}

	if ticket.ControlURL == "" || ticket.Token == "" || ticket.DBPassword == "" {
		return ticket, fmt.Errorf("-join 값에 빠진 것이 있습니다. 중앙에서 다시 받으십시오")
	}
	// 주소가 성한지 여기서 봅니다. 뒤에서 보면 꾸러미를 다 깔고 나서
	// 실패합니다.
	if _, err := url.Parse(ticket.ControlURL); err != nil {
		return ticket, fmt.Errorf("-join 값의 중앙 주소가 잘못되었습니다: %w", err)
	}
	return ticket, nil
}

// joinBlob은 이 중앙 노드에 붙는 데 쓸 값을 냅니다.
func (p *installPlan) joinBlob() (string, error) {
	return joinTicket{
		ControlURL: "http://" + p.bind,
		Token:      p.token,
		DBPassword: p.dbPassword,
	}.encode()
}
