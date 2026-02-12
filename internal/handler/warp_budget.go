package handler

import (
	"strings"

	"orchids-api/internal/prompt"
	"orchids-api/internal/tiktoken"
)

// enforceWarpBudget trims Warp messages to keep total prompt+messages within a hard token budget.
// Strategy:
// 1) Compress tool_result blocks (largest offenders).
// 2) Drop oldest messages until within budget, always keeping the last user message.
type warpTokenBreakdown struct {
	PromptTokens   int
	MessagesTokens int
	ToolTokens     int
	Total          int
}

func enforceWarpBudget(builtPrompt string, messages []prompt.Message, maxTokens int) (trimmed []prompt.Message, before warpTokenBreakdown, after warpTokenBreakdown, compressedBlocks int, droppedMessages int) {
	budget := maxTokens
	if budget <= 0 {
		budget = 12000
	}
	if budget > 12000 {
		budget = 12000
	}

	// Start with tool_result compression.
	compressed, compressedCount := compressToolResults(messages, 1800, "warp")

	beforeBD := estimateWarpTokensBreakdown(builtPrompt, compressed)
	if beforeBD.Total <= budget {
		return compressed, beforeBD, beforeBD, compressedCount, 0
	}

	// Drop oldest messages until within budget.
	work := cloneMessages(compressed)
	beforeTokens := beforeBD

	// Find last user message index.
	lastUser := -1
	for i := len(work) - 1; i >= 0; i-- {
		if work[i].Role == "user" {
			lastUser = i
			break
		}
	}
	if lastUser == -1 {
		lastUser = len(work) - 1
	}

	start := 0
	for start < lastUser && len(work[start:]) > 1 {
		testMsgs := work[start+1:]
		bd := estimateWarpTokensBreakdown(builtPrompt, testMsgs)
		if bd.Total <= budget {
			start++
			break
		}
		start++
	}
	trimmed = work[start:]
	if len(trimmed) == 0 {
		trimmed = work[len(work)-1:]
	}
	afterTokens := estimateWarpTokensBreakdown(builtPrompt, trimmed)
	return trimmed, beforeTokens, afterTokens, compressedCount, start
}

func estimateWarpTokensBreakdown(builtPrompt string, messages []prompt.Message) warpTokenBreakdown {
	bd := warpTokenBreakdown{}
	bd.PromptTokens = tiktoken.EstimateTextTokens(builtPrompt)
	// Conservative wrapper overhead.
	overhead := 200

	for _, m := range messages {
		if m.Content.IsString() {
			bd.MessagesTokens += tiktoken.EstimateTextTokens(strings.TrimSpace(m.Content.GetText())) + 15
			continue
		}
		for _, b := range m.Content.GetBlocks() {
			switch b.Type {
			case "text":
				bd.MessagesTokens += tiktoken.EstimateTextTokens(strings.TrimSpace(b.Text)) + 10
			case "tool_result":
				if s, ok := b.Content.(string); ok {
					bd.ToolTokens += tiktoken.EstimateTextTokens(s) + 10
				} else {
					bd.ToolTokens += 200
				}
			default:
				bd.ToolTokens += 50
			}
		}
	}
	bd.Total = bd.PromptTokens + bd.MessagesTokens + bd.ToolTokens + overhead
	return bd
}
