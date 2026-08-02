package domain

import (
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

// 아래 함수들은 모두 바깥에서 온 바이트를 받습니다. 벡터는 워커가 base64로
// 보내고 PostgreSQL에 bytea로 저장됩니다. 해시는 DB 문자열입니다. 게시물은
// Danbooru 응답입니다. 어느 것도 이쪽에서 만든 값이 아니므로, 어떤 입력에도
// 터지지 않는지 무작위로 밀어 넣어 봅니다.

func FuzzDecodeVector(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0x00, 0x00, 0x80, 0x3f})
	f.Add([]byte{1, 2, 3})
	f.Add([]byte{0xff, 0xff, 0xff, 0x7f})

	f.Fuzz(func(t *testing.T, raw []byte) {
		values, err := DecodeVector(raw)
		if err != nil {
			if len(raw)%4 == 0 {
				t.Fatalf("4의 배수인데 거부했습니다: %d바이트, %v", len(raw), err)
			}
			return
		}
		if len(values) != len(raw)/4 {
			t.Fatalf("%d바이트에서 값 %d개가 나왔습니다", len(raw), len(values))
		}

		// 다시 인코딩하면 원래 바이트가 나와야 합니다. NaN은 비트가
		// 여러 가지라 이 규칙에서 빼 둡니다.
		var hasNaN bool
		for _, v := range values {
			if math.IsNaN(float64(v)) {
				hasNaN = true
			}
		}
		if !hasNaN {
			again := EncodeVector(values)
			if string(again) != string(raw) {
				t.Fatalf("왕복이 어긋납니다: %x → %x", raw, again)
			}
		}
	})
}

func FuzzNormalize(f *testing.F) {
	f.Add([]byte{0x00, 0x00, 0x80, 0x3f, 0x00, 0x00, 0x00, 0x40})
	f.Add(make([]byte, 16))

	f.Fuzz(func(t *testing.T, raw []byte) {
		values, err := DecodeVector(raw)
		if err != nil {
			return
		}
		Normalize(values)

		// 원래 값이 멀쩡했다면 정규화 뒤에도 멀쩡해야 합니다.
		// 0으로 나눠 NaN을 만들어 내면 안 됩니다.
		clean := true
		for _, v := range values {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				clean = false
			}
		}
		if clean {
			return
		}
		before, _ := DecodeVector(raw)
		for _, v := range before {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return
			}
		}
		t.Fatalf("멀쩡한 값에서 NaN이나 무한대를 만들었습니다: %x → %v", raw, values)
	})
}

func FuzzHammingDistance(f *testing.F) {
	f.Add("f0f0f0f0f0f0f0f0", "0f0f0f0f0f0f0f0f")
	f.Add("", "")
	f.Add("zz", "zz")
	f.Add("abc", "abc")

	f.Fuzz(func(t *testing.T, a, b string) {
		distance, err := HammingDistance(a, b)
		if err != nil {
			return
		}
		if distance < 0 || distance > HashBits(a) {
			t.Fatalf("%q와 %q의 거리가 %d입니다. 0에서 %d 사이여야 합니다",
				a, b, distance, HashBits(a))
		}
		// 방향을 바꿔도 같아야 합니다.
		reverse, err := HammingDistance(b, a)
		if err != nil || reverse != distance {
			t.Fatalf("방향에 따라 %d와 %d로 다릅니다", distance, reverse)
		}
		// 자기 자신과의 거리는 0입니다.
		if same, err := HammingDistance(a, a); err == nil && same != 0 {
			t.Fatalf("%q와 자기 자신의 거리가 %d입니다", a, same)
		}
	})
}

func FuzzSameImageThreshold(f *testing.F) {
	f.Add("f0f0f0f0f0f0f0f0")
	f.Add("")
	f.Add(strings.Repeat("a", 1024))

	f.Fuzz(func(t *testing.T, hash string) {
		threshold := SameImageThreshold(hash)
		if threshold < 1 {
			t.Fatalf("%q의 임계값이 %d입니다. 1 이상이어야 합니다", hash, threshold)
		}
		// 임계값이 전체 비트 수를 넘으면 모든 그림이 같다고 하게 됩니다.
		if bits := HashBits(hash); bits > 0 && threshold > bits {
			t.Fatalf("임계값 %d가 전체 비트 %d를 넘습니다", threshold, bits)
		}
	})
}

// DownloadURL은 Danbooru가 준 주소를 그대로 다룹니다.
// 어떤 문자열이 와도 허용한 확장자만 통과해야 합니다.
func FuzzDownloadURL(f *testing.F) {
	f.Add("https://cdn.example/a.jpg", "https://cdn.example/b.png", "")
	f.Add("a.mp4", "a.gif", "a.webp")
	f.Add("", "", "")
	f.Add("....jpg", "?.jpg", "a.jpg?x=.mp4")

	f.Fuzz(func(t *testing.T, large, file, preview string) {
		post := SourcePost{LargeURL: large, FileURL: file, PreviewURL: preview}
		got := post.DownloadURL()
		if got == "" {
			return
		}

		// 고른 주소는 반드시 후보 셋 중 하나여야 합니다.
		if got != large && got != file && got != preview {
			t.Fatalf("후보에 없는 %q를 골랐습니다", got)
		}
		// 허용한 확장자로 끝나야 합니다.
		trimmed := got
		if i := strings.IndexAny(trimmed, "?#"); i >= 0 {
			trimmed = trimmed[:i]
		}
		lower := strings.ToLower(trimmed)
		ok := false
		for ext := range allowedExt {
			if strings.HasSuffix(lower, ext) {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("허용하지 않은 확장자를 통과시켰습니다: %q", got)
		}
		// 주소가 있으면 건너뛸 이유가 없어야 합니다.
		if post.SkipReason() != "" {
			t.Fatalf("주소는 골랐는데 %q라고 합니다", post.SkipReason())
		}
	})
}

// 답이 길면 자릅니다. 어떤 글자가 섞여 있어도 글자 중간에서 잘리면
// 텔레그램이 메시지를 통째로 거부합니다.
func FuzzBotReplyTrimmed(f *testing.F) {
	f.Add("짧은 답")
	f.Add(strings.Repeat("한", 4000))
	f.Add(strings.Repeat("🎨", 2000))
	f.Add(strings.Repeat("a", 5000))

	f.Fuzz(func(t *testing.T, text string) {
		if !utf8.ValidString(text) {
			return
		}
		got := BotReply{ChatID: 1, Text: text}.Trimmed()

		if len(got.Text) > botTextLimit {
			t.Fatalf("%d바이트입니다. %d 이하여야 합니다", len(got.Text), botTextLimit)
		}
		if !utf8.ValidString(got.Text) {
			t.Fatalf("글자 중간에서 잘렸습니다: %q", got.Text)
		}
		if len(text) <= botTextLimit && got.Text != text {
			t.Fatalf("자를 필요가 없는데 건드렸습니다")
		}
	})
}
