package handler

import (
	"strings"

	"orchids-api/internal/perf"
)

func injectToolGate(promptText string, message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return promptText
	}
	section := "<tool_gate>\n" + message + "\n</tool_gate>\n\n"
	_, idx := findUserMarker(promptText)

	sb := perf.AcquireStringBuilder()
	defer perf.ReleaseStringBuilder(sb)

	if idx != -1 {
		sb.Grow(len(promptText) + len(section))
		sb.WriteString(promptText[:idx])
		sb.WriteString(section)
		sb.WriteString(promptText[idx:])
		return strings.Clone(sb.String())
	}

	if strings.TrimSpace(promptText) == "" {
		return section
	}

	sb.Grow(len(promptText) + len(section) + 2)
	sb.WriteString(promptText)
	sb.WriteString("\n\n")
	sb.WriteString(strings.TrimRight(section, "\n"))
	return strings.Clone(sb.String())
}

func findUserMarker(promptText string) (string, int) {
	marker := "<user_request>"
	if idx := strings.Index(promptText, marker); idx != -1 {
		return marker, idx
	}
	marker = "<user_message>"
	if idx := strings.Index(promptText, marker); idx != -1 {
		return marker, idx
	}
	return "", -1
}
