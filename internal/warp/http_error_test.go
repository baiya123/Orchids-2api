package warp

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestHTTPStatusCode(t *testing.T) {
	t.Parallel()

	err := &HTTPStatusError{Operation: "refresh token", StatusCode: 429}
	if got := HTTPStatusCode(err); got != 429 {
		t.Fatalf("expected 429, got %d", got)
	}

	wrapped := errors.New("wrapped: " + err.Error())
	if got := HTTPStatusCode(wrapped); got != 0 {
		t.Fatalf("expected 0 for non-typed wrapped error, got %d", got)
	}
}

func TestRetryAfter(t *testing.T) {
	t.Parallel()

	err := &HTTPStatusError{Operation: "refresh token", StatusCode: 429, RetryAfter: 15 * time.Second}
	if got := RetryAfter(err); got != 15*time.Second {
		t.Fatalf("expected 15s, got %s", got)
	}
}

func TestParseRetryAfterHeader(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 2, 8, 13, 0, 0, 0, time.UTC)

	if got := parseRetryAfterHeader("120", now); got != 120*time.Second {
		t.Fatalf("expected 120s, got %s", got)
	}

	dateValue := now.Add(90 * time.Second).UTC().Format(http.TimeFormat)
	got := parseRetryAfterHeader(dateValue, now)
	if got < 89*time.Second || got > 91*time.Second {
		t.Fatalf("expected ~90s, got %s", got)
	}

	if got := parseRetryAfterHeader("", now); got != 0 {
		t.Fatalf("expected 0 for empty header, got %s", got)
	}

	if got := parseRetryAfterHeader("invalid", now); got != 0 {
		t.Fatalf("expected 0 for invalid header, got %s", got)
	}
}
