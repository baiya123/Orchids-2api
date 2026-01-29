package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"orchids-api/internal/debug"
	"orchids-api/internal/prompt"
)

var orchidsAIClientModels = []string{
	"claude-sonnet-4-5",
	"claude-opus-4-5",
	"claude-haiku-4-5",
	"gemini-3-flash",
	"gpt-5.2",
}

const orchidsAIClientDefaultModel = "claude-sonnet-4-5"

func (c *Client) sendRequestWSAIClient(ctx context.Context, req UpstreamRequest, onMessage func(SSEMessage), logger *debug.Logger) error {
	parentCtx := ctx
	timeout := orchidsWSRequestTimeout
	if c.config != nil && c.config.RequestTimeout > 0 {
		timeout = time.Duration(c.config.RequestTimeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	token, err := c.getWSToken()
	if err != nil {
		return fmt.Errorf("failed to get ws token: %w", err)
	}

	wsURL := c.buildWSURLAIClient(token)
	if wsURL == "" {
		return errors.New("ws url not configured")
	}
	headers := http.Header{
		"User-Agent": []string{orchidsWSUserAgent},
		"Origin":     []string{orchidsWSOrigin},
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: orchidsWSConnectTimeout,
		Proxy:            http.ProxyFromEnvironment,
	}

	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		if resp != nil {
			if parentCtx.Err() == nil {
				return wsFallbackError{err: fmt.Errorf("ws dial failed: %s", resp.Status)}
			}
			return fmt.Errorf("ws dial failed: %s", resp.Status)
		}
		if parentCtx.Err() == nil {
			return wsFallbackError{err: fmt.Errorf("ws dial failed: %w", err)}
		}
		return fmt.Errorf("ws dial failed: %w", err)
	}
	defer conn.Close()

	wsPayload, err := c.buildWSRequestAIClient(req)
	if err != nil {
		return err
	}

	if logger != nil {
		logHeaders := map[string]string{
			"User-Agent": orchidsWSUserAgent,
			"Origin":     orchidsWSOrigin,
		}
		logger.LogUpstreamRequest(wsURL, logHeaders, wsPayload)
	}

	if err := conn.WriteJSON(wsPayload); err != nil {
		if parentCtx.Err() == nil {
			return wsFallbackError{err: fmt.Errorf("ws write failed: %w", err)}
		}
		return fmt.Errorf("ws write failed: %w", err)
	}

	var (
		preferCodingAgent bool
		textStarted       bool
		reasoningStarted  bool
		lastTextDelta     string
		finishSent        bool
		sawToolCall       bool
		editFilePath      string
		editOldString     string
		editNewString     string
	)

	for {
		if err := conn.SetReadDeadline(time.Now().Add(orchidsWSReadTimeout)); err != nil {
			if parentCtx.Err() == nil {
				return wsFallbackError{err: err}
			}
			return err
		}

		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				break
			}
			if parentCtx.Err() == nil {
				return wsFallbackError{err: err}
			}
			break
		}

		var msg map[string]interface{}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}

		msgType, _ := msg["type"].(string)
		if logger != nil {
			logger.LogUpstreamSSE(msgType, string(data))
		}

		if msgType == "connected" {
			continue
		}

		if msgType == "coding_agent.tokens_used" {
			data, _ := msg["data"].(map[string]interface{})
			if data == nil {
				continue
			}
			event := map[string]interface{}{
				"type": "tokens-used",
			}
			if v, ok := data["input_tokens"]; ok {
				event["inputTokens"] = v
			} else if v, ok := data["inputTokens"]; ok {
				event["inputTokens"] = v
			}
			if v, ok := data["output_tokens"]; ok {
				event["outputTokens"] = v
			} else if v, ok := data["outputTokens"]; ok {
				event["outputTokens"] = v
			}
			onMessage(SSEMessage{Type: "model", Event: event})
			continue
		}

		if msgType == "response_done" || msgType == "coding_agent.end" || msgType == "complete" {
			if msgType == "response_done" {
				if usage, ok := msg["response"].(map[string]interface{}); ok {
					if u, ok := usage["usage"].(map[string]interface{}); ok {
						event := map[string]interface{}{
							"type": "tokens-used",
						}
						if v, ok := u["inputTokens"]; ok {
							event["inputTokens"] = v
						}
						if v, ok := u["outputTokens"]; ok {
							event["outputTokens"] = v
						}
						onMessage(SSEMessage{Type: "model", Event: event})
					}
				}
				toolCalls := extractToolCallsFromResponse(msg)
				if len(toolCalls) > 0 {
					for _, call := range toolCalls {
						onMessage(SSEMessage{
							Type: "model.tool-call",
							Event: map[string]interface{}{
								"toolCallId": call.id,
								"toolName":   call.name,
								"input":      call.input,
							},
						})
						sawToolCall = true
					}
					if !finishSent {
						onMessage(SSEMessage{Type: "model", Event: map[string]interface{}{"finishReason": "tool-calls", "type": "finish"}})
						finishSent = true
					}
					break
				}
			}
			if textStarted {
				onMessage(SSEMessage{Type: "model", Event: map[string]interface{}{"type": "text-end", "id": "0"}})
			}
			if !finishSent {
				finishReason := "stop"
				if sawToolCall {
					finishReason = "tool-calls"
				}
				onMessage(SSEMessage{Type: "model", Event: map[string]interface{}{"type": "finish", "finishReason": finishReason}})
				finishSent = true
			}
			break
		}

		if msgType == "fs_operation" {
			if err := c.handleFSOperation(conn, msg); err != nil {
				continue
			}
			continue
		}

		if msgType == "coding_agent.todo_write.started" {
			data, _ := msg["data"].(map[string]interface{})
			input := map[string]interface{}{}
			if data != nil {
				if todos, ok := data["todos"]; ok {
					input["todos"] = todos
				}
			}
			inputJSON, err := json.Marshal(input)
			if err != nil {
				inputJSON = []byte("{}")
			}
			onMessage(SSEMessage{
				Type: "model.tool-call",
				Event: map[string]interface{}{
					"toolCallId": "toolu_todo_" + randomSuffix(8),
					"toolName":   "TodoWrite",
					"input":      string(inputJSON),
				},
			})
			sawToolCall = true
			continue
		}

		if msgType == "run_item_stream_event" || msgType == "tool_call_output_item" {
			continue
		}

		if msgType == "coding_agent.Edit.edit.started" {
			if data, ok := msg["data"].(map[string]interface{}); ok {
				if v, ok := data["file_path"].(string); ok && v != "" {
					editFilePath = v
				}
			}
			editOldString = ""
			editNewString = ""
			continue
		}

		if msgType == "coding_agent.Edit.edit.chunk" {
			if data, ok := msg["data"].(map[string]interface{}); ok {
				if v, ok := data["text"].(string); ok && v != "" {
					editNewString += v
				}
			}
			continue
		}

		if msgType == "coding_agent.edit_file.completed" || msgType == "coding_agent.Edit.edit.completed" {
			var data map[string]interface{}
			if raw, ok := msg["data"].(map[string]interface{}); ok {
				data = raw
			}
			filePath := editFilePath
			oldString := editOldString
			newString := editNewString
			if data != nil {
				if v, ok := data["file_path"].(string); ok && v != "" {
					filePath = v
				}
				if v, ok := data["old_string"].(string); ok && v != "" {
					oldString = v
				}
				if v, ok := data["new_string"].(string); ok && v != "" {
					newString = v
				}
				if oldString == "" {
					if v, ok := data["old_code"].(string); ok && v != "" {
						oldString = truncateSnippet(v, 120)
					}
				}
				if newString == "" {
					if v, ok := data["new_code"].(string); ok && v != "" {
						newString = truncateSnippet(v, 120)
					}
				}
			}
			if strings.TrimSpace(filePath) != "" {
				input := map[string]interface{}{
					"file_path":  filePath,
					"old_string": oldString,
					"new_string": newString,
				}
				inputJSON, err := json.Marshal(input)
				if err != nil {
					inputJSON = []byte("{}")
				}
				onMessage(SSEMessage{
					Type: "model.tool-call",
					Event: map[string]interface{}{
						"toolCallId": "toolu_edit_" + randomSuffix(8),
						"toolName":   "Edit",
						"input":      string(inputJSON),
					},
				})
				sawToolCall = true
			}
			editFilePath = ""
			editOldString = ""
			editNewString = ""
			continue
		}

		if msgType == "coding_agent.reasoning.chunk" {
			preferCodingAgent = true
			text := extractOrchidsText(msg)
			if text == "" {
				continue
			}
			if !reasoningStarted {
				reasoningStarted = true
				onMessage(SSEMessage{Type: "model", Event: map[string]interface{}{"type": "reasoning-start", "id": "0"}})
			}
			onMessage(SSEMessage{Type: "model", Event: map[string]interface{}{"type": "reasoning-delta", "id": "0", "delta": text}})
			continue
		}

		if msgType == "coding_agent.reasoning.completed" {
			preferCodingAgent = true
			if reasoningStarted {
				onMessage(SSEMessage{Type: "model", Event: map[string]interface{}{"type": "reasoning-end", "id": "0"}})
			}
			continue
		}

		if msgType == "output_text_delta" || msgType == "coding_agent.response.chunk" {
			preferCodingAgent = true
			text := extractOrchidsText(msg)
			if text == "" {
				continue
			}
			if text == lastTextDelta {
				continue
			}
			lastTextDelta = text
			if !textStarted {
				textStarted = true
				onMessage(SSEMessage{Type: "model", Event: map[string]interface{}{"type": "text-start", "id": "0"}})
			}
			onMessage(SSEMessage{Type: "model", Event: map[string]interface{}{"type": "text-delta", "id": "0", "delta": text}})
			continue
		}

		if msgType == "model" {
			if preferCodingAgent {
				continue
			}
			event, ok := msg["event"].(map[string]interface{})
			if !ok {
				continue
			}
			eventType, _ := event["type"].(string)
			if eventType == "tool-call" {
				sawToolCall = true
			}
			onMessage(SSEMessage{Type: "model", Event: event, Raw: msg})
			if eventType == "finish" {
				finishSent = true
				if reason, ok := event["finishReason"].(string); ok {
					if textStarted {
						onMessage(SSEMessage{Type: "model", Event: map[string]interface{}{"type": "text-end", "id": "0"}})
					}
					if reason == "tool-calls" {
						break
					}
					break
				}
			}
			continue
		}
	}

	if !finishSent {
		finishReason := "stop"
		if sawToolCall {
			finishReason = "tool-calls"
		}
		onMessage(SSEMessage{Type: "model", Event: map[string]interface{}{"type": "finish", "finishReason": finishReason}})
	}

	return nil
}

