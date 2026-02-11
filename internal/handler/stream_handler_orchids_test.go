package handler

import (
	"net/http/httptest"
	"strings"
	"testing"

	"orchids-api/internal/adapter"
	"orchids-api/internal/config"
	"orchids-api/internal/debug"
	"orchids-api/internal/upstream"
)

func TestWriteChunkSuppressThinkingFallsBackToTextDelta(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	h := newStreamHandler(
		&config.Config{OutputTokenMode: "final"},
		rec,
		debug.New(false, false),
		true, // suppress thinking
		true, // stream mode
		adapter.FormatAnthropic,
		"",
	)
	defer h.release()

	h.handleMessage(upstream.SSEMessage{
		Type: "coding_agent.Write.content.chunk",
		Event: map[string]interface{}{
			"data": map[string]interface{}{
				"file_path": "/tmp/calculator.py",
				"text":      "print('hello')",
			},
		},
	})
	h.finishResponse("end_turn")

	body := rec.Body.String()
	if !strings.Contains(body, `event: content_block_start`) {
		t.Fatalf("expected content_block_start in stream, got: %s", body)
	}
	if !strings.Contains(body, `"type":"text_delta"`) {
		t.Fatalf("expected text_delta fallback, got: %s", body)
	}
	if !strings.Contains(body, `"text":"print('hello')"`) {
		t.Fatalf("expected chunk text in stream, got: %s", body)
	}
	if strings.Contains(body, `event: coding_agent.Write.content.chunk`) {
		t.Fatalf("did not expect raw coding_agent chunk event when thinking is suppressed, got: %s", body)
	}
}

func TestWriteChunkNormalModeKeepsThinkingAndRawEventAndAddsFallbackText(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	h := newStreamHandler(
		&config.Config{OutputTokenMode: "final"},
		rec,
		debug.New(false, false),
		false, // suppress thinking disabled
		true,  // stream mode
		adapter.FormatAnthropic,
		"",
	)
	defer h.release()

	h.handleMessage(upstream.SSEMessage{
		Type: "coding_agent.Write.content.chunk",
		Event: map[string]interface{}{
			"data": map[string]interface{}{
				"file_path": "/tmp/calculator.py",
				"text":      "print('hello')",
			},
		},
	})
	h.finishResponse("end_turn")

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"thinking_delta"`) {
		t.Fatalf("expected thinking_delta in normal mode, got: %s", body)
	}
	if !strings.Contains(body, `event: coding_agent.Write.content.chunk`) {
		t.Fatalf("expected raw coding_agent chunk passthrough in normal mode, got: %s", body)
	}
	if !strings.Contains(body, `"type":"text_delta"`) || !strings.Contains(body, `"text":"print('hello')"`) {
		t.Fatalf("expected fallback text_delta for clients that do not render thinking, got: %s", body)
	}
}

func TestWriteChunkNormalModeSkipsFallbackWhenTextAlreadyExists(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	h := newStreamHandler(
		&config.Config{OutputTokenMode: "final"},
		rec,
		debug.New(false, false),
		false, // suppress thinking disabled
		true,  // stream mode
		adapter.FormatAnthropic,
		"",
	)
	defer h.release()

	h.handleMessage(upstream.SSEMessage{
		Type: "model",
		Event: map[string]interface{}{
			"type":  "text-delta",
			"delta": "done",
		},
	})
	h.handleMessage(upstream.SSEMessage{
		Type: "coding_agent.Write.content.chunk",
		Event: map[string]interface{}{
			"data": map[string]interface{}{
				"file_path": "/tmp/calculator.py",
				"text":      "print('hello')",
			},
		},
	})
	h.finishResponse("end_turn")

	body := rec.Body.String()
	if !strings.Contains(body, `"type":"text_delta"`) || !strings.Contains(body, `"text":"done"`) {
		t.Fatalf("expected original text delta, got: %s", body)
	}
	if strings.Contains(body, `"type":"text_delta","text":"print('hello')"`) {
		t.Fatalf("did not expect fallback text when model text already exists, got: %s", body)
	}
}
