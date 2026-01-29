package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"orchids-api/internal/client"
	"orchids-api/internal/config"
	"orchids-api/internal/debug"
	"orchids-api/internal/loadbalancer"
	"orchids-api/internal/prompt"
	"orchids-api/internal/store"
	"orchids-api/internal/summarycache"
	"orchids-api/internal/tiktoken"
)

type Handler struct {
	config       *config.Config
	client       UpstreamClient
	loadBalancer *loadbalancer.LoadBalancer
	summaryCache prompt.SummaryCache
	summaryStats *summarycache.Stats
	summaryLog   bool
}

type UpstreamClient interface {
	SendRequest(ctx context.Context, prompt string, chatHistory []interface{}, model string, onMessage func(client.SSEMessage), logger *debug.Logger) error
}

type UpstreamPayloadClient interface {
	SendRequestWithPayload(ctx context.Context, req client.UpstreamRequest, onMessage func(client.SSEMessage), logger *debug.Logger) error
}

type ClaudeRequest struct {
	Model          string                 `json:"model"`
	Messages       []prompt.Message       `json:"messages"`
	System         SystemItems            `json:"system"`
	Tools          []interface{}          `json:"tools"`
	Stream         bool                   `json:"stream"`
	ConversationID string                 `json:"conversation_id"`
	Metadata       map[string]interface{} `json:"metadata"`
}

type toolCall struct {
	id    string
	name  string
	input string
}

const keepAliveInterval = 15 * time.Second

func New(cfg *config.Config) *Handler {
	return &Handler{
		config:     cfg,
		client:     client.New(cfg),
		summaryLog: cfg.SummaryCacheLog,
	}
}

func NewWithLoadBalancer(cfg *config.Config, lb *loadbalancer.LoadBalancer) *Handler {
	return &Handler{
		config:       cfg,
		loadBalancer: lb,
		summaryLog:   cfg.SummaryCacheLog,
	}
}

func (h *Handler) SetSummaryCache(cache prompt.SummaryCache) {
	h.summaryCache = cache
}

func (h *Handler) SetSummaryStats(stats *summarycache.Stats) {
	h.summaryStats = stats
}

