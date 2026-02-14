package grok

import (
	"net/http"
	"strings"
	"time"
)

// replyChatImagesOnly returns a minimal OpenAI-compatible chat completion (stream or non-stream)
// that contains only Markdown images.
func (h *Handler) replyChatImagesOnly(w http.ResponseWriter, model string, imgs []string, stream bool) {
	var out strings.Builder
	out.WriteString("\n\n")
	for _, u := range imgs {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		out.WriteString("![](")
		out.WriteString(u)
		out.WriteString(")\n")
	}
	content := out.String()

	id := "chatcmpl_" + randomHex(8)
	created := time.Now().Unix()

	if stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, _ := w.(http.Flusher)

		chunk1 := map[string]interface{}{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]interface{}{
				{
					"index":         0,
					"delta":         map[string]interface{}{"role": "assistant"},
					"logprobs":      nil,
					"finish_reason": nil,
				},
			},
		}
		writeSSE(w, "", encodeJSON(chunk1))
		if flusher != nil {
			flusher.Flush()
		}

		chunk2 := map[string]interface{}{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   model,
			"choices": []map[string]interface{}{
				{
					"index":         0,
					"delta":         map[string]interface{}{"content": content},
					"logprobs":      nil,
					"finish_reason": nil,
				},
			},
		}
		writeSSE(w, "", encodeJSON(chunk2))
		writeSSE(w, "", "[DONE]")
		if flusher != nil {
			flusher.Flush()
		}
		return
	}

	resp := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": content,
				},
				"logprobs":      nil,
				"finish_reason": "stop",
			},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(encodeJSON(resp)))
}
