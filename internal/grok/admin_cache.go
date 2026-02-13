package grok

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type cacheEntry struct {
	MediaType string `json:"media_type"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	UpdatedAt int64  `json:"updated_at"`
}

type cacheClearRequest struct {
	MediaType string `json:"media_type"`
}

type cacheDeleteItemRequest struct {
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
	Name      string `json:"name"`
	FileName  string `json:"file_name"`
}

func validCacheMediaType(mediaType string) bool {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image", "video":
		return true
	default:
		return false
	}
}

func listCachedEntries(mediaType string) ([]cacheEntry, int64, error) {
	typ := strings.ToLower(strings.TrimSpace(mediaType))
	if !validCacheMediaType(typ) {
		return nil, 0, nil
	}
	dir := filepath.Join(cacheBaseDir, typ)
	items, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []cacheEntry{}, 0, nil
		}
		return nil, 0, err
	}

	out := make([]cacheEntry, 0, len(items))
	var totalSize int64
	for _, item := range items {
		if !item.Type().IsRegular() {
			continue
		}
		name := sanitizeCachedFilename(item.Name())
		if name == "" {
			continue
		}
		info, err := item.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		totalSize += info.Size()
		out = append(out, cacheEntry{
			MediaType: typ,
			Name:      name,
			Path:      typ + "/" + name,
			URL:       "/grok/v1/files/" + typ + "/" + name,
			Size:      info.Size(),
			UpdatedAt: info.ModTime().UnixMilli(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out, totalSize, nil
}

func parseCacheDeleteTarget(req cacheDeleteItemRequest) (string, string, bool) {
	mediaType := strings.ToLower(strings.TrimSpace(req.MediaType))
	name := sanitizeCachedFilename(strings.TrimSpace(req.Name))
	if name == "" {
		name = sanitizeCachedFilename(strings.TrimSpace(req.FileName))
	}
	if validCacheMediaType(mediaType) && name != "" {
		return mediaType, name, true
	}

	rawPath := strings.TrimSpace(req.Path)
	if rawPath == "" {
		return "", "", false
	}
	if !strings.HasPrefix(rawPath, "/") {
		rawPath = "/grok/v1/files/" + strings.TrimLeft(rawPath, "/")
	}
	mt, fn, ok := parseFilesPath(rawPath)
	if !ok {
		return "", "", false
	}
	return mt, fn, true
}

func (h *Handler) HandleAdminCache(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	images, imageSize, err := listCachedEntries("image")
	if err != nil {
		http.Error(w, "failed to list cache", http.StatusInternalServerError)
		return
	}
	videos, videoSize, err := listCachedEntries("video")
	if err != nil {
		http.Error(w, "failed to list cache", http.StatusInternalServerError)
		return
	}
	out := map[string]interface{}{
		"status":   "success",
		"base_dir": cacheBaseDir,
		"image": map[string]interface{}{
			"count": len(images),
			"bytes": imageSize,
		},
		"video": map[string]interface{}{
			"count": len(videos),
			"bytes": videoSize,
		},
		"total": map[string]interface{}{
			"count": len(images) + len(videos),
			"bytes": imageSize + videoSize,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *Handler) HandleAdminCacheList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	mediaType := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("media_type")))
	entries := make([]cacheEntry, 0)
	if mediaType == "" {
		img, _, err := listCachedEntries("image")
		if err != nil {
			http.Error(w, "failed to list cache", http.StatusInternalServerError)
			return
		}
		vid, _, err := listCachedEntries("video")
		if err != nil {
			http.Error(w, "failed to list cache", http.StatusInternalServerError)
			return
		}
		entries = append(entries, img...)
		entries = append(entries, vid...)
		sort.Slice(entries, func(i, j int) bool { return entries[i].UpdatedAt > entries[j].UpdatedAt })
	} else {
		if !validCacheMediaType(mediaType) {
			http.Error(w, "invalid media_type", http.StatusBadRequest)
			return
		}
		list, _, err := listCachedEntries(mediaType)
		if err != nil {
			http.Error(w, "failed to list cache", http.StatusInternalServerError)
			return
		}
		entries = list
	}
	out := map[string]interface{}{
		"status": "success",
		"items":  entries,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *Handler) HandleAdminCacheClear(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req cacheClearRequest
	_ = json.NewDecoder(r.Body).Decode(&req)

	mediaTypes := []string{"image", "video"}
	if strings.TrimSpace(req.MediaType) != "" {
		typ := strings.ToLower(strings.TrimSpace(req.MediaType))
		if !validCacheMediaType(typ) {
			http.Error(w, "invalid media_type", http.StatusBadRequest)
			return
		}
		mediaTypes = []string{typ}
	}

	removedFiles := 0
	removedBytes := int64(0)
	for _, typ := range mediaTypes {
		list, size, err := listCachedEntries(typ)
		if err != nil {
			http.Error(w, "failed to read cache", http.StatusInternalServerError)
			return
		}
		removedFiles += len(list)
		removedBytes += size
		dir := filepath.Join(cacheBaseDir, typ)
		if err := os.RemoveAll(dir); err != nil {
			http.Error(w, "failed to clear cache", http.StatusInternalServerError)
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			http.Error(w, "failed to recreate cache dir", http.StatusInternalServerError)
			return
		}
	}

	out := map[string]interface{}{
		"status":        "success",
		"removed_count": removedFiles,
		"removed_bytes": removedBytes,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (h *Handler) HandleAdminCacheItemDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req cacheDeleteItemRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	mediaType, name, ok := parseCacheDeleteTarget(req)
	if !ok {
		http.Error(w, "invalid delete target", http.StatusBadRequest)
		return
	}

	full := filepath.Join(cacheBaseDir, mediaType, name)
	err := os.Remove(full)
	removed := true
	if err != nil {
		if os.IsNotExist(err) {
			removed = false
		} else {
			http.Error(w, "failed to delete cache item", http.StatusInternalServerError)
			return
		}
	}
	out := map[string]interface{}{
		"status":     "success",
		"removed":    removed,
		"media_type": mediaType,
		"name":       name,
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
