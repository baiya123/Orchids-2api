package grok

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseFilesPath(t *testing.T) {
	tests := []struct {
		path      string
		wantType  string
		wantFile  string
		wantMatch bool
	}{
		{path: "/grok/v1/files/image/a.jpg", wantType: "image", wantFile: "a.jpg", wantMatch: true},
		{path: "/grok/v1/files/video/sub/path.mp4", wantType: "video", wantFile: "sub-path.mp4", wantMatch: true},
		{path: "/grok/v1/files/other/a.bin", wantMatch: false},
		{path: "/grok/v1/files/image/../a.jpg", wantMatch: false},
		{path: "/v1/files/image/a.jpg", wantMatch: false},
	}
	for _, tt := range tests {
		gotType, gotFile, ok := parseFilesPath(tt.path)
		if ok != tt.wantMatch {
			t.Fatalf("parseFilesPath(%q) ok=%v want=%v", tt.path, ok, tt.wantMatch)
		}
		if !ok {
			continue
		}
		if gotType != tt.wantType || gotFile != tt.wantFile {
			t.Fatalf("parseFilesPath(%q)=(%q,%q) want=(%q,%q)", tt.path, gotType, gotFile, tt.wantType, tt.wantFile)
		}
	}
}

func TestHandleFiles(t *testing.T) {
	oldBase := cacheBaseDir
	cacheBaseDir = t.TempDir()
	t.Cleanup(func() { cacheBaseDir = oldBase })

	imageDir := filepath.Join(cacheBaseDir, "image")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	full := filepath.Join(imageDir, "sample.jpg")
	if err := os.WriteFile(full, []byte("abc"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	h := &Handler{}

	req := httptest.NewRequest(http.MethodGet, "/grok/v1/files/image/sample.jpg", nil)
	rec := httptest.NewRecorder()
	h.HandleFiles(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=200", rec.Code)
	}
	if body := rec.Body.String(); body != "abc" {
		t.Fatalf("body=%q want=abc", body)
	}

	req404 := httptest.NewRequest(http.MethodGet, "/grok/v1/files/image/notfound.jpg", nil)
	rec404 := httptest.NewRecorder()
	h.HandleFiles(rec404, req404)
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=404", rec404.Code)
	}
}
