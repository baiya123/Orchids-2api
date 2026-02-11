package handler

import (
	"bytes"
	"net/http"
	"testing"
	"time"
)

func TestComputeRequestHash_ChangesWithAuthPathBody(t *testing.T) {
	h := &Handler{}
	mkReq := func(path, auth string) *http.Request {
		r, _ := http.NewRequest(http.MethodPost, "http://example.com"+path, bytes.NewReader([]byte("{}")))
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		return r
	}
	bodyA := []byte(`{"a":1}`)
	bodyB := []byte(`{"a":2}`)

	h1 := h.computeRequestHash(mkReq("/v1/messages", "Bearer x"), bodyA)
	h2 := h.computeRequestHash(mkReq("/v1/messages", "Bearer x"), bodyA)
	if h1 != h2 {
		t.Fatalf("expected stable hash, got %q vs %q", h1, h2)
	}

	if h1 == h.computeRequestHash(mkReq("/v1/messages", "Bearer y"), bodyA) {
		t.Fatalf("expected auth to affect hash")
	}
	if h1 == h.computeRequestHash(mkReq("/v1/other", "Bearer x"), bodyA) {
		t.Fatalf("expected path to affect hash")
	}
	if h1 == h.computeRequestHash(mkReq("/v1/messages", "Bearer x"), bodyB) {
		t.Fatalf("expected body to affect hash")
	}
}

func TestRegisterRequest_DedupWindowAndInFlight(t *testing.T) {
	h := &Handler{recentRequests: make(map[string]*recentRequest)}
	key := "k"

	dup, inFlight := h.registerRequest(key)
	if dup || inFlight {
		t.Fatalf("first request should not be dup/inflight, got dup=%v inflight=%v", dup, inFlight)
	}

	dup, inFlight = h.registerRequest(key)
	if !dup {
		t.Fatalf("second immediate request should be treated as duplicate")
	}
	if !inFlight {
		t.Fatalf("expected inflight=true while original is in flight")
	}

	h.finishRequest(key)
	dup, inFlight = h.registerRequest(key)
	if !dup {
		t.Fatalf("request within dedup window should still be treated as duplicate")
	}
	if inFlight {
		t.Fatalf("expected inflight=false after finish")
	}
}

func TestCleanupRecentLocked_RemovesOldFinished(t *testing.T) {
	h := &Handler{recentRequests: make(map[string]*recentRequest)}
	now := time.Now()
	h.recentRequests["old"] = &recentRequest{last: now.Add(-2 * duplicateCleanupWindow), inFlight: 0}
	h.recentRequests["new"] = &recentRequest{last: now, inFlight: 0}
	h.cleanupRecentLocked(now)
	if _, ok := h.recentRequests["old"]; ok {
		t.Fatalf("expected old key to be removed")
	}
	if _, ok := h.recentRequests["new"]; !ok {
		t.Fatalf("expected new key to be kept")
	}
}
