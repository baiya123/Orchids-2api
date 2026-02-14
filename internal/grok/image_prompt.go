package grok

import "strings"

// buildImagePromptFromUser keeps the subject consistent while adding minimal constraints
// to reduce "street scenery" drift.
func buildImagePromptFromUser(userText string, n int) string {
	u := strings.TrimSpace(userText)
	if u == "" {
		u = "美女人像照片"
	}
	// Force portrait/person wording so the upstream doesn't decide to draw scenery.
	return strings.TrimSpace(u + "\n\n要求：真人风格、人物为主（portrait / 人像），不要风景街景为主体；高清、细节丰富。生成 " + itoaSafe(n, 1) + " 张。")
}

func itoaSafe(n int, def int) string {
	if n <= 0 {
		n = def
	}
	// small manual int to string (avoid extra imports)
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var b [32]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
