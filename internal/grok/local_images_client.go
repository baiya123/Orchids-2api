package grok

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type imagesGenerationsResp struct {
	Data []struct {
		URL string `json:"url"`
	} `json:"data"`
}

func (h *Handler) callLocalImagesGenerations(ctx context.Context, prompt string, n int) ([]string, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, fmt.Errorf("empty prompt")
	}
	if n <= 0 {
		n = 1
	}
	// Use the same endpoint logic that is known to work.
	url := fmt.Sprintf("http://127.0.0.1:%s/grok/v1/images/generations", h.cfg.Port)
	payload := map[string]any{
		"prompt":          prompt,
		"n":               n,
		"response_format": "url",
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("images endpoint status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out imagesGenerationsResp
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode images response: %w", err)
	}
	urls := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		u := strings.TrimSpace(d.URL)
		if u != "" {
			urls = append(urls, u)
		}
	}
	return urls, nil
}
