package orchids

import (
	"strings"

	"orchids-api/internal/tiktoken"
)

// trimSystemContextToBudget aggressively trims the system context text to fit within
// a portion of the overall maxTokens budget, without dropping key facts.
//
// Strategy:
// - Prefer keeping lines containing key markers.
// - Keep a small head and tail window.
// - Remove very long lines and repeated blank lines.
func trimSystemContextToBudget(text string, maxTokens int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	budget := maxTokens
	if budget <= 0 {
		budget = 12000
	}
	if budget > 12000 {
		budget = 12000
	}
	// Allocate up to ~1/6 of the total budget to system_context.
	sysBudget := budget / 6
	if sysBudget < 500 {
		sysBudget = 500
	}
	if sysBudget > 1200 {
		sysBudget = 1200
	}

	if tiktoken.EstimateTextTokens(text) <= sysBudget {
		return text
	}

	lines := strings.Split(text, "\n")
	// Normalize: drop huge lines early.
	filtered := make([]string, 0, len(lines))
	for _, ln := range lines {
		l := strings.TrimRight(ln, " \t")
		if len(l) > 800 {
			l = l[:800] + "…[truncated]"
		}
		filtered = append(filtered, l)
	}
	lines = filtered

	keepMarkers := []string{
		"Primary working directory",
		"working directory",
		"gitStatus",
		"git status",
		"AGENTS.md",
		"MEMORY.md",
		"Environment",
		"Runtime",
		"OS",
		"node",
		"workspace",
	}

	isImportant := func(s string) bool {
		t := strings.ToLower(strings.TrimSpace(s))
		for _, m := range keepMarkers {
			if strings.Contains(t, strings.ToLower(m)) {
				return true
			}
		}
		return false
	}

	// Build candidate chunks: important lines + head/tail.
	var important []string
	for _, ln := range lines {
		if isImportant(ln) {
			important = append(important, ln)
		}
	}
	// Dedup important lines while preserving order.
	seen := map[string]bool{}
	dedup := make([]string, 0, len(important))
	for _, ln := range important {
		k := strings.TrimSpace(ln)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		dedup = append(dedup, ln)
	}
	important = dedup

	headN := 24
	if len(lines) < headN {
		headN = len(lines)
	}
	tailN := 16
	if len(lines) < tailN {
		tailN = len(lines)
	}

	builder := func(head, imp, tail []string) string {
		var out []string
		appendLines := func(block []string) {
			out = append(out, block...)
		}
		appendLines(head)
		if len(imp) > 0 {
			out = append(out, "…[trimmed:key]…")
			appendLines(imp)
		}
		if len(tail) > 0 {
			out = append(out, "…[tail]…")
			appendLines(tail)
		}
		// Collapse consecutive blank lines
		var collapsed []string
		blank := false
		for _, ln := range out {
			if strings.TrimSpace(ln) == "" {
				if blank {
					continue
				}
				blank = true
				collapsed = append(collapsed, "")
				continue
			}
			blank = false
			collapsed = append(collapsed, ln)
		}
		return strings.TrimSpace(strings.Join(collapsed, "\n"))
	}

	head := lines[:headN]
	tail := lines[len(lines)-tailN:]
	candidate := builder(head, important, tail)
	// If still too large, shrink head/tail.
	for (headN > 6 || tailN > 6) && tiktoken.EstimateTextTokens(candidate) > sysBudget {
		if headN > 6 {
			headN = headN - 6
		}
		if tailN > 6 {
			tailN = tailN - 6
		}
		head = lines[:headN]
		tail = lines[len(lines)-tailN:]
		candidate = builder(head, important, tail)
	}

	// Final fallback: hard truncate by runes.
	if tiktoken.EstimateTextTokens(candidate) > sysBudget {
		candidate = truncateTextWithEllipsis(candidate, 2200)
	}
	return candidate
}
