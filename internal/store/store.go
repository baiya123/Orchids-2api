package store

import (
	"context"
	"fmt"
	"log/slog"
	"orchids-api/internal/model"
	"sync"
	"time"
)

var ErrNoRows = fmt.Errorf("no rows in result set")

type Account struct {
	ID           int64     `json:"id"`
	Name         string    `json:"name"`
	SessionID    string    `json:"session_id"`
	ClientCookie string    `json:"client_cookie"`
	ClientUat    string    `json:"client_uat"`
	ProjectID    string    `json:"project_id"`
	UserID       string    `json:"user_id"`
	AgentMode    string    `json:"agent_mode"`
	Email        string    `json:"email"`
	Weight       int       `json:"weight"`
	Enabled      bool      `json:"enabled"`
	Token        string    `json:"token"`        // Truncated display token
	Subscription string    `json:"subscription"` // "free", "pro", etc.
	UsageCurrent float64   `json:"usage_current"`
	UsageTotal   float64   `json:"usage_total"`
	ResetDate    string    `json:"reset_date"`
	RequestCount int64     `json:"request_count"`
	LastUsedAt   time.Time `json:"last_used_at"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Settings struct {
	ID    int64  `json:"id"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

type ApiKey struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	KeyHash    string     `json:"-"`
	KeyFull    string     `json:"key_full,omitempty"`
	KeyPrefix  string     `json:"key_prefix"`
	KeySuffix  string     `json:"key_suffix"`
	Enabled    bool       `json:"enabled"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

type Store struct {
	mu       sync.RWMutex
	accounts accountStore
	settings settingsStore
	apiKeys  apiKeyStore
	models   modelStore
}

type Options struct {
	StoreMode     string
	RedisAddr     string
	RedisPassword string
	RedisDB       int
	RedisPrefix   string
}

type accountStore interface {
	CreateAccount(ctx context.Context, acc *Account) error
	UpdateAccount(ctx context.Context, acc *Account) error
	DeleteAccount(ctx context.Context, id int64) error
	GetAccount(ctx context.Context, id int64) (*Account, error)
	ListAccounts(ctx context.Context) ([]*Account, error)
	GetEnabledAccounts(ctx context.Context) ([]*Account, error)
	IncrementRequestCount(ctx context.Context, id int64) error
}

type settingsStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	SetSetting(ctx context.Context, key, value string) error
}

type apiKeyStore interface {
	CreateApiKey(ctx context.Context, key *ApiKey) error
	ListApiKeys(ctx context.Context) ([]*ApiKey, error)
	GetApiKeyByHash(ctx context.Context, hash string) (*ApiKey, error)
	UpdateApiKeyEnabled(ctx context.Context, id int64, enabled bool) error
	UpdateApiKeyLastUsed(ctx context.Context, id int64) error
	DeleteApiKey(ctx context.Context, id int64) error
	GetApiKeyByID(ctx context.Context, id int64) (*ApiKey, error)
}

type modelStore interface {
	CreateModel(ctx context.Context, m *model.Model) error
	UpdateModel(ctx context.Context, m *model.Model) error
	DeleteModel(ctx context.Context, id string) error
	GetModel(ctx context.Context, id string) (*model.Model, error)
	ListModels(ctx context.Context) ([]*model.Model, error)
}

type closeableStore interface {
	Close() error
}

func New(opts Options) (*Store, error) {
	store := &Store{}
	redisStore, err := newRedisStore(opts.RedisAddr, opts.RedisPassword, opts.RedisDB, opts.RedisPrefix)
	if err != nil {
		return nil, fmt.Errorf("failed to init redis store: %w", err)
	}
	store.accounts = redisStore
	store.settings = redisStore
	store.apiKeys = redisStore
	store.models = redisStore
	if err := store.seedModels(); err != nil {
		slog.Warn("failed to seed models in redis", "error", err)
	}
	return store, nil
}

func (s *Store) seedModels() error {
	ctx := context.Background() // Seed occurs at startup, background is OK
	var count int
	if s.models != nil {
		models, err := s.models.ListModels(ctx)
		if err == nil {
			count = len(models)
		}
	}

	if count > 0 {
		return nil
	}

	models := []model.Model{
		// Antigravity
		{ID: "11", Channel: "Antigravity", ModelID: "gemini-2.5-flash-preview", Name: "Gemini 2.5 Flash", Status: true, IsDefault: true, SortOrder: 0},
		{ID: "12", Channel: "Antigravity", ModelID: "gemini-3-flash-preview", Name: "Gemini 3 Flash", Status: true, IsDefault: false, SortOrder: 1},
		{ID: "13", Channel: "Antigravity", ModelID: "gemini-3-pro-preview", Name: "Gemini 3 Pro", Status: true, IsDefault: false, SortOrder: 2},
		{ID: "14", Channel: "Antigravity", ModelID: "gemini-3-pro-image-preview", Name: "Gemini 3 Pro Image", Status: true, IsDefault: false, SortOrder: 3},
		{ID: "15", Channel: "Antigravity", ModelID: "gemini-2.5-computer-use-preview-1022", Name: "Gemini 2.5 Computer Use", Status: true, IsDefault: false, SortOrder: 4},
		// Warp
		{ID: "19", Channel: "Warp", ModelID: "claude-4-sonnet", Name: "Claude 4 Sonnet", Status: true, IsDefault: false, SortOrder: 0},
		{ID: "20", Channel: "Warp", ModelID: "claude-4.5-sonnet", Name: "Claude 4.5 Sonnet", Status: true, IsDefault: false, SortOrder: 1},
		{ID: "21", Channel: "Warp", ModelID: "claude-4.5-sonnet-thinking", Name: "Claude 4.5 Sonnet Thinking", Status: true, IsDefault: false, SortOrder: 2},
		{ID: "22", Channel: "Warp", ModelID: "claude-4.5-opus", Name: "Claude 4.5 Opus", Status: true, IsDefault: true, SortOrder: 3},
		// Orchids
		{ID: "6", Channel: "Orchids", ModelID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5", Status: true, IsDefault: true, SortOrder: 0},
		{ID: "7", Channel: "Orchids", ModelID: "claude-opus-4-5", Name: "Claude Opus 4.5", Status: true, IsDefault: false, SortOrder: 1},
		{ID: "8", Channel: "Orchids", ModelID: "claude-sonnet-4-5-thinking", Name: "Claude Sonnet 4.5 Thinking", Status: true, IsDefault: false, SortOrder: 2},
		// Kiro
		{ID: "1", Channel: "Kiro", ModelID: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5", Status: true, IsDefault: true, SortOrder: 0},
		{ID: "2", Channel: "Kiro", ModelID: "claude-opus-4-5", Name: "Claude Opus 4.5", Status: true, IsDefault: false, SortOrder: 1},
	}

	for _, m := range models {
		if err := s.CreateModel(ctx, &m); err != nil {
			slog.Warn("Failed to seed model", "model_id", m.ModelID, "error", err)
		}
	}
	return nil
}

func (s *Store) Close() error {
	if s.accounts != nil {
		if closer, ok := s.accounts.(closeableStore); ok {
			return closer.Close()
		}
	}
	return nil
}

func (s *Store) CreateAccount(ctx context.Context, acc *Account) error {
	if s.accounts != nil {
		return s.accounts.CreateAccount(ctx, acc)
	}
	return fmt.Errorf("store not configured")
}

func (s *Store) UpdateAccount(ctx context.Context, acc *Account) error {
	if s.accounts != nil {
		return s.accounts.UpdateAccount(ctx, acc)
	}
	return fmt.Errorf("store not configured")
}

func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	if s.accounts != nil {
		return s.accounts.DeleteAccount(ctx, id)
	}
	return fmt.Errorf("store not configured")
}

func (s *Store) GetAccount(ctx context.Context, id int64) (*Account, error) {
	if s.accounts != nil {
		return s.accounts.GetAccount(ctx, id)
	}
	return nil, fmt.Errorf("store not configured")
}

func (s *Store) ListAccounts(ctx context.Context) ([]*Account, error) {
	if s.accounts != nil {
		return s.accounts.ListAccounts(ctx)
	}
	return nil, fmt.Errorf("store not configured")
}

func (s *Store) GetEnabledAccounts(ctx context.Context) ([]*Account, error) {
	if s.accounts != nil {
		return s.accounts.GetEnabledAccounts(ctx)
	}
	return nil, fmt.Errorf("store not configured")
}

func (s *Store) IncrementRequestCount(ctx context.Context, id int64) error {
	if s.accounts != nil {
		return s.accounts.IncrementRequestCount(ctx, id)
	}
	return fmt.Errorf("store not configured")
}

func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	if s.settings != nil {
		return s.settings.GetSetting(ctx, key)
	}
	return "", fmt.Errorf("settings store not configured")
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if s.settings != nil {
		return s.settings.SetSetting(ctx, key, value)
	}
	return fmt.Errorf("settings store not configured")
}

func (s *Store) CreateApiKey(ctx context.Context, key *ApiKey) error {
	if s.apiKeys != nil {
		return s.apiKeys.CreateApiKey(ctx, key)
	}
	return fmt.Errorf("api keys store not configured")
}

func (s *Store) ListApiKeys(ctx context.Context) ([]*ApiKey, error) {
	if s.apiKeys != nil {
		return s.apiKeys.ListApiKeys(ctx)
	}
	return nil, fmt.Errorf("api keys store not configured")
}

func (s *Store) GetApiKeyByHash(ctx context.Context, hash string) (*ApiKey, error) {
	if s.apiKeys != nil {
		return s.apiKeys.GetApiKeyByHash(ctx, hash)
	}
	return nil, fmt.Errorf("api keys store not configured")
}

func (s *Store) UpdateApiKeyEnabled(ctx context.Context, id int64, enabled bool) error {
	if s.apiKeys != nil {
		return s.apiKeys.UpdateApiKeyEnabled(ctx, id, enabled)
	}
	return fmt.Errorf("api keys store not configured")
}

func (s *Store) UpdateApiKeyLastUsed(ctx context.Context, id int64) error {
	if s.apiKeys != nil {
		return s.apiKeys.UpdateApiKeyLastUsed(ctx, id)
	}
	return fmt.Errorf("api keys store not configured")
}

func (s *Store) DeleteApiKey(ctx context.Context, id int64) error {
	if s.apiKeys != nil {
		return s.apiKeys.DeleteApiKey(ctx, id)
	}
	return fmt.Errorf("api keys store not configured")
}

func (s *Store) GetApiKeyByID(ctx context.Context, id int64) (*ApiKey, error) {
	if s.apiKeys != nil {
		return s.apiKeys.GetApiKeyByID(ctx, id)
	}
	return nil, fmt.Errorf("api keys store not configured")
}

// Model wrappers

func (s *Store) CreateModel(ctx context.Context, m *model.Model) error {
	if s.models != nil {
		if m.IsDefault {
			models, err := s.models.ListModels(ctx)
			if err == nil {
				for _, other := range models {
					if other.Channel == m.Channel && other.IsDefault {
						other.IsDefault = false
						s.models.UpdateModel(ctx, other)
					}
				}
			}
		}
		return s.models.CreateModel(ctx, m)
	}
	return fmt.Errorf("models store not configured")
}

func (s *Store) UpdateModel(ctx context.Context, m *model.Model) error {
	if s.models != nil {
		if m.IsDefault {
			models, err := s.models.ListModels(ctx)
			if err == nil {
				for _, other := range models {
					if other.Channel == m.Channel && other.ID != m.ID && other.IsDefault {
						other.IsDefault = false
						s.models.UpdateModel(ctx, other)
					}
				}
			}
		}
		return s.models.UpdateModel(ctx, m)
	}
	return fmt.Errorf("models store not configured")
}

func (s *Store) DeleteModel(ctx context.Context, id string) error {
	if s.models != nil {
		return s.models.DeleteModel(ctx, id)
	}
	return fmt.Errorf("models store not configured")
}

func (s *Store) GetModel(ctx context.Context, id string) (*model.Model, error) {
	if s.models != nil {
		return s.models.GetModel(ctx, id)
	}
	return nil, fmt.Errorf("models store not configured")
}

func (s *Store) GetModelByModelID(ctx context.Context, modelID string) (*model.Model, error) {
	if s.models != nil {
		models, err := s.models.ListModels(ctx)
		if err != nil {
			return nil, err
		}
		for _, m := range models {
			if m.ModelID == modelID {
				return m, nil
			}
		}
		return nil, fmt.Errorf("model not found")
	}
	return nil, fmt.Errorf("models store not configured")
}

func (s *Store) ListModels(ctx context.Context) ([]*model.Model, error) {
	if s.models != nil {
		return s.models.ListModels(ctx)
	}
	return nil, fmt.Errorf("models store not configured")
}
