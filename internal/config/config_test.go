package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	v, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Unmarshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:8787" {
		t.Errorf("ListenAddr = %q, want loopback default", cfg.ListenAddr)
	}
	if cfg.VectorBackend != "sqlite-vec" {
		t.Errorf("VectorBackend = %q, want sqlite-vec", cfg.VectorBackend)
	}
	if cfg.LLM.ChatModel != "local-chat" {
		t.Errorf("LLM.ChatModel = %q, want local-first default local-chat", cfg.LLM.ChatModel)
	}
	if cfg.LLM.EmbedModel != "local-embed" {
		t.Errorf("LLM.EmbedModel = %q, want local-first default local-embed", cfg.LLM.EmbedModel)
	}
	if cfg.LLM.Timeout != 60*time.Second {
		t.Errorf("LLM.Timeout = %v, want 60s", cfg.LLM.Timeout)
	}
	if !cfg.Journal.DigestEnabled {
		t.Error("Journal.DigestEnabled should default true")
	}
	if cfg.Journal.DigestPrompt != DefaultDigestPrompt {
		t.Error("Journal.DigestPrompt should default to DefaultDigestPrompt")
	}
	// Device sync (ADR-0018) MUST default off: absent config means no sync
	// listener and the loopback-only posture unchanged (SPEC-0011).
	if cfg.DeviceSync.Enabled {
		t.Error("DeviceSync.Enabled should default false (strictly opt-in)")
	}
	if cfg.DeviceSync.ListenAddr != ":8788" {
		t.Errorf("DeviceSync.ListenAddr = %q, want :8788 (distinct from web UI)", cfg.DeviceSync.ListenAddr)
	}
	if cfg.DeviceSync.PollInterval != 15*time.Minute {
		t.Errorf("DeviceSync.PollInterval = %v, want 15m", cfg.DeviceSync.PollInterval)
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("MSGBROWSE_LISTEN_ADDR", "127.0.0.1:9999")
	t.Setenv("MSGBROWSE_LLM_API_KEY", "secret-from-env")
	t.Setenv("MSGBROWSE_LOG_LEVEL", "debug")

	v, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Unmarshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "127.0.0.1:9999" {
		t.Errorf("ListenAddr = %q, want env override", cfg.ListenAddr)
	}
	if cfg.LLM.APIKey != "secret-from-env" {
		t.Errorf("LLM.APIKey = %q, want env value", cfg.LLM.APIKey)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"defaults ok", func(*Config) {}, false},
		{"bad vector backend", func(c *Config) { c.VectorBackend = "pinecone" }, true},
		{"bad log level", func(c *Config) { c.LogLevel = "trace" }, true},
		{"empty data dir", func(c *Config) { c.DataDir = "" }, true},
		{"device sync enabled with defaults ok", func(c *Config) { c.DeviceSync.Enabled = true }, false},
		{"device sync empty listen addr", func(c *Config) {
			c.DeviceSync.Enabled = true
			c.DeviceSync.ListenAddr = ""
		}, true},
		{"device sync colliding with web ui addr", func(c *Config) {
			c.DeviceSync.Enabled = true
			c.DeviceSync.ListenAddr = c.ListenAddr
		}, true},
		{"device sync non-positive poll interval", func(c *Config) {
			c.DeviceSync.Enabled = true
			c.DeviceSync.PollInterval = 0
		}, true},
		{"device sync disabled skips its checks", func(c *Config) {
			c.DeviceSync.Enabled = false
			c.DeviceSync.ListenAddr = ""
			c.DeviceSync.PollInterval = 0
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, _ := Load("")
			cfg, _ := Unmarshal(v)
			tt.mutate(cfg)
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
