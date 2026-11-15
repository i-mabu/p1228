package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ===== 1. 型定義 =====
type Config struct {
	Gateway  GatewayConfig           `yaml:"gateway"`
	Bots     map[string]BotConfig    `yaml:"bots"`
	Workers  map[string]WorkerConfig `yaml:"workers"`
	Database DatabaseConfig          `yaml:"database"`
	Logging  LoggingConfig           `yaml:"logging"`
}

type GatewayConfig struct {
	ListenAddress              string `yaml:"listen_address"`
	ListenPort                 int    `yaml:"listen_port"`
	EventLeaseSeconds          int    `yaml:"event_lease_seconds"`
	MaxEventAttempts           int    `yaml:"max_event_attempts"`
	InteractionTTLSeconds      int    `yaml:"interaction_ttl_seconds"`
	EventPayloadRetentionHours int    `yaml:"event_payload_retention_hours"`
}

type BotConfig struct {
	DisplayName   string `yaml:"display_name"`
	Watching      string `yaml:"watching"`
	ApplicationID string `yaml:"application_id"`
	TokenEnv      string `yaml:"token_env"`
	Intents       int64  `yaml:"intents"`
	Token         string `yaml:"-"`
}

type WorkerConfig struct {
	WorkerID  string `yaml:"worker_id"`
	Role      string `yaml:"role"`
	BotID     string `yaml:"bot_id"`
	SecretEnv string `yaml:"secret_env"`
	Secret    string `yaml:"-"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type LoggingConfig struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// ===== 2. 設定の読み込みと検証 =====
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	decoder.KnownFields(true)
	if err := decoder.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.Gateway.ListenAddress == "" {
		c.Gateway.ListenAddress = "127.0.0.1"
	}
	if c.Gateway.ListenPort == 0 {
		c.Gateway.ListenPort = 8787
	}
	if c.Gateway.EventLeaseSeconds == 0 {
		c.Gateway.EventLeaseSeconds = 30
	}
	if c.Gateway.MaxEventAttempts == 0 {
		c.Gateway.MaxEventAttempts = 8
	}
	if c.Gateway.InteractionTTLSeconds == 0 {
		c.Gateway.InteractionTTLSeconds = 900
	}
	if c.Gateway.EventPayloadRetentionHours == 0 {
		c.Gateway.EventPayloadRetentionHours = 24
	}
	if ip := net.ParseIP(c.Gateway.ListenAddress); ip == nil || !ip.IsLoopback() {
		return nil, errors.New("gateway.listen_address must be a loopback address (127.0.0.1 or ::1)")
	}
	if c.Gateway.ListenPort < 1 || c.Gateway.ListenPort > 65535 {
		return nil, errors.New("gateway.listen_port must be between 1 and 65535")
	}
	if c.Gateway.EventLeaseSeconds < 5 || c.Gateway.MaxEventAttempts < 1 {
		return nil, errors.New("event_lease_seconds must be >= 5 and max_event_attempts must be >= 1")
	}
	if c.Gateway.InteractionTTLSeconds < 1 || c.Gateway.InteractionTTLSeconds > 900 {
		return nil, errors.New("interaction_ttl_seconds must be between 1 and Discord's 900 second token lifetime")
	}
	if c.Gateway.EventPayloadRetentionHours < 1 {
		return nil, errors.New("event_payload_retention_hours must be at least 1")
	}
	if c.Database.Path == "" {
		c.Database.Path = "./data/database/p1228.db"
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}
	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "warning", "error":
	default:
		return nil, fmt.Errorf("logging.level must be debug, info, warn, or error")
	}
	c.Logging.Level = strings.ToLower(c.Logging.Level)
	if c.Logging.Level == "warning" {
		c.Logging.Level = "warn"
	}
	c.Logging.Format = strings.ToLower(c.Logging.Format)
	if c.Logging.Format != "json" && c.Logging.Format != "text" {
		return nil, errors.New("logging.format must be json or text")
	}
	seenTokens := map[string]string{}
	seenApps := map[string]string{}
	for id, bot := range c.Bots {
		bot.ApplicationID = os.ExpandEnv(bot.ApplicationID)
		if strings.TrimSpace(bot.DisplayName) == "" || strings.TrimSpace(bot.Watching) == "" || bot.TokenEnv == "" {
			return nil, fmt.Errorf("bot %s requires display_name, watching, and token_env", id)
		}
		if len([]rune(bot.Watching)) > 128 {
			return nil, fmt.Errorf("bots.%s.watching must be at most 128 characters", id)
		}
		bot.Token = os.Getenv(bot.TokenEnv)
		if strings.TrimSpace(bot.Token) == "" || strings.HasPrefix(strings.ToUpper(strings.TrimSpace(bot.Token)), "REPLACE_ME") {
			return nil, fmt.Errorf("environment variable %s must contain a real BOT token for %s", bot.TokenEnv, id)
		}
		if previous, ok := seenTokens[bot.Token]; ok {
			return nil, fmt.Errorf("bots %s and %s must not share a BOT token", previous, id)
		}
		seenTokens[bot.Token] = id
		if strings.TrimSpace(bot.ApplicationID) == "" || strings.HasPrefix(strings.ToUpper(strings.TrimSpace(bot.ApplicationID)), "REPLACE") {
			return nil, fmt.Errorf("bots.%s.application_id must be set to the real Discord Application ID", id)
		}
		if previous, ok := seenApps[bot.ApplicationID]; ok {
			return nil, fmt.Errorf("bots %s and %s must not share an Application ID", previous, id)
		}
		seenApps[bot.ApplicationID] = id
		c.Bots[id] = bot
	}
	if len(c.Bots) != 3 {
		return nil, fmt.Errorf("exactly 3 bots are required (general, read_aloud_a, read_aloud_b); got %d", len(c.Bots))
	}
	for _, id := range []string{"general", "read_aloud_a", "read_aloud_b"} {
		bot, ok := c.Bots[id]
		if !ok {
			return nil, fmt.Errorf("required bot definition %s is missing", id)
		}
		if strings.TrimSpace(bot.ApplicationID) == "" {
			return nil, fmt.Errorf("bots.%s.application_id is required", id)
		}
	}
	seenSecrets := map[string]string{}
	for id, w := range c.Workers {
		if w.WorkerID == "" || w.BotID == "" || w.SecretEnv == "" {
			return nil, fmt.Errorf("worker %s requires worker_id, bot_id and secret_env", id)
		}
		if _, ok := c.Bots[w.BotID]; !ok {
			return nil, fmt.Errorf("worker %s references unknown bot %s", id, w.BotID)
		}
		w.Secret = os.Getenv(w.SecretEnv)
		if strings.TrimSpace(w.Secret) == "" {
			return nil, fmt.Errorf("environment variable %s is required for worker %s", w.SecretEnv, id)
		}
		if len(w.Secret) < 32 || strings.HasPrefix(strings.ToUpper(strings.TrimSpace(w.Secret)), "REPLACE_ME") {
			return nil, fmt.Errorf("worker secret %s must be a real secret of at least 32 characters", w.SecretEnv)
		}
		if previous, ok := seenSecrets[w.Secret]; ok {
			return nil, fmt.Errorf("workers %s and %s must use distinct secrets", previous, id)
		}
		seenSecrets[w.Secret] = id
		c.Workers[id] = w
	}
	if len(c.Workers) != 3 {
		return nil, errors.New("exactly 3 worker definitions are required")
	}
	for id, expectedRole := range map[string]string{"general": "general", "read_aloud_a": "read-aloud", "read_aloud_b": "read-aloud"} {
		w, ok := c.Workers[id]
		if !ok {
			return nil, fmt.Errorf("required worker definition %s is missing", id)
		}
		if w.WorkerID != id || w.BotID != id || w.Role != expectedRole {
			return nil, fmt.Errorf("worker %s must use worker_id/bot_id=%s and role=%s", id, id, expectedRole)
		}
	}
	return &c, nil
}
