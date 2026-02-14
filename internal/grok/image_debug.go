package grok

import (
	"encoding/json"
	"net/http"
)

// maybeAddImageDebug adds best-effort debug info to the /v1/images/* responses.
// It is gated behind the query param `debug=1` to avoid breaking strict clients.
func maybeAddImageDebug(r *http.Request, out map[string]interface{}, requestedN int, uniqueURLs int, attempts int, stoppedReason string) {
	q := r.URL.Query()
	if q.Get("debug") != "1" {
		return
	}
	dbg := map[string]interface{}{
		"requested_n":   requestedN,
		"unique_urls":   uniqueURLs,
		"attempts":      attempts,
		"stop_reason":   stoppedReason,
		"debug_version": 1,
	}
	// Keep JSON-friendly
	_, _ = json.Marshal(dbg)
	out["debug"] = dbg
}
