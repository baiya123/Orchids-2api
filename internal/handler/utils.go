package handler

import (
	"net"
	"net/http"
	"strings"

	"orchids-api/internal/prompt"
)

// mapModel 根据请求的 model 名称映射到实际使用的模型
func mapModel(requestModel string) string {
	lowerModel := strings.ToLower(requestModel)
	if strings.Contains(lowerModel, "opus") {
		return "claude-opus-4.5"
	}
	if strings.Contains(lowerModel, "haiku") {
		return "claude-sonnet-4-5"
	}
	return "claude-sonnet-4-5"
}

func conversationKeyForRequest(r *http.Request, req ClaudeRequest) string {
	if req.ConversationID != "" {
		return req.ConversationID
	}
	if req.Metadata != nil {
		if key := metadataString(req.Metadata, "conversation_id", "conversationId", "session_id", "sessionId", "thread_id", "threadId", "chat_id", "chatId"); key != "" {
			return key
		}
	}
	if key := headerValue(r, "X-Conversation-Id", "X-Session-Id", "X-Thread-Id", "X-Chat-Id"); key != "" {
		return key
	}

	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	if host == "" {
		return ""
	}
	ua := strings.TrimSpace(r.UserAgent())
	if ua == "" {
		return host
	}
	return host + "|" + ua
}

func metadataString(metadata map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := metadata[key]; ok {
			if str, ok := value.(string); ok {
				str = strings.TrimSpace(str)
				if str != "" {
					return str
				}
			}
		}
	}
	return ""
}

func headerValue(r *http.Request, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func extractUserText(messages []prompt.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "user" {
			continue
		}
		if msg.Content.IsString() {
			return strings.TrimSpace(msg.Content.GetText())
		}
		var parts []string
		for _, block := range msg.Content.GetBlocks() {
			if block.Type == "text" {
				text := strings.TrimSpace(block.Text)
				if text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}

func isPlanMode(messages []prompt.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "user" {
			continue
		}
		if msg.Content.IsString() {
			if containsPlanReminder(msg.Content.GetText()) {
				return true
			}
			continue
		}
		for _, block := range msg.Content.GetBlocks() {
			if block.Type == "text" && containsPlanReminder(block.Text) {
				return true
			}
		}
	}
	return false
}

func containsPlanReminder(text string) bool {
	lower := strings.ToLower(text)
	if !strings.Contains(lower, "<system-reminder>") {
		return false
	}
	if strings.Contains(lower, "plan mode") || strings.Contains(lower, "planning") || strings.Contains(lower, "plan") {
		return true
	}
	if strings.Contains(text, "计划模式") || strings.Contains(text, "计划") || strings.Contains(text, "规划") {
		return true
	}
	return false
}
