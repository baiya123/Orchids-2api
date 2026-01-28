package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"orchids-api/internal/config"
	"orchids-api/internal/debug"
	"orchids-api/internal/store"
)

const upstreamURL = "https://orchids-server.calmstone-6964e08a.westeurope.azurecontainerapps.io/agent/coding-agent"

const (
	defaultTokenTTL = 5 * time.Minute
	tokenExpirySkew = 30 * time.Second
)

type Client struct {
	config     *config.Config
	account    *store.Account
	httpClient *http.Client
}

type TokenResponse struct {
	JWT string `json:"jwt"`
}

type AgentRequest struct {
	Prompt        string        `json:"prompt"`
	ChatHistory   []interface{} `json:"chatHistory"`
	ProjectID     string        `json:"projectId"`
	CurrentPage   interface{}   `json:"currentPage"`
	AgentMode     string        `json:"agentMode"`
	Mode          string        `json:"mode"`
	GitRepoUrl    string        `json:"gitRepoUrl"`
	Email         string        `json:"email"`
	ChatSessionID int           `json:"chatSessionId"`
	UserID        string        `json:"userId"`
	APIVersion    int           `json:"apiVersion"`
	Model         string        `json:"model,omitempty"`
}

type SSEMessage struct {
	Type  string                 `json:"type"`
	Event map[string]interface{} `json:"event,omitempty"`
	Raw   map[string]interface{} `json:"-"`
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

var tokenCache = struct {
	mu    sync.RWMutex
	items map[string]cachedToken
}{
	items: map[string]cachedToken{},
}

func New(cfg *config.Config) *Client {
	return &Client{
		config:     cfg,
		httpClient: &http.Client{},
	}
}

func NewFromAccount(acc *store.Account) *Client {
	cfg := &config.Config{
		SessionID:    acc.SessionID,
		ClientCookie: acc.ClientCookie,
		ClientUat:    acc.ClientUat,
		ProjectID:    acc.ProjectID,
		UserID:       acc.UserID,
		AgentMode:    acc.AgentMode,
		Email:        acc.Email,
	}
	return &Client{
		config:     cfg,
		account:    acc,
		httpClient: &http.Client{},
	}
}

func (c *Client) GetToken() (string, error) {
	if token := os.Getenv("UPSTREAM_TOKEN"); token != "" {
		return token, nil
	}

	if cached, ok := getCachedToken(c.config.SessionID); ok {
		return cached, nil
	}

	url := fmt.Sprintf("https://clerk.orchids.app/v1/client/sessions/%s/tokens?__clerk_api_version=2025-11-10&_clerk_js_version=5.117.0", c.config.SessionID)

	req, err := http.NewRequest("POST", url, strings.NewReader("organization_id="))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", c.config.GetCookies())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token request failed with status %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", err
	}

	setCachedToken(c.config.SessionID, tokenResp.JWT)
	return tokenResp.JWT, nil
}

func (c *Client) SendRequest(ctx context.Context, prompt string, chatHistory []interface{}, model string, onMessage func(SSEMessage), logger *debug.Logger) error {
	token, err := c.GetToken()
	if err != nil {
		return fmt.Errorf("failed to get token: %w", err)
	}

	payload := AgentRequest{
		Prompt:        prompt,
		ChatHistory:   chatHistory,
		ProjectID:     c.config.ProjectID,
		CurrentPage:   map[string]interface{}{},
		AgentMode:     c.config.AgentMode,
		Mode:          "agent",
		GitRepoUrl:    "",
		Email:         c.config.Email,
		ChatSessionID: rand.IntN(90000000) + 10000000,
		UserID:        c.config.UserID,
		APIVersion:    2,
		Model:         model,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := getUpstreamURL()
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Orchids-Api-Version", "2")

	// 记录上游请求
	if logger != nil {
		headers := map[string]string{
			"Accept":                "text/event-stream",
			"Authorization":         "Bearer [REDACTED]",
			"Content-Type":          "application/json",
			"X-Orchids-Api-Version": "2",
		}
		logger.LogUpstreamRequest(url, headers, payload)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upstream request failed with status %d: %s", resp.StatusCode, string(body))
	}

	reader := bufio.NewReader(resp.Body)
	var buffer strings.Builder

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		buffer.WriteString(line)

		if line == "\n" {
			eventData := buffer.String()
			buffer.Reset()

			lines := strings.Split(eventData, "\n")
			for _, l := range lines {
				if strings.HasPrefix(l, "data: ") {
					rawData := strings.TrimPrefix(l, "data: ")

					var msg map[string]interface{}
					if err := json.Unmarshal([]byte(rawData), &msg); err != nil {
						continue
					}

					msgType, _ := msg["type"].(string)

					// 记录上游 SSE
					if logger != nil {
						logger.LogUpstreamSSE(msgType, rawData)
					}

					// 只处理 "model" 类型的事件
					if msgType != "model" {
						continue
					}

					sseMsg := SSEMessage{
						Type: msgType,
						Raw:  msg,
					}

					if event, ok := msg["event"].(map[string]interface{}); ok {
						sseMsg.Event = event
					}

					onMessage(sseMsg)
				}
			}
		}
	}

	return nil
}

func getUpstreamURL() string {
	if value := os.Getenv("UPSTREAM_URL"); value != "" {
		return value
	}
	return upstreamURL
}

func getCachedToken(sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}

	tokenCache.mu.RLock()
	entry, ok := tokenCache.items[sessionID]
	tokenCache.mu.RUnlock()
	if !ok {
		return "", false
	}

	if time.Now().After(entry.expiresAt) {
		tokenCache.mu.Lock()
		delete(tokenCache.items, sessionID)
		tokenCache.mu.Unlock()
		return "", false
	}

	return entry.token, true
}

func setCachedToken(sessionID, token string) {
	if sessionID == "" || token == "" {
		return
	}

	expiresAt := tokenExpiry(token)
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(defaultTokenTTL)
	}

	tokenCache.mu.Lock()
	tokenCache.items[sessionID] = cachedToken{
		token:     token,
		expiresAt: expiresAt,
	}
	tokenCache.mu.Unlock()
}

func tokenExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}
	}

	expValue, ok := claims["exp"]
	if !ok {
		return time.Time{}
	}

	var exp int64
	switch v := expValue.(type) {
	case float64:
		exp = int64(v)
	case json.Number:
		exp, _ = v.Int64()
	}

	if exp == 0 {
		return time.Time{}
	}

	return time.Unix(exp, 0).Add(-tokenExpirySkew)
}
