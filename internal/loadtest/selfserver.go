package loadtest

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
)

type SelfServer struct {
	Server  *httptest.Server
	BaseURL string
}

// StartSelfServer starts an in-process mock server that implements the two public endpoints:
//   - /orchids/v1/messages
//   - /warp/v1/messages
// It returns valid Claude-style JSON for stream=false and valid Anthropic SSE for stream=true.
// This lets the loadtest runner "跑通" without requiring real upstream accounts.
func StartSelfServer() *SelfServer {
	mux := http.NewServeMux()
	mux.HandleFunc("/orchids/v1/messages", serveMockMessages)
	mux.HandleFunc("/warp/v1/messages", serveMockMessages)

	s := httptest.NewServer(mux)
	return &SelfServer{Server: s, BaseURL: s.URL}
}

func (s *SelfServer) Close() {
	if s != nil && s.Server != nil {
		s.Server.Close()
	}
}

func serveMockMessages(w http.ResponseWriter, r *http.Request) {
	// Very small in-process mock that behaves like this project:
	// - if stream=true => text/event-stream with message_start -> text delta -> message_stop
	// - else => JSON message with a non-empty text block
	// This validates the load generator + basic protocol parsing without relying on upstream accounts.
	body, _ := io.ReadAll(r.Body)
	_ = r.Body.Close()

	// Naive check: presence of '"stream":true'
	isStream := bytes.Contains(body, []byte("\"stream\":true"))
	if isStream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(200)
		w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"))
		w.Write([]byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"))
		w.Write([]byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"))
		w.Write([]byte("event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"))
		w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
}
