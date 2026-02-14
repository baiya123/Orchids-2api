package grok

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	imagineSessionTTL      = 10 * time.Minute
	imagineBatchImageCount = 6
)

var imagineUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type imagineSession struct {
	Prompt      string
	AspectRatio string
	CreatedAt   time.Time
}

var (
	imagineSessionsMu sync.Mutex
	imagineSessions   = map[string]imagineSession{}
)

type imagineStartRequest struct {
	Prompt      string `json:"prompt"`
	AspectRatio string `json:"aspect_ratio"`
}

type imagineStopRequest struct {
	TaskIDs []string `json:"task_ids"`
}

type imagineImage struct {
	B64 string
	URL string
}

func cleanupImagineSessionsLocked(now time.Time) {
	for id, session := range imagineSessions {
		if now.Sub(session.CreatedAt) > imagineSessionTTL {
			delete(imagineSessions, id)
		}
	}
}

func createImagineSession(prompt, aspectRatio string) string {
	id := randomHex(16)
	if id == "" {
		id = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	now := time.Now()

	imagineSessionsMu.Lock()
	defer imagineSessionsMu.Unlock()
	cleanupImagineSessionsLocked(now)
	imagineSessions[id] = imagineSession{
		Prompt:      strings.TrimSpace(prompt),
		AspectRatio: resolveAspectRatio(strings.TrimSpace(aspectRatio)),
		CreatedAt:   now,
	}
	return id
}

func getImagineSession(taskID string) (imagineSession, bool) {
	id := strings.TrimSpace(taskID)
	if id == "" {
		return imagineSession{}, false
	}
	now := time.Now()
	imagineSessionsMu.Lock()
	defer imagineSessionsMu.Unlock()
	cleanupImagineSessionsLocked(now)
	session, ok := imagineSessions[id]
	if !ok {
		return imagineSession{}, false
	}
	if now.Sub(session.CreatedAt) > imagineSessionTTL {
		delete(imagineSessions, id)
		return imagineSession{}, false
	}
	return session, true
}

func deleteImagineSession(taskID string) {
	id := strings.TrimSpace(taskID)
	if id == "" {
		return
	}
	imagineSessionsMu.Lock()
	delete(imagineSessions, id)
	imagineSessionsMu.Unlock()
}

func deleteImagineSessions(taskIDs []string) int {
	removed := 0
	imagineSessionsMu.Lock()
	for _, raw := range taskIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, ok := imagineSessions[id]; ok {
			delete(imagineSessions, id)
			removed++
		}
	}
	imagineSessionsMu.Unlock()
	return removed
}

func ensureImageAspectRatio(payload map[string]interface{}, ratio string) {
	if payload == nil {
		return
	}
	if strings.TrimSpace(ratio) == "" {
		ratio = "2:3"
	}
	ratio = resolveAspectRatio(ratio)

	responseMetadata, _ := payload["responseMetadata"].(map[string]interface{})
	if responseMetadata == nil {
		responseMetadata = map[string]interface{}{}
		payload["responseMetadata"] = responseMetadata
	}
	modelConfigOverride, _ := responseMetadata["modelConfigOverride"].(map[string]interface{})
	if modelConfigOverride == nil {
		modelConfigOverride = map[string]interface{}{}
		responseMetadata["modelConfigOverride"] = modelConfigOverride
	}
	modelMap, _ := modelConfigOverride["modelMap"].(map[string]interface{})
	if modelMap == nil {
		modelMap = map[string]interface{}{}
		modelConfigOverride["modelMap"] = modelMap
	}
	modelMap["imageGenModelConfig"] = map[string]interface{}{
		"aspectRatio": ratio,
	}
}