func (c *Client) buildWSURLAIClient(token string) string {
	if c.config == nil {
		return ""
	}
	wsURL := strings.TrimSpace(c.config.OrchidsWSURL)
	if wsURL == "" {
		wsURL = "wss://orchids-v2-alpha-108292236521.europe-west1.run.app/agent/ws/coding-agent"
	}
	sep := "?"
	if strings.Contains(wsURL, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%stoken=%s", wsURL, sep, urlEncode(token))
}

func (c *Client) buildWSRequestAIClient(req UpstreamRequest) (*orchidsWSRequest, error) {
	if c.config == nil {
		return nil, errors.New("server config unavailable")
	}
	systemText := extractSystemPrompt(req.Messages)
	userText, currentToolResults := extractUserMessageAIClient(req.Messages)
	currentUserIdx := findCurrentUserMessageIndex(req.Messages)
	var historyMessages []prompt.Message
	if currentUserIdx >= 0 {
		historyMessages = req.Messages[:currentUserIdx]
	} else {
		historyMessages = req.Messages
	}
	chatHistory, historyToolResults := convertChatHistoryAIClient(historyMessages)
	toolResults := mergeToolResults(historyToolResults, currentToolResults)
	orchidsTools := convertOrchidsTools(req.Tools)
	attachmentUrls := extractAttachmentURLsAIClient(req.Messages)

	promptText := buildLocalAssistantPrompt(systemText, userText)
	promptText = injectThinkingPrefix(promptText)

	workingDir := strings.TrimSpace(c.config.OrchidsLocalWorkdir)
	if req.NoTools {
		orchidsTools = nil
		toolResults = nil
		workingDir = ""
	}

	agentMode := normalizeAIClientModel(req.Model)

	payload := map[string]interface{}{
		"projectId":      nil,
		"chatSessionId":  "chat_" + randomSuffix(12),
		"prompt":         promptText,
		"agentMode":      agentMode,
		"mode":           "agent",
		"chatHistory":    chatHistory,
		"attachmentUrls": attachmentUrls,
		"currentPage":    nil,
		"email":          "bridge@localhost",
		"isLocal":        workingDir != "",
		"isFixingErrors": false,
		"fileStructure":  nil,
		"userId":         defaultUserID(c.config.UserID),
	}
	if workingDir != "" {
		payload["localWorkingDirectory"] = workingDir
	}
	if len(orchidsTools) > 0 {
		payload["tools"] = orchidsTools
	}
	if len(toolResults) > 0 {
		payload["toolResults"] = toolResults
	}

	return &orchidsWSRequest{
		Type: "user_request",
		Data: payload,
	}, nil
}

func defaultUserID(id string) string {
	if strings.TrimSpace(id) == "" {
		return "local_user"
	}
	return id
}

func normalizeAIClientModel(model string) string {
	mapped := strings.TrimSpace(model)
	if mapped == "" {
		mapped = orchidsAIClientDefaultModel
	}
	switch mapped {
	case "claude-haiku-4-5":
		mapped = "claude-sonnet-4-5"
	case "claude-opus-4-5":
		mapped = "claude-opus-4.5"
	}
	if !containsString(orchidsAIClientModels, mapped) {
		return orchidsAIClientDefaultModel
	}
	return mapped
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func extractUserMessageAIClient(messages []prompt.Message) (string, []orchidsToolResult) {
	var toolResults []orchidsToolResult
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "user" {
			continue
		}
		text, results := extractMessageTextAIClient(msg.Content)
		if len(results) > 0 {
			toolResults = append(toolResults, results...)
		}
		if strings.TrimSpace(text) != "" || len(results) > 0 {
			return text, toolResults
		}
	}
	return "", toolResults
}

func extractMessageTextAIClient(content prompt.MessageContent) (string, []orchidsToolResult) {
	if content.IsString() {
		text := strings.TrimSpace(content.GetText())
		if text != "" && strings.Contains(text, "<system-reminder>") {
			return "", nil
		}
		return text, nil
	}
	var parts []string
	var toolResults []orchidsToolResult
	for _, block := range content.GetBlocks() {
		switch block.Type {
		case "text":
			text := strings.TrimSpace(block.Text)
			if text != "" && !strings.Contains(text, "<system-reminder>") {
				parts = append(parts, text)
			}
		case "tool_result":
			text := formatToolResultContentLocal(block.Content)
			text = strings.ReplaceAll(text, "<tool_use_error>", "")
			text = strings.ReplaceAll(text, "</tool_use_error>", "")
			if strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
			toolResults = append(toolResults, orchidsToolResult{
				Content:   []map[string]string{{"text": text}},
				Status:    "success",
				ToolUseID: block.ToolUseID,
			})
		case "image":
			parts = append(parts, formatMediaHint(block))
		case "document":
			parts = append(parts, formatMediaHint(block))
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), toolResults
}

func convertChatHistoryAIClient(messages []prompt.Message) ([]map[string]string, []orchidsToolResult) {
	var history []map[string]string
	var toolResults []orchidsToolResult
	for _, msg := range messages {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		if msg.Role == "user" {
			if msg.Content.IsString() {
				text := strings.TrimSpace(msg.Content.GetText())
				if text != "" && !strings.Contains(text, "<system-reminder>") {
					history = append(history, map[string]string{
						"role":    "user",
						"content": text,
					})
				}
				continue
			}
			blocks := msg.Content.GetBlocks()
			hasSystemReminder := false
			for _, block := range blocks {
				if block.Type == "text" && strings.Contains(block.Text, "<system-reminder>") {
					hasSystemReminder = true
					break
				}
			}
			if hasSystemReminder {
				continue
			}
			var textParts []string
			for _, block := range blocks {
				switch block.Type {
				case "text":
					text := strings.TrimSpace(block.Text)
					if text != "" {
						textParts = append(textParts, text)
					}
				case "tool_result":
					contentText := formatToolResultContentLocal(block.Content)
					contentText = strings.ReplaceAll(contentText, "<tool_use_error>", "")
					contentText = strings.ReplaceAll(contentText, "</tool_use_error>", "")
					if strings.TrimSpace(contentText) != "" {
						textParts = append(textParts, contentText)
					}
					toolResults = append(toolResults, orchidsToolResult{
						Content:   []map[string]string{{"text": contentText}},
						Status:    "success",
						ToolUseID: block.ToolUseID,
					})
				case "image":
					textParts = append(textParts, formatMediaHint(block))
				case "document":
					textParts = append(textParts, formatMediaHint(block))
				}
			}
			text := strings.TrimSpace(strings.Join(textParts, "\n"))
			if text != "" {
				history = append(history, map[string]string{
					"role":    "user",
					"content": text,
				})
			}
			continue
		}

		if msg.Content.IsString() {
			text := strings.TrimSpace(msg.Content.GetText())
			if text == "" {
				continue
			}
			history = append(history, map[string]string{
				"role":    "assistant",
				"content": text,
			})
			continue
		}
		var parts []string
		for _, block := range msg.Content.GetBlocks() {
			switch block.Type {
			case "text":
				text := strings.TrimSpace(block.Text)
				if text != "" {
					parts = append(parts, text)
				}
			case "tool_use":
				inputJSON, _ := json.Marshal(block.Input)
				parts = append(parts, fmt.Sprintf("[Used tool: %s with input: %s]", block.Name, string(inputJSON)))
			case "image":
				parts = append(parts, formatMediaHint(block))
			case "document":
				parts = append(parts, formatMediaHint(block))
			}
		}
		text := strings.TrimSpace(strings.Join(parts, "\n"))
		if text == "" {
			continue
		}
		history = append(history, map[string]string{
			"role":    "assistant",
			"content": text,
		})
	}
	return history, toolResults
}

func extractAttachmentURLsAIClient(messages []prompt.Message) []string {
	seen := map[string]bool{}
	var urls []string
	for _, msg := range messages {
		if msg.Content.IsString() {
			continue
		}
		for _, block := range msg.Content.GetBlocks() {
			if block.Type != "image" && block.Type != "document" {
				continue
			}
			url := ""
			if block.Source != nil {
				url = strings.TrimSpace(block.Source.URL)
			}
			if url == "" {
				url = strings.TrimSpace(block.URL)
			}
			if url == "" || seen[url] {
				continue
			}
			seen[url] = true
			urls = append(urls, url)
		}
	}
	return urls
}

func formatMediaHint(block prompt.ContentBlock) string {
	sourceType := "unknown"
	mediaType := "unknown"
	sizeHint := ""
	if block.Source != nil {
		if strings.TrimSpace(block.Source.Type) != "" {
			sourceType = block.Source.Type
		}
		if strings.TrimSpace(block.Source.MediaType) != "" {
			mediaType = block.Source.MediaType
		}
		if block.Source.Data != "" {
			approx := int(float64(len(block.Source.Data)) * 0.75)
			sizeHint = fmt.Sprintf(" bytes≈%d", approx)
		}
	}
	switch block.Type {
	case "image":
		return fmt.Sprintf("[Image %s %s%s]", mediaType, sourceType, sizeHint)
	case "document":
		return fmt.Sprintf("[Document %s%s]", sourceType, sizeHint)
	default:
		return "[Document unknown]"
	}
}
