package store

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"orchids-api/internal/model"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

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
	db       *sql.DB
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
	CreateAccount(acc *Account) error
	UpdateAccount(acc *Account) error
	DeleteAccount(id int64) error
	GetAccount(id int64) (*Account, error)
	ListAccounts() ([]*Account, error)
	GetEnabledAccounts() ([]*Account, error)
	IncrementRequestCount(id int64) error
}

type settingsStore interface {
	GetSetting(key string) (string, error)
	SetSetting(key, value string) error
}

type apiKeyStore interface {
	CreateApiKey(key *ApiKey) error
	ListApiKeys() ([]*ApiKey, error)
	GetApiKeyByHash(hash string) (*ApiKey, error)
	UpdateApiKeyEnabled(id int64, enabled bool) error
	UpdateApiKeyLastUsed(id int64) error
	DeleteApiKey(id int64) error
	GetApiKeyByID(id int64) (*ApiKey, error)
}

type modelStore interface {
	CreateModel(m *model.Model) error
	UpdateModel(m *model.Model) error
	DeleteModel(id string) error
	GetModel(id string) (*model.Model, error)
	ListModels() ([]*model.Model, error)
}

type closeableStore interface {
	Close() error
}

func New(dbPath string, opts Options) (*Store, error) {
	mode := strings.ToLower(strings.TrimSpace(opts.StoreMode))
	if mode == "" {
		mode = "redis"
	}

	store := &Store{}
	if mode == "redis" {
		redisStore, err := newRedisStore(opts.RedisAddr, opts.RedisPassword, opts.RedisDB, opts.RedisPrefix)
		if err != nil {
			return nil, fmt.Errorf("failed to init redis store: %w", err)
		}
		store.accounts = redisStore
		store.settings = redisStore
		store.apiKeys = redisStore
		store.models = redisStore
		if err := store.seedModels(); err != nil {
			log.Printf("Warning: failed to seed models in redis: %v", err)
		}
		return store, nil
	}

	if dbPath == "" {
		return nil, errors.New("sqlite db path is required when store_mode=sqlite")
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(time.Hour)

	if err := applySQLitePragmas(db); err != nil {
		return nil, fmt.Errorf("failed to apply sqlite pragmas: %w", err)
	}

	store.db = db
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return store, nil
}

func (s *Store) migrate() error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			session_id TEXT NOT NULL,
			client_cookie TEXT NOT NULL,
			client_uat TEXT NOT NULL,
			project_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			agent_mode TEXT DEFAULT 'claude-opus-4.5',
			email TEXT NOT NULL,
			weight INTEGER DEFAULT 1,
			enabled INTEGER DEFAULT 1,
			token TEXT DEFAULT '',
			subscription TEXT DEFAULT 'free',
			usage_current REAL DEFAULT 0,
			usage_total REAL DEFAULT 550,
			reset_date TEXT DEFAULT '-',
			request_count INTEGER DEFAULT 0,
			last_used_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key TEXT UNIQUE NOT NULL,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS api_keys (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			key_hash TEXT NOT NULL UNIQUE,
			key_full TEXT NOT NULL DEFAULT '',
			key_prefix TEXT NOT NULL DEFAULT 'sk-',
			key_suffix TEXT NOT NULL,
			enabled INTEGER DEFAULT 1,
			last_used_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS models (
			id TEXT PRIMARY KEY,
			channel TEXT NOT NULL,
			model_id TEXT NOT NULL,
			name TEXT NOT NULL,
			status INTEGER DEFAULT 1,
			is_default INTEGER DEFAULT 0,
			sort_order INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_enabled ON accounts(enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_accounts_weight ON accounts(weight) WHERE enabled=1`,
		`CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys(key_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_api_keys_enabled ON api_keys(enabled)`,
		`CREATE INDEX IF NOT EXISTS idx_models_channel ON models(channel, status)`,
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, q := range queries {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}

	tx.Exec(`ALTER TABLE api_keys ADD COLUMN key_full TEXT NOT NULL DEFAULT ''`)
	tx.Exec(`ALTER TABLE accounts ADD COLUMN token TEXT DEFAULT ''`)
	tx.Exec(`ALTER TABLE accounts ADD COLUMN subscription TEXT DEFAULT 'free'`)
	tx.Exec(`ALTER TABLE accounts ADD COLUMN usage_current REAL DEFAULT 0`)
	tx.Exec(`ALTER TABLE accounts ADD COLUMN usage_total REAL DEFAULT 550`)
	tx.Exec(`ALTER TABLE accounts ADD COLUMN reset_date TEXT DEFAULT '-'`)

	if err := tx.Commit(); err != nil {
		return err
	}

	return s.seedModels()
}

func (s *Store) seedModels() error {
	var count int
	if s.models != nil {
		models, err := s.models.ListModels()
		if err == nil {
			count = len(models)
		}
	} else {
		s.db.QueryRow("SELECT COUNT(*) FROM models").Scan(&count)
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
		if err := s.CreateModel(&m); err != nil {
			log.Printf("Failed to seed model %s: %v", m.ModelID, err)
		}
	}
	return nil
}

func applySQLitePragmas(db *sql.DB) error {
	queries := []string{
		"PRAGMA journal_mode=WAL;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA busy_timeout=5000;",
		"PRAGMA foreign_keys=ON;",
	}
	for _, q := range queries {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error {
	if s.accounts != nil {
		if closer, ok := s.accounts.(closeableStore); ok {
			_ = closer.Close()
		}
	}
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) CreateAccount(acc *Account) error {
	if s.accounts != nil {
		return s.accounts.CreateAccount(acc)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		INSERT INTO accounts (name, session_id, client_cookie, client_uat, project_id, user_id, agent_mode, email, weight, enabled, token, subscription, usage_current, usage_total, reset_date)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, acc.Name, acc.SessionID, acc.ClientCookie, acc.ClientUat, acc.ProjectID, acc.UserID, acc.AgentMode, acc.Email, acc.Weight, acc.Enabled, acc.Token, acc.Subscription, acc.UsageCurrent, acc.UsageTotal, acc.ResetDate)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	acc.ID = id
	return nil
}

func (s *Store) UpdateAccount(acc *Account) error {
	if s.accounts != nil {
		return s.accounts.UpdateAccount(acc)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE accounts SET
			name = ?, session_id = ?, client_cookie = ?, client_uat = ?,
			project_id = ?, user_id = ?, agent_mode = ?, email = ?,
			weight = ?, enabled = ?, token = ?, subscription = ?,
			usage_current = ?, usage_total = ?, reset_date = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, acc.Name, acc.SessionID, acc.ClientCookie, acc.ClientUat, acc.ProjectID, acc.UserID, acc.AgentMode, acc.Email, acc.Weight, acc.Enabled, acc.Token, acc.Subscription, acc.UsageCurrent, acc.UsageTotal, acc.ResetDate, acc.ID)
	return err
}

func (s *Store) DeleteAccount(id int64) error {
	if s.accounts != nil {
		return s.accounts.DeleteAccount(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM accounts WHERE id = ?", id)
	return err
}

func (s *Store) GetAccount(id int64) (*Account, error) {
	if s.accounts != nil {
		return s.accounts.GetAccount(id)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	acc := &Account{}
	var lastUsedAt sql.NullTime
	err := s.db.QueryRow(`
		SELECT id, name, session_id, client_cookie, client_uat, project_id, user_id,
			   agent_mode, email, weight, enabled, token, subscription, usage_current, usage_total, reset_date,
			   request_count, last_used_at, created_at, updated_at
		FROM accounts WHERE id = ?
	`, id).Scan(&acc.ID, &acc.Name, &acc.SessionID, &acc.ClientCookie, &acc.ClientUat,
		&acc.ProjectID, &acc.UserID, &acc.AgentMode, &acc.Email, &acc.Weight,
		&acc.Enabled, &acc.Token, &acc.Subscription, &acc.UsageCurrent, &acc.UsageTotal, &acc.ResetDate,
		&acc.RequestCount, &lastUsedAt, &acc.CreatedAt, &acc.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if lastUsedAt.Valid {
		acc.LastUsedAt = lastUsedAt.Time
	}
	return acc, nil
}

func (s *Store) ListAccounts() ([]*Account, error) {
	if s.accounts != nil {
		return s.accounts.ListAccounts()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, name, session_id, client_cookie, client_uat, project_id, user_id,
			   agent_mode, email, weight, enabled, token, subscription, usage_current, usage_total, reset_date,
			   request_count, last_used_at, created_at, updated_at
		FROM accounts ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var accounts []*Account
	for rows.Next() {
		acc := &Account{}
		var lastUsedAt sql.NullTime
		err := rows.Scan(&acc.ID, &acc.Name, &acc.SessionID, &acc.ClientCookie, &acc.ClientUat,
			&acc.ProjectID, &acc.UserID, &acc.AgentMode, &acc.Email, &acc.Weight,
			&acc.Enabled, &acc.Token, &acc.Subscription, &acc.UsageCurrent, &acc.UsageTotal, &acc.ResetDate,
			&acc.RequestCount, &lastUsedAt, &acc.CreatedAt, &acc.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if lastUsedAt.Valid {
			acc.LastUsedAt = lastUsedAt.Time
		}
		accounts = append(accounts, acc)
	}
	return accounts, nil
}

func (s *Store) GetEnabledAccounts() ([]*Account, error) {
	if s.accounts != nil {
		return s.accounts.GetEnabledAccounts()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, name, session_id, client_cookie, client_uat, project_id, user_id,
			   agent_mode, email, weight, enabled, token, subscription, usage_current, usage_total, reset_date,
			   request_count, last_used_at, created_at, updated_at
		FROM accounts WHERE enabled = 1 ORDER BY weight DESC, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := make([]*Account, 0, 10)
	for rows.Next() {
		acc := &Account{}
		var lastUsedAt sql.NullTime
		err := rows.Scan(&acc.ID, &acc.Name, &acc.SessionID, &acc.ClientCookie, &acc.ClientUat,
			&acc.ProjectID, &acc.UserID, &acc.AgentMode, &acc.Email, &acc.Weight,
			&acc.Enabled, &acc.Token, &acc.Subscription, &acc.UsageCurrent, &acc.UsageTotal, &acc.ResetDate,
			&acc.RequestCount, &lastUsedAt, &acc.CreatedAt, &acc.UpdatedAt)
		if err != nil {
			return nil, err
		}
		if lastUsedAt.Valid {
			acc.LastUsedAt = lastUsedAt.Time
		}
		accounts = append(accounts, acc)
	}
	return accounts, rows.Err()
}

func (s *Store) IncrementRequestCount(id int64) error {
	if s.accounts != nil {
		return s.accounts.IncrementRequestCount(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE accounts SET request_count = request_count + 1, last_used_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, id)
	return err
}

func (s *Store) GetSetting(key string) (string, error) {
	if s.settings != nil {
		return s.settings.GetSetting(key)
	}
	if s.db == nil {
		return "", errors.New("settings store not configured")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	var value string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (s *Store) SetSetting(key, value string) error {
	if s.settings != nil {
		return s.settings.SetSetting(key, value)
	}
	if s.db == nil {
		return errors.New("settings store not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

func (s *Store) CreateApiKey(key *ApiKey) error {
	if s.apiKeys != nil {
		return s.apiKeys.CreateApiKey(key)
	}
	if s.db == nil {
		return errors.New("api keys store not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		INSERT INTO api_keys (name, key_hash, key_full, key_prefix, key_suffix, enabled)
		VALUES (?, ?, ?, ?, ?, ?)
	`, key.Name, key.KeyHash, key.KeyFull, key.KeyPrefix, key.KeySuffix, key.Enabled)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	key.ID = id

	var createdAt time.Time
	var lastUsedAt sql.NullTime
	if err := s.db.QueryRow(`
		SELECT enabled, last_used_at, created_at
		FROM api_keys WHERE id = ?
	`, id).Scan(&key.Enabled, &lastUsedAt, &createdAt); err != nil {
		return err
	}
	if lastUsedAt.Valid {
		t := lastUsedAt.Time
		key.LastUsedAt = &t
	} else {
		key.LastUsedAt = nil
	}
	key.CreatedAt = createdAt

	return nil
}

func (s *Store) ListApiKeys() ([]*ApiKey, error) {
	if s.apiKeys != nil {
		return s.apiKeys.ListApiKeys()
	}
	if s.db == nil {
		return nil, errors.New("api keys store not configured")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, name, key_full, key_prefix, key_suffix, enabled, last_used_at, created_at
		FROM api_keys ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []*ApiKey
	for rows.Next() {
		key := &ApiKey{}
		var lastUsedAt sql.NullTime
		if err := rows.Scan(&key.ID, &key.Name, &key.KeyFull, &key.KeyPrefix, &key.KeySuffix, &key.Enabled, &lastUsedAt, &key.CreatedAt); err != nil {
			return nil, err
		}
		if lastUsedAt.Valid {
			t := lastUsedAt.Time
			key.LastUsedAt = &t
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return keys, nil
}

func (s *Store) GetApiKeyByHash(hash string) (*ApiKey, error) {
	if s.apiKeys != nil {
		return s.apiKeys.GetApiKeyByHash(hash)
	}
	if s.db == nil {
		return nil, errors.New("api keys store not configured")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := &ApiKey{}
	var lastUsedAt sql.NullTime
	err := s.db.QueryRow(`
		SELECT id, name, key_hash, key_prefix, key_suffix, enabled, last_used_at, created_at
		FROM api_keys WHERE key_hash = ?
	`, hash).Scan(&key.ID, &key.Name, &key.KeyHash, &key.KeyPrefix, &key.KeySuffix, &key.Enabled, &lastUsedAt, &key.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if lastUsedAt.Valid {
		t := lastUsedAt.Time
		key.LastUsedAt = &t
	}
	return key, nil
}

func (s *Store) UpdateApiKeyEnabled(id int64, enabled bool) error {
	if s.apiKeys != nil {
		return s.apiKeys.UpdateApiKeyEnabled(id, enabled)
	}
	if s.db == nil {
		return errors.New("api keys store not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec(`
		UPDATE api_keys SET enabled = ?
		WHERE id = ?
	`, enabled, id)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateApiKeyLastUsed(id int64) error {
	if s.apiKeys != nil {
		return s.apiKeys.UpdateApiKeyLastUsed(id)
	}
	if s.db == nil {
		return errors.New("api keys store not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, id)
	return err
}

func (s *Store) DeleteApiKey(id int64) error {
	if s.apiKeys != nil {
		return s.apiKeys.DeleteApiKey(id)
	}
	if s.db == nil {
		return errors.New("api keys store not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.db.Exec("DELETE FROM api_keys WHERE id = ?", id)
	if err != nil {
		return err
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetApiKeyByID(id int64) (*ApiKey, error) {
	if s.apiKeys != nil {
		return s.apiKeys.GetApiKeyByID(id)
	}
	if s.db == nil {
		return nil, errors.New("api keys store not configured")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := &ApiKey{}
	var lastUsedAt sql.NullTime
	err := s.db.QueryRow(`
		SELECT id, name, key_prefix, key_suffix, enabled, last_used_at, created_at
		FROM api_keys WHERE id = ?
	`, id).Scan(&key.ID, &key.Name, &key.KeyPrefix, &key.KeySuffix, &key.Enabled, &lastUsedAt, &key.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if lastUsedAt.Valid {
		t := lastUsedAt.Time
		key.LastUsedAt = &t
	}
	return key, nil
}

// Model wrappers

func (s *Store) CreateModel(m *model.Model) error {
	if s.models != nil {
		if m.IsDefault {
			models, err := s.models.ListModels()
			if err == nil {
				for _, other := range models {
					if other.Channel == m.Channel && other.IsDefault {
						other.IsDefault = false
						s.models.UpdateModel(other)
					}
				}
			}
		}
		return s.models.CreateModel(m)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// If ID is empty, we should generate one or let DB handled (but it's TEXT PRIMARY KEY)
	// Usually numeric IDs are used in the screenshot.
	if m.ID == "" {
		var maxID int
		s.db.QueryRow("SELECT COALESCE(MAX(CAST(id AS INTEGER)), 0) FROM models").Scan(&maxID)
		m.ID = fmt.Sprintf("%d", maxID+1)
	}

	if m.IsDefault {
		// Clear other defaults for same channel
		s.db.Exec("UPDATE models SET is_default = 0 WHERE channel = ?", m.Channel)
	}

	_, err := s.db.Exec(`
		INSERT INTO models (id, channel, model_id, name, status, is_default, sort_order)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, m.ID, m.Channel, m.ModelID, m.Name, m.Status, m.IsDefault, m.SortOrder)
	return err
}

func (s *Store) UpdateModel(m *model.Model) error {
	if s.models != nil {
		if m.IsDefault {
			models, err := s.models.ListModels()
			if err == nil {
				for _, other := range models {
					if other.Channel == m.Channel && other.ID != m.ID && other.IsDefault {
						other.IsDefault = false
						s.models.UpdateModel(other)
					}
				}
			}
		}
		return s.models.UpdateModel(m)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if m.IsDefault {
		// Clear other defaults for same channel
		s.db.Exec("UPDATE models SET is_default = 0 WHERE channel = ? AND id != ?", m.Channel, m.ID)
	}

	_, err := s.db.Exec(`
		UPDATE models SET
			channel = ?, model_id = ?, name = ?, status = ?, is_default = ?,
			sort_order = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, m.Channel, m.ModelID, m.Name, m.Status, m.IsDefault, m.SortOrder, m.ID)
	return err
}

func (s *Store) DeleteModel(id string) error {
	if s.models != nil {
		return s.models.DeleteModel(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec("DELETE FROM models WHERE id = ?", id)
	return err
}

func (s *Store) GetModel(id string) (*model.Model, error) {
	if s.models != nil {
		return s.models.GetModel(id)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	m := &model.Model{}
	err := s.db.QueryRow(`
		SELECT id, channel, model_id, name, status, is_default, sort_order
		FROM models WHERE id = ?
	`, id).Scan(&m.ID, &m.Channel, &m.ModelID, &m.Name, &m.Status, &m.IsDefault, &m.SortOrder)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Store) GetModelByModelID(modelID string) (*model.Model, error) {
	if s.models != nil {
		// For Redis, we do a simple scan of ListModels since the list is small
		models, err := s.models.ListModels()
		if err != nil {
			return nil, err
		}
		for _, m := range models {
			if m.ModelID == modelID {
				return m, nil
			}
		}
		return nil, sql.ErrNoRows
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	m := &model.Model{}
	// Prefer default models if multiple exist for same model_id
	err := s.db.QueryRow(`
		SELECT id, channel, model_id, name, status, is_default, sort_order
		FROM models WHERE model_id = ? ORDER BY is_default DESC LIMIT 1
	`, modelID).Scan(&m.ID, &m.Channel, &m.ModelID, &m.Name, &m.Status, &m.IsDefault, &m.SortOrder)
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (s *Store) ListModels() ([]*model.Model, error) {
	if s.models != nil {
		return s.models.ListModels()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, channel, model_id, name, status, is_default, sort_order
		FROM models ORDER BY sort_order ASC, name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var models []*model.Model
	for rows.Next() {
		m := &model.Model{}
		err := rows.Scan(&m.ID, &m.Channel, &m.ModelID, &m.Name, &m.Status, &m.IsDefault, &m.SortOrder)
		if err != nil {
			return nil, err
		}
		models = append(models, m)
	}
	return models, nil
}
