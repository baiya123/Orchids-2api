package orchids

import (
	"testing"

	"orchids-api/internal/config"
	"orchids-api/internal/store"
)

func TestNewFromAccount_CopiesOrchidsConfig(t *testing.T) {
	t.Parallel()

	base := &config.Config{
		OrchidsImpl:               "aiclient",
		UpstreamMode:              "sse",
		OrchidsAPIBaseURL:         "https://example.com",
		OrchidsWSURL:              "wss://example.com/ws",
		OrchidsAPIVersion:         "2",
		OrchidsMaxToolResults:     10,
		OrchidsMaxHistoryMessages: 20,
		SuppressThinking:          true,
	}
	acc := &store.Account{
		SessionID: "sid",
		Email:     "user@example.com",
	}

	client := NewFromAccount(acc, base)
	if client == nil || client.config == nil {
		t.Fatalf("expected client/config to be initialized")
	}
	if client.config.OrchidsImpl != "aiclient" {
		t.Fatalf("expected orchids_impl to be copied from base config")
	}
	if !client.config.SuppressThinking {
		t.Fatalf("expected suppress_thinking to be copied from base config")
	}
}
