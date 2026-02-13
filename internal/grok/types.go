package grok

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type ChatCompletionsRequest struct {
	Model       string          `json:"model"`
	Messages    []ChatMessage   `json:"messages"`
	Stream      bool            `json:"stream"`
	Thinking    *string         `json:"thinking,omitempty"`
	VideoConfig *VideoConfig    `json:"video_config,omitempty"`
	Raw         json.RawMessage `json:"-"`
}

type ChatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type VideoConfig struct {
	AspectRatio    string `json:"aspect_ratio"`
	VideoLength    int    `json:"video_length"`
	ResolutionName string `json:"resolution_name"`
	Preset         string `json:"preset"`
}

type ImagesGenerationsRequest struct {
	Model          string `json:"model"`
	Prompt         string `json:"prompt"`
	N              int    `json:"n"`
	Stream         bool   `json:"stream"`
	ResponseFormat string `json:"response_format"`
}

type RateLimitInfo struct {
	Limit     int64
	Remaining int64
	ResetAt   time.Time
}

func (r *ChatCompletionsRequest) Validate() error {
	if strings.TrimSpace(r.Model) == "" {
		return fmt.Errorf("model is required")
	}
	if len(r.Messages) == 0 {
		return fmt.Errorf("messages is required")
	}
	return nil
}

func (r *ImagesGenerationsRequest) Normalize() {
	if strings.TrimSpace(r.Model) == "" {
		r.Model = "grok-imagine-1.0"
	}
	if r.N <= 0 {
		r.N = 1
	}
	if strings.TrimSpace(r.ResponseFormat) == "" {
		r.ResponseFormat = "url"
	}
}

func (v *VideoConfig) Normalize() {
	if v == nil {
		return
	}
	if strings.TrimSpace(v.AspectRatio) == "" {
		v.AspectRatio = "3:2"
	}
	if v.VideoLength == 0 {
		v.VideoLength = 6
	}
	if strings.TrimSpace(v.ResolutionName) == "" {
		v.ResolutionName = "480p"
	}
	if strings.TrimSpace(v.Preset) == "" {
		v.Preset = "custom"
	}
}