func (h *Handler) generateImagineBatch(ctx context.Context, prompt, aspectRatio string, n int) ([]imagineImage, int, error) {
	if err := h.ensureModelEnabled(ctx, "grok-imagine-1.0"); err != nil {
		return nil, 0, err
	}
	spec, ok := ResolveModel("grok-imagine-1.0")
	if !ok || !spec.IsImage {
		return nil, 0, fmt.Errorf("image model not supported")
	}
	if n < 1 {
		n = 1
	}

	acc, token, err := h.selectAccount(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("no available grok token: %w", err)
	}
	release := h.trackAccount(acc)
	defer release()

	payload := h.client.chatPayload(spec, "Image Generation: "+strings.TrimSpace(prompt), true, 2)
	ensureImageAspectRatio(payload, aspectRatio)

	callsNeeded := (n + 1) / 2
	if callsNeeded < 1 {
		callsNeeded = 1
	}

	startedAt := time.Now()
	var urls []string
	for i := 0; i < callsNeeded; i++ {
		resp, err := h.client.doChat(ctx, token, payload)
		if err != nil {
			return nil, 0, err
		}
		parseErr := parseUpstreamLines(resp.Body, func(line map[string]interface{}) error {
			if mr, ok := line["modelResponse"].(map[string]interface{}); ok {
				urls = append(urls, extractImageURLs(mr)...)
			}
			return nil
		})
		resp.Body.Close()
		if parseErr != nil {
			return nil, 0, parseErr
		}
	}

	urls = uniqueStrings(urls)
	if len(urls) == 0 {
		return nil, 0, fmt.Errorf("no image generated")
	}
	if len(urls) > n {
		urls = urls[:n]
	}

	images := make([]imagineImage, 0, len(urls))
	for _, u := range urls {
		data, mimeType, err := h.client.downloadAsset(ctx, token, u)
		if err != nil {
			slog.Warn("imagine image download failed", "url", u, "error", err)
			continue
		}
		if len(data) == 0 {
			continue
		}
		b64 := base64.StdEncoding.EncodeToString(data)
		fileURL := ""
		if name, err := h.cacheMediaBytes(u, "image", data, mimeType); err == nil && name != "" {
			fileURL = "/grok/v1/files/image/" + name
		}
		if strings.TrimSpace(b64) != "" || fileURL != "" {
			images = append(images, imagineImage{B64: b64, URL: fileURL})
		}
	}
	if len(images) == 0 {
		return nil, 0, fmt.Errorf("no usable image generated")
	}

	return images, int(time.Since(startedAt) / time.Millisecond), nil
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (h *Handler) runImagineLoop(
	ctx context.Context,
	prompt string,
	aspectRatio string,
	taskID string,
	deleteSessionOnExit bool,
	emit func(map[string]interface{}) bool,
) {
	runID := randomHex(12)
	if runID == "" {
		runID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	sequence := 0
	if !emit(map[string]interface{}{
		"type":         "status",
		"status":       "running",
		"prompt":       prompt,
		"aspect_ratio": aspectRatio,
		"run_id":       runID,
	}) {
		return
	}

	defer func() {
		_ = emit(map[string]interface{}{
			"type":   "status",
			"status": "stopped",
			"run_id": runID,
		})
		if deleteSessionOnExit && strings.TrimSpace(taskID) != "" {
			deleteImagineSession(taskID)
		}
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		if strings.TrimSpace(taskID) != "" {
			if _, ok := getImagineSession(taskID); !ok {
				return
			}
		}

		images, elapsedMS, err := h.generateImagineBatch(ctx, prompt, aspectRatio, imagineBatchImageCount)
		if err != nil {
			if !emit(map[string]interface{}{
				"type":    "error",
				"message": err.Error(),
				"code":    "internal_error",
			}) {
				return
			}
			if !sleepWithContext(ctx, 1500*time.Millisecond) {
				return
			}
			continue
		}

		nowMillis := time.Now().UnixMilli()
		for _, img := range images {
			sequence++
			if !emit(map[string]interface{}{
				"type":         "image",
				"b64_json":     img.B64,
				"file_url":     img.URL,
				"sequence":     sequence,
				"created_at":   nowMillis,
				"elapsed_ms":   elapsedMS,
				"aspect_ratio": aspectRatio,
				"run_id":       runID,
			}) {
				return
			}
		}
	}
}

func (h *Handler) HandleAdminImagineStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req imagineStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		http.Error(w, "prompt cannot be empty", http.StatusBadRequest)
		return
	}
	ratio := resolveAspectRatio(strings.TrimSpace(req.AspectRatio))
	taskID := createImagineSession(prompt, ratio)
	out := map[string]interface{}{
		"task_id":      taskID,
		"aspect_ratio": ratio,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *Handler) HandleAdminImagineStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req imagineStopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	removed := deleteImagineSessions(req.TaskIDs)
	out := map[string]interface{}{
		"status":  "success",
		"removed": removed,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *Handler) HandleAdminImagineSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("task_id"))
	prompt := strings.TrimSpace(r.URL.Query().Get("prompt"))
	ratio := strings.TrimSpace(r.URL.Query().Get("aspect_ratio"))

	if taskID != "" {
		session, ok := getImagineSession(taskID)
		if !ok {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
		prompt = session.Prompt
		ratio = session.AspectRatio
	}
	if prompt == "" {
		http.Error(w, "prompt cannot be empty", http.StatusBadRequest)
		return
	}
	ratio = resolveAspectRatio(ratio)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	emit := func(payload map[string]interface{}) bool {
		writeSSE(w, "", encodeJSON(payload))
		if flusher != nil {
			flusher.Flush()
		}
		return r.Context().Err() == nil
	}

	h.runImagineLoop(r.Context(), prompt, ratio, taskID, true, emit)
}

