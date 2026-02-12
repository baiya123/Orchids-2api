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
func enforceWarpBudget(builtPrompt string, messages []prompt.Message, maxTokens int) (trimmed []prompt.Message, before int, after int, compressedBlocks int, droppedMessages int) {
	budget := maxTokens
	if budget <= 0 {
		budget = 12000
	}
	if budget > 12000 {
		budget = 12000
	}

	// Start with tool_result compression.
	compressed, compressedCount := compressToolResults(messages, 1800, "warp")

	est := estimateWarpTokens(builtPrompt, compressed)
	if est <= budget {
		return compressed, est, est, compressedCount, 0
	}

	// Drop oldest messages until within budget.
	work := cloneMessages(compressed)
	beforeTokens := est

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
		est = estimateWarpTokens(builtPrompt, testMsgs)
		if est <= budget {
			start++
			break
		}
		start++
	}
	trimmed = work[start:]
	if len(trimmed) == 0 {
		trimmed = work[len(work)-1:]
	}
	afterTokens := estimateWarpTokens(builtPrompt, trimmed)
	return trimmed, beforeTokens, afterTokens, compressedCount, start
}

func estimateWarpTokens(builtPrompt string, messages []prompt.Message) int {
	total := tiktoken.EstimateTextTokens(builtPrompt) + 200
	for _, m := range messages {
		if m.Content.IsString() {
			total += tiktoken.EstimateTextTokens(strings.TrimSpace(m.Content.GetText())) + 15
			continue
		}
		// blocks
		for _, b := range m.Content.GetBlocks() {
			switch b.Type {
			case "text":
				total += tiktoken.EstimateTextTokens(strings.TrimSpace(b.Text)) + 10
			case "tool_result":
				// tool results can be huge; estimate by marshaling to string-ish
				if s, ok := b.Content.(string); ok {
					total += tiktoken.EstimateTextTokens(s) + 10
				} else {
					// fallback: small constant to avoid pathological expansions
					total += 200
				}
			default:
				// tool_use/image/document
				total += 50
			}
		}
	}
	return total
}