func (h *Handler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ClaudeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// 初始化调试日志
	logger := debug.New(h.config.DebugEnabled, h.config.DebugLogSSE)
	defer logger.Close()

	// 1. 记录进入的 Claude 请求
	logger.LogIncomingRequest(req)

	if ok, command := isCommandPrefixRequest(req); ok {
		prefix := detectCommandPrefix(command)
		writeCommandPrefixResponse(w, req, prefix, startTime, logger)
		return
	}

	// 选择账号
	var apiClient UpstreamClient
	var currentAccount *store.Account
	var failedAccountIDs []int64

	selectAccount := func() error {
		if h.loadBalancer != nil {
			targetChannel := h.loadBalancer.GetModelChannel(r.Context(), req.Model)
			if targetChannel != "" {
				slog.Info("Model recognition", "model", req.Model, "channel", targetChannel)
			}
			account, err := h.loadBalancer.GetNextAccountExcludingByChannel(r.Context(), failedAccountIDs, targetChannel)
			if err != nil {
				if h.client != nil {
					apiClient = h.client
					currentAccount = nil
					slog.Info("Load balancer: no available accounts for channel, using default config", "channel", targetChannel)
					return nil
				}
				return err
			}
			apiClient = client.NewFromAccount(account, h.config)
			currentAccount = account
			return nil
		} else if h.client != nil {
			apiClient = h.client
			currentAccount = nil
			return nil
		}
		return errors.New("no client configured")
	}

	if err := selectAccount(); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	if currentAccount != nil && h.loadBalancer != nil {
		h.loadBalancer.AcquireConnection(currentAccount.ID)
		defer h.loadBalancer.ReleaseConnection(currentAccount.ID)
	}

	conversationKey := conversationKeyForRequest(r, req)
	var hitsBefore, missesBefore uint64
	if h.summaryStats != nil && h.summaryLog {
		hitsBefore, missesBefore = h.summaryStats.Snapshot()
	}

	userText := extractUserText(req.Messages)
	planMode := isPlanMode(req.Messages)
	gateNoTools := false
	effectiveTools := req.Tools
	if gateNoTools {
		effectiveTools = nil
		slog.Info("tool_gate: disabled tools for short non-code request")
	}
	toolCallMode := strings.ToLower(strings.TrimSpace(h.config.ToolCallMode))
	if toolCallMode == "" {
		toolCallMode = "proxy"
	}
	if planMode {
		toolCallMode = "proxy"
	}
	if toolCallMode == "auto" || toolCallMode == "internal" {
		effectiveTools = filterSupportedTools(effectiveTools)
	}

	// 构建 prompt（V2 Markdown 格式）
	opts := prompt.PromptOptions{
		Context:          r.Context(),
		ConversationID:   conversationKey,
		MaxTokens:        h.config.ContextMaxTokens,
		SummaryMaxTokens: h.config.ContextSummaryMaxTokens,
		KeepTurns:        h.config.ContextKeepTurns,
		SummaryCache:     h.summaryCache,
	}
	builtPrompt := prompt.BuildPromptV2WithOptions(prompt.ClaudeAPIRequest{
		Model:    req.Model,
		Messages: req.Messages,
		System:   req.System,
		Tools:    effectiveTools,
		Stream:   req.Stream,
	}, opts)

	if h.summaryStats != nil && h.summaryLog {
		hitsAfter, missesAfter := h.summaryStats.Snapshot()
		hitDelta := hitsAfter - hitsBefore
		missDelta := missesAfter - missesBefore
		if hitDelta > 0 || missDelta > 0 {
			slog.Info("summary_cache", "hit", hitDelta, "miss", missDelta)
		}
	}

	// 映射模型
	mappedModel := mapModel(req.Model)
	slog.Info("Model mapping", "original", req.Model, "mapped", mappedModel)

	useWS := strings.EqualFold(strings.TrimSpace(h.config.UpstreamMode), "ws")
	if toolCallMode == "internal" && req.Stream {
		req.Stream = false
	}

	isStream := req.Stream
	var flusher http.Flusher
	if isStream {
		// 设置 SSE 响应头
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		streamFlusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming not supported", http.StatusInternalServerError)
			return
		}
		flusher = streamFlusher
	} else {
		w.Header().Set("Content-Type", "application/json")
	}

	// 状态管理
	msgID := fmt.Sprintf("msg_%d", time.Now().UnixMilli())
	blockIndex := -1
	var hasReturn bool
	var mu sync.Mutex
	var keepAliveStop chan struct{}
	writeKeepAlive := func() {
		if !isStream {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if hasReturn {
			return
		}
		fmt.Fprint(w, ": keepalive\n\n")
		flusher.Flush()
		logger.LogOutputSSE("keepalive", "")
	}
	if isStream {
		keepAliveStop = make(chan struct{})
		ticker := time.NewTicker(keepAliveInterval)
		go func() {
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					writeKeepAlive()
				case <-keepAliveStop:
					return
				}
			}
		}()
	}
	var finalStopReason string
	toolBlocks := make(map[string]int)
	var responseText strings.Builder
	var contentBlocks []map[string]interface{}
	var currentTextIndex = -1
	textBlockBuilders := map[int]*strings.Builder{}
	var pendingToolCalls []toolCall
	toolInputNames := map[string]string{}
	toolInputBuffers := map[string]*strings.Builder{}
	toolInputHadDelta := map[string]bool{}
	toolCallHandled := map[string]bool{}
	currentToolInputID := ""
	allowedTools := map[string]string{}
	var allowedIndex []toolNameInfo
	for _, t := range effectiveTools {
		if tm, ok := t.(map[string]interface{}); ok {
			if name, ok := tm["name"].(string); ok {
				name = strings.TrimSpace(name)
				if name != "" {
					allowedTools[strings.ToLower(name)] = name
				}
			}
		}
	}
	allowedIndex = buildToolNameIndex(effectiveTools, allowedTools)
	hasToolList := len(allowedTools) > 0
	resolveToolName := func(name string) (string, bool) {
		name = strings.TrimSpace(name)
		if name == "" {
			return "", false
		}
		if !hasToolList {
			return name, true
		}
		if resolved, ok := allowedTools[strings.ToLower(name)]; ok {
			return resolved, true
		}
		return "", false
	}
	var toolCallCount int
	var internalToolResults []safeToolResult
	var preflightResults []safeToolResult
	var internalNeedsFollowup bool
	chatHistory := []interface{}{}
	upstreamMessages := append([]prompt.Message(nil), req.Messages...)
	allowBashName := ""
	shouldLocalFallback := false
	if name, ok := resolveToolName("bash"); ok {
		allowBashName = name
	}
	if toolCallMode == "internal" && !useWS && allowBashName != "" && shouldPreflightTools(userText) {
		preflight := []string{
			"pwd",
			"find . -maxdepth 2 -not -path '*/.*'",
			"ls -la",
		}
		for i, cmd := range preflight {
			call := toolCall{
				id:    fmt.Sprintf("internal_tool_%d", i+1),
				name:  allowBashName,
				input: fmt.Sprintf(`{"command":%q,"description":"internal preflight"}`, cmd),
			}
			result := executeSafeTool(call)
			preflightResults = append(preflightResults, result)
			chatHistory = append(chatHistory, map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":  "tool_use",
						"id":    result.call.id,
						"name":  result.call.name,
						"input": result.input,
					},
				},
			})
			chatHistory = append(chatHistory, map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":        "tool_result",
						"tool_use_id": result.call.id,
						"content":     result.output,
						"is_error":    result.isError,
					},
				},
			})
		}
		shouldLocalFallback = len(preflightResults) > 0
	}

	localContext := formatLocalToolResults(preflightResults)
	if localContext != "" {
		builtPrompt = injectLocalContext(builtPrompt, localContext)
	}
	if gateNoTools {
		builtPrompt = injectToolGate(builtPrompt, "This is a short, non-code request. Do NOT call tools or perform any file operations. Answer directly.")
	}

	// 2. 记录转换后的 prompt
	logger.LogConvertedPrompt(builtPrompt)

	// Token 计数
	inputTokens := tiktoken.EstimateTextTokens(builtPrompt)
	var outputTokens int
	var outputMu sync.Mutex
	var outputBuilder strings.Builder
	var useUpstreamUsage bool
	outputTokenMode := strings.ToLower(strings.TrimSpace(h.config.OutputTokenMode))
	if outputTokenMode == "" {
		outputTokenMode = "final"
	}

	addOutputTokens := func(text string) {
		if text == "" {
			return
		}
		outputMu.Lock()
		if useUpstreamUsage {
			outputMu.Unlock()
			return
		}
		outputMu.Unlock()
		if outputTokenMode == "stream" {
			tokens := tiktoken.EstimateTextTokens(text)
			outputMu.Lock()
			outputTokens += tokens
			outputMu.Unlock()
			return
		}
		outputMu.Lock()
		outputBuilder.WriteString(text)
		outputMu.Unlock()
	}

	finalizeOutputTokens := func() {
		if outputTokenMode == "stream" {
			return
		}
		outputMu.Lock()
		if useUpstreamUsage {
			outputMu.Unlock()
			return
		}
		text := outputBuilder.String()
		outputTokens = tiktoken.EstimateTextTokens(text)
		outputMu.Unlock()
	}
	setUsageTokens := func(input, output int) {
		outputMu.Lock()
		if input >= 0 {
			inputTokens = input
		}
		if output >= 0 {
			outputTokens = output
		}
		useUpstreamUsage = true
		outputMu.Unlock()
	}

	resetRoundState := func() {
		blockIndex = -1
		toolBlocks = make(map[string]int)
		responseText = strings.Builder{}
		contentBlocks = nil
		currentTextIndex = -1
		textBlockBuilders = map[int]*strings.Builder{}
		pendingToolCalls = nil
		toolInputNames = map[string]string{}
		toolInputBuffers = map[string]*strings.Builder{}
		toolInputHadDelta = map[string]bool{}
		toolCallHandled = map[string]bool{}
		currentToolInputID = ""
		toolCallCount = 0
		outputTokens = 0
		outputBuilder = strings.Builder{}
		useUpstreamUsage = false
		finalStopReason = ""
	}

	// SSE 写入函数
	writeSSE := func(event, data string) {
		if !isStream {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if hasReturn {
			return
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()

		// 5. 记录输出给客户端的 SSE
		logger.LogOutputSSE(event, data)
	}

	writeFinalSSE := func(event, data string) {
		if !isStream {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()

		// 5. 记录输出给客户端的 SSE
		logger.LogOutputSSE(event, data)
	}

	shouldEmitToolCalls := func(stopReason string) bool {
		switch toolCallMode {
		case "proxy":
			return true
		case "auto":
			return stopReason == "tool_use"
		case "internal":
			return false
		default:
			return true
		}
	}

	emitToolCallNonStream := func(call toolCall) {
		addOutputTokens(call.name)
		addOutputTokens(call.input)
		fixedInput := fixToolInput(call.input)
		var inputValue interface{}
		if err := json.Unmarshal([]byte(fixedInput), &inputValue); err != nil {
			inputValue = map[string]interface{}{}
		}
		contentBlocks = append(contentBlocks, map[string]interface{}{
			"type":  "tool_use",
			"id":    call.id,
			"name":  call.name,
			"input": inputValue,
		})
	}

	emitToolCallStream := func(call toolCall, idx int, write func(event, data string)) {
		if call.id == "" {
			return
		}
		if idx < 0 {
			mu.Lock()
			blockIndex++
			idx = blockIndex
			mu.Unlock()
		}

		addOutputTokens(call.name)
		addOutputTokens(call.input)
		fixedInput := fixToolInput(call.input)

		startData, _ := json.Marshal(map[string]interface{}{
			"type":  "content_block_start",
			"index": idx,
			"content_block": map[string]interface{}{
				"type":  "tool_use",
				"id":    call.id,
				"name":  call.name,
				"input": map[string]interface{}{},
			},
		})
		write("content_block_start", string(startData))

		deltaData, _ := json.Marshal(map[string]interface{}{
			"type":  "content_block_delta",
			"index": idx,
			"delta": map[string]interface{}{
				"type":         "input_json_delta",
				"partial_json": fixedInput,
			},
		})
		write("content_block_delta", string(deltaData))

		stopData, _ := json.Marshal(map[string]interface{}{
			"type":  "content_block_stop",
			"index": idx,
		})
		write("content_block_stop", string(stopData))
	}

	flushPendingToolCalls := func(stopReason string, write func(event, data string)) {
		if !shouldEmitToolCalls(stopReason) {
			return
		}

		mu.Lock()
		calls := make([]toolCall, len(pendingToolCalls))
		copy(calls, pendingToolCalls)
		pendingToolCalls = nil
		mu.Unlock()

		for _, call := range calls {
			if isStream {
				emitToolCallStream(call, -1, write)
			} else {
				emitToolCallNonStream(call)
			}
		}
	}

	overrideWithLocalContext := func() {
		if isStream || !shouldLocalFallback {
			return
		}
		pwd := extractPreflightPwd(preflightResults)
		currentText := strings.TrimSpace(responseText.String())
		if currentText != "" && pwd != "" && strings.Contains(currentText, pwd) {
			return
		}
		fallback := buildLocalFallbackResponse(preflightResults)
		if fallback == "" {
			return
		}
		responseText = strings.Builder{}
		responseText.WriteString(fallback)
		contentBlocks = []map[string]interface{}{
			{
				"type": "text",
				"text": fallback,
			},
		}
		textBlockBuilders = map[int]*strings.Builder{}
		currentTextIndex = -1
		outputBuilder = strings.Builder{}
		outputBuilder.WriteString(fallback)
		outputTokens = 0
	}

	finishResponse := func(stopReason string) {
		if stopReason == "tool_use" {
			mu.Lock()
			hasToolCalls := toolCallCount > 0 || len(pendingToolCalls) > 0
			mu.Unlock()
			if !hasToolCalls {
				stopReason = "end_turn"
			}
		}
		mu.Lock()
		if hasReturn {
			mu.Unlock()
			return
		}
		hasReturn = true
		finalStopReason = stopReason
		mu.Unlock()

		if isStream {
			flushPendingToolCalls(stopReason, writeFinalSSE)
			finalizeOutputTokens()
			deltaData, _ := json.Marshal(map[string]interface{}{
				"type":  "message_delta",
				"delta": map[string]string{"stop_reason": stopReason},
				"usage": map[string]int{"output_tokens": outputTokens},
			})
			writeFinalSSE("message_delta", string(deltaData))

			stopData, _ := json.Marshal(map[string]string{"type": "message_stop"})
			writeFinalSSE("message_stop", string(stopData))
		} else {
			flushPendingToolCalls(stopReason, writeFinalSSE)
			overrideWithLocalContext()
			finalizeOutputTokens()
		}

		// 6. 记录摘要
		logger.LogSummary(inputTokens, outputTokens, time.Since(startTime), stopReason)
		slog.Info("Request completed", "input_tokens", inputTokens, "output_tokens", outputTokens, "duration", time.Since(startTime))
	}

	// 发送 message_start
	startData, _ := json.Marshal(map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":      msgID,
			"type":    "message",
			"role":    "assistant",
			"content": []interface{}{},
			"model":   req.Model,
			"usage":   map[string]int{"input_tokens": inputTokens, "output_tokens": 0},
		},
	})
	writeSSE("message_start", string(startData))

	slog.Info("New request received")

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			internalNeedsFollowup = false
			internalToolResults = nil
			maxRetries := h.config.MaxRetries
			if maxRetries < 0 {
				maxRetries = 0
			}
			retryDelay := time.Duration(h.config.RetryDelay) * time.Millisecond
			retriesRemaining := maxRetries
			handleToolCall := func(call toolCall) {
				if call.id == "" {
					return
				}
				if toolCallMode == "internal" {
					result := executeSafeTool(call)
					internalToolResults = append(internalToolResults, result)
					return
				}
				if toolCallMode == "auto" {
					if !isStream {
						emitToolCallNonStream(call)
					}
					result := executeToolCall(call, h.config)
					internalToolResults = append(internalToolResults, result)
					return
				}
				mu.Lock()
				toolCallCount++
				mu.Unlock()
				if toolCallMode != "proxy" {
					mu.Lock()
					pendingToolCalls = append(pendingToolCalls, call)
					mu.Unlock()
					return
				}

				if !isStream {
					emitToolCallNonStream(call)
					return
				}

				idx := -1
				mu.Lock()
				if value, exists := toolBlocks[call.id]; exists {
					idx = value
					delete(toolBlocks, call.id)
				}
				mu.Unlock()
				emitToolCallStream(call, idx, writeSSE)
			}

			handleMessage := func(msg client.SSEMessage) {
				mu.Lock()
				if hasReturn {
					mu.Unlock()
					return
				}
				mu.Unlock()

				eventKey := msg.Type
				if msg.Type == "model" && msg.Event != nil {
					if evtType, ok := msg.Event["type"].(string); ok {
						eventKey = "model." + evtType
					}
				}
				getUsageInt := func(usage map[string]interface{}, key string) (int, bool) {
					if usage == nil {
						return 0, false
					}
					if raw, ok := usage[key]; ok {
						switch v := raw.(type) {
						case float64:
							return int(v), true
						case int:
							return v, true
						case json.Number:
							if n, err := v.Int64(); err == nil {
								return int(n), true
							}
						}
					}
					return 0, false
				}

				switch eventKey {
				case "model.reasoning-start":
					mu.Lock()
					blockIndex++
					idx := blockIndex
					mu.Unlock()
					data, _ := json.Marshal(map[string]interface{}{
						"type":          "content_block_start",
						"index":         idx,
						"content_block": map[string]interface{}{"type": "thinking", "thinking": ""},
					})
					writeSSE("content_block_start", string(data))

				case "model.reasoning-delta":
					mu.Lock()
					idx := blockIndex
					mu.Unlock()
					delta, _ := msg.Event["delta"].(string)
					if isStream {
						addOutputTokens(delta)
					}
					data, _ := json.Marshal(map[string]interface{}{
						"type":  "content_block_delta",
						"index": idx,
						"delta": map[string]interface{}{"type": "thinking_delta", "thinking": delta},
					})
					writeSSE("content_block_delta", string(data))

				case "model.reasoning-end":
					mu.Lock()
					idx := blockIndex
					mu.Unlock()
					data, _ := json.Marshal(map[string]interface{}{
						"type":  "content_block_stop",
						"index": idx,
					})
					writeSSE("content_block_stop", string(data))

				case "model.text-start":
					mu.Lock()
					blockIndex++
					idx := blockIndex
					mu.Unlock()
					if !isStream {
						contentBlocks = append(contentBlocks, map[string]interface{}{
							"type": "text",
						})
						currentTextIndex = len(contentBlocks) - 1
						textBlockBuilders[currentTextIndex] = &strings.Builder{}
					}
					data, _ := json.Marshal(map[string]interface{}{
						"type":          "content_block_start",
						"index":         idx,
						"content_block": map[string]string{"type": "text", "text": ""},
					})
					writeSSE("content_block_start", string(data))

				case "model.text-delta":
					mu.Lock()
					idx := blockIndex
					mu.Unlock()
					delta, _ := msg.Event["delta"].(string)
					addOutputTokens(delta)
					if !isStream {
						responseText.WriteString(delta)
						if currentTextIndex >= 0 && currentTextIndex < len(contentBlocks) {
							builder, ok := textBlockBuilders[currentTextIndex]
							if !ok {
								builder = &strings.Builder{}
								textBlockBuilders[currentTextIndex] = builder
							}
							builder.WriteString(delta)
						}
					}
					data, _ := json.Marshal(map[string]interface{}{
						"type":  "content_block_delta",
						"index": idx,
						"delta": map[string]string{"type": "text_delta", "text": delta},
					})
					writeSSE("content_block_delta", string(data))

				case "model.text-end":
					mu.Lock()
					idx := blockIndex
					mu.Unlock()
					data, _ := json.Marshal(map[string]interface{}{
						"type":  "content_block_stop",
						"index": idx,
					})
					writeSSE("content_block_stop", string(data))

				case "model.tool-input-start":
					toolID, _ := msg.Event["id"].(string)
					toolName, _ := msg.Event["toolName"].(string)
					if toolID == "" || toolName == "" {
						return
					}
					currentToolInputID = toolID
					toolInputNames[toolID] = toolName
					toolInputBuffers[toolID] = &strings.Builder{}
					toolInputHadDelta[toolID] = false
					if !isStream || (toolCallMode != "proxy" && toolCallMode != "auto") {
						return
					}
					mappedName := mapOrchidsToolName(toolName, "", allowedIndex, allowedTools)
					finalName, ok := resolveToolName(mappedName)
					if !ok {
						return
					}
					addOutputTokens(finalName)
					mu.Lock()
					blockIndex++
					idx := blockIndex
					toolBlocks[toolID] = idx
					toolCallCount++
					mu.Unlock()
					startData, _ := json.Marshal(map[string]interface{}{
						"type":  "content_block_start",
						"index": idx,
						"content_block": map[string]interface{}{
							"type":  "tool_use",
							"id":    toolID,
							"name":  finalName,
							"input": map[string]interface{}{},
						},
					})
					writeSSE("content_block_start", string(startData))

				case "model.tool-input-delta":
					toolID, _ := msg.Event["id"].(string)
					delta, _ := msg.Event["delta"].(string)
					if toolID == "" {
						return
					}
					if buf, ok := toolInputBuffers[toolID]; ok {
						buf.WriteString(delta)
					}
					if delta != "" {
						toolInputHadDelta[toolID] = true
					}
					if !isStream || (toolCallMode != "proxy" && toolCallMode != "auto") || delta == "" {
						return
					}
					idx, ok := toolBlocks[toolID]
					if !ok {
						return
					}
					deltaData, _ := json.Marshal(map[string]interface{}{
						"type":  "content_block_delta",
						"index": idx,
						"delta": map[string]interface{}{
							"type":         "input_json_delta",
							"partial_json": delta,
						},
					})
					writeSSE("content_block_delta", string(deltaData))

				case "model.tool-input-end":
					toolID, _ := msg.Event["id"].(string)
					if toolID == "" {
						return
					}
					if currentToolInputID == toolID {
						currentToolInputID = ""
					}
					name, ok := toolInputNames[toolID]
					if !ok || name == "" {
						delete(toolInputBuffers, toolID)
						delete(toolInputHadDelta, toolID)
						delete(toolInputNames, toolID)
						return
					}
					inputStr := ""
					if buf, ok := toolInputBuffers[toolID]; ok {
						inputStr = strings.TrimSpace(buf.String())
					}
					if isStream && (toolCallMode == "proxy" || toolCallMode == "auto") {
						if inputStr != "" {
							addOutputTokens(inputStr)
						}
						if idx, ok := toolBlocks[toolID]; ok {
							if !toolInputHadDelta[toolID] && inputStr != "" {
								deltaData, _ := json.Marshal(map[string]interface{}{
									"type":  "content_block_delta",
									"index": idx,
									"delta": map[string]interface{}{
										"type":         "input_json_delta",
										"partial_json": inputStr,
									},
								})
								writeSSE("content_block_delta", string(deltaData))
							}
							stopData, _ := json.Marshal(map[string]interface{}{
								"type":  "content_block_stop",
								"index": idx,
							})
							writeSSE("content_block_stop", string(stopData))
							delete(toolBlocks, toolID)
						}
					}
					delete(toolInputBuffers, toolID)
					delete(toolInputHadDelta, toolID)
					delete(toolInputNames, toolID)
					if toolCallHandled[toolID] {
						return
					}
					resolvedName := mapOrchidsToolName(name, inputStr, allowedIndex, allowedTools)
					finalName, ok := resolveToolName(resolvedName)
					if !ok {
						return
					}
					call := toolCall{id: toolID, name: finalName, input: inputStr}
					toolCallHandled[toolID] = true
					if toolCallMode == "proxy" && isStream {
						return
					}
					handleToolCall(call)

				case "model.tool-call":
					toolID, _ := msg.Event["toolCallId"].(string)
					toolName, _ := msg.Event["toolName"].(string)
					inputStr, _ := msg.Event["input"].(string)
					if toolID == "" {
						return
					}
					if currentToolInputID != "" && toolID != currentToolInputID {
						return
					}
					if toolCallHandled[toolID] {
						return
					}
					if _, ok := toolInputBuffers[toolID]; ok {
						return
					}
					mappedName := mapOrchidsToolName(toolName, inputStr, allowedIndex, allowedTools)
					resolvedName, ok := resolveToolName(mappedName)
					if !ok {
						return
					}
					call := toolCall{id: toolID, name: resolvedName, input: inputStr}
					toolCallHandled[toolID] = true
					handleToolCall(call)

				case "model.tokens-used":
					usage := msg.Event
					inputTokens, hasIn := getUsageInt(usage, "inputTokens")
					outputTokens, hasOut := getUsageInt(usage, "outputTokens")
					if !hasIn {
						inputTokens, hasIn = getUsageInt(usage, "input_tokens")
					}
					if !hasOut {
						outputTokens, hasOut = getUsageInt(usage, "output_tokens")
					}
					if hasIn || hasOut {
						in := -1
						out := -1
						if hasIn {
							in = inputTokens
						}
						if hasOut {
							out = outputTokens
						}
						setUsageTokens(in, out)
					}
					return

				case "model.finish":
					stopReason := "end_turn"
					if usage, ok := msg.Event["usage"].(map[string]interface{}); ok {
						inputTokens, hasIn := getUsageInt(usage, "inputTokens")
						outputTokens, hasOut := getUsageInt(usage, "outputTokens")
						if !hasIn {
							inputTokens, hasIn = getUsageInt(usage, "input_tokens")
						}
						if !hasOut {
							outputTokens, hasOut = getUsageInt(usage, "output_tokens")
						}
						if hasIn || hasOut {
							in := -1
							out := -1
							if hasIn {
								in = inputTokens
							}
							if hasOut {
								out = outputTokens
							}
							setUsageTokens(in, out)
						}
					}
					if finishReason, ok := msg.Event["finishReason"].(string); ok {
						switch finishReason {
						case "tool-calls":
							stopReason = "tool_use"
						case "stop", "end_turn":
							stopReason = "end_turn"
						}
					}
					if stopReason == "tool_use" && (toolCallMode == "internal" || toolCallMode == "auto") {
						if len(internalToolResults) > 0 {
							internalNeedsFollowup = true
						}
						return
					}
					finishResponse(stopReason)
				}
			}

			upstreamReq := client.UpstreamRequest{
				Prompt:      builtPrompt,
				ChatHistory: chatHistory,
				Model:       mappedModel,
				Messages:    upstreamMessages,
				System:      []prompt.SystemItem(req.System),
				Tools:       effectiveTools,
				NoTools:     gateNoTools,
			}
			for {
				var err error
				if sender, ok := apiClient.(UpstreamPayloadClient); ok {
					err = sender.SendRequestWithPayload(r.Context(), upstreamReq, handleMessage, logger)
				} else {
					err = apiClient.SendRequest(r.Context(), builtPrompt, chatHistory, mappedModel, handleMessage, logger)
				}

				if err == nil {
					break
				}

				slog.Error("Request error", "error", err)
				if r.Context().Err() != nil {
					finishResponse("end_turn")
					return
				}
				if retriesRemaining <= 0 {
					if currentAccount != nil && h.loadBalancer != nil {
						slog.Error("Account request failed, max retries reached", "account", currentAccount.Name)
					}
					finishResponse("end_turn")
					return
				}
				retriesRemaining--
				if currentAccount != nil && h.loadBalancer != nil {
					failedAccountIDs = append(failedAccountIDs, currentAccount.ID)
					slog.Warn("Account request failed, switching account", "account", currentAccount.Name, "failed_count", len(failedAccountIDs))
					if retryErr := selectAccount(); retryErr == nil {
						slog.Info("Switched to account", "account", currentAccount.Name)
					} else {
						slog.Error("No more accounts available", "error", retryErr)
						finishResponse("end_turn")
						return
					}
				}
				if retryDelay > 0 {
					select {
					case <-time.After(retryDelay):
					case <-r.Context().Done():
						finishResponse("end_turn")
						return
					}
				}
			}
			if (toolCallMode == "internal" || toolCallMode == "auto") && internalNeedsFollowup {
				for _, result := range internalToolResults {
					upstreamMessages = append(upstreamMessages,
						prompt.Message{
							Role: "assistant",
							Content: prompt.MessageContent{
								Blocks: []prompt.ContentBlock{
									{
										Type:  "tool_use",
										ID:    result.call.id,
										Name:  result.call.name,
										Input: result.input,
									},
								},
							},
						},
						prompt.Message{
							Role: "user",
							Content: prompt.MessageContent{
								Blocks: []prompt.ContentBlock{
									{
										Type:      "tool_result",
										ToolUseID: result.call.id,
										Content:   result.output,
										IsError:   result.isError,
									},
								},
							},
						},
					)
					chatHistory = append(chatHistory, map[string]interface{}{
						"role": "assistant",
						"content": []map[string]interface{}{
							{
								"type":  "tool_use",
								"id":    result.call.id,
								"name":  result.call.name,
								"input": result.input,
							},
						},
					})
					chatHistory = append(chatHistory, map[string]interface{}{
						"role": "user",
						"content": []map[string]interface{}{
							{
								"type":        "tool_result",
								"tool_use_id": result.call.id,
								"content":     result.output,
								"is_error":    result.isError,
							},
						},
					})
				}
				resetRoundState()
				continue
			}
			break
		}
	}()

	<-done

	if keepAliveStop != nil {
		close(keepAliveStop)
	}

	// 确保有最终响应
	if !hasReturn {
		finishResponse("end_turn")
	}

	if !isStream {
		stopReason := finalStopReason
		if stopReason == "" {
			stopReason = "end_turn"
		}

		for i := range contentBlocks {
			if blockType, ok := contentBlocks[i]["type"].(string); ok && blockType == "text" {
				if builder, ok := textBlockBuilders[i]; ok {
					contentBlocks[i]["text"] = builder.String()
				} else if _, ok := contentBlocks[i]["text"]; !ok {
					contentBlocks[i]["text"] = ""
				}
			}
		}

		if len(contentBlocks) == 0 && responseText.Len() > 0 {
			contentBlocks = append(contentBlocks, map[string]interface{}{
				"type": "text",
				"text": responseText.String(),
			})
		}

		response := map[string]interface{}{
			"id":            msgID,
			"type":          "message",
			"role":          "assistant",
			"content":       contentBlocks,
			"model":         req.Model,
			"stop_reason":   stopReason,
			"stop_sequence": nil,
			"usage": map[string]int{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens,
			},
		}
		_ = json.NewEncoder(w).Encode(response)
	}
	_ = finalStopReason
}