func (h *Handler) HandleAdminImagineWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	taskID := strings.TrimSpace(r.URL.Query().Get("task_id"))
	if taskID != "" {
		if _, ok := getImagineSession(taskID); !ok {
			http.Error(w, "task not found", http.StatusNotFound)
			return
		}
	}

	conn, err := imagineUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var writeMu sync.Mutex
	send := func(payload map[string]interface{}) bool {
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := conn.WriteJSON(payload); err != nil {
			return false
		}
		return true
	}

	var runMu sync.Mutex
	var runCancel context.CancelFunc
	var runDone chan struct{}

	stopRun := func() {
		runMu.Lock()
		cancelFn := runCancel
		done := runDone
		runCancel = nil
		runDone = nil
		runMu.Unlock()

		if cancelFn != nil {
			cancelFn()
		}
		if done != nil {
			<-done
		}
	}
	defer stopRun()

	startRun := func(prompt, ratio string) {
		stopRun()
		runCtx, cancelFn := context.WithCancel(ctx)
		done := make(chan struct{})
		runMu.Lock()
		runCancel = cancelFn
		runDone = done
		runMu.Unlock()
		go func() {
			defer close(done)
			h.runImagineLoop(runCtx, prompt, ratio, taskID, false, send)
		}()
	}

	for {
		var payload map[string]interface{}
		if err := conn.ReadJSON(&payload); err != nil {
			break
		}
		msgType := strings.ToLower(strings.TrimSpace(fmt.Sprint(payload["type"])))
		switch msgType {
		case "start":
			prompt := strings.TrimSpace(fmt.Sprint(payload["prompt"]))
			ratio := strings.TrimSpace(fmt.Sprint(payload["aspect_ratio"]))
			if taskID != "" {
				if session, ok := getImagineSession(taskID); ok {
					if prompt == "" {
						prompt = session.Prompt
					}
					if ratio == "" {
						ratio = session.AspectRatio
					}
				}
			}
			if prompt == "" {
				_ = send(map[string]interface{}{
					"type":    "error",
					"message": "prompt cannot be empty",
					"code":    "empty_prompt",
				})
				continue
			}
			if ratio == "" {
				ratio = "2:3"
			}
			ratio = resolveAspectRatio(ratio)
			startRun(prompt, ratio)
		case "stop":
			stopRun()
		case "ping":
			_ = send(map[string]interface{}{"type": "pong"})
		default:
			_ = send(map[string]interface{}{
				"type":    "error",
				"message": "unknown command",
				"code":    "unknown_command",
			})
		}
	}

	if taskID != "" {
		deleteImagineSession(taskID)
	}
}
