package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ===== 1. テスト用ヘルパー =====
func writeConfig(t *testing.T, listen string) string {
	t.Helper()
	text := `gateway:
  listen_address: "` + listen + `"
  listen_port: 8787
bots:
  general: {display_name: "星燈", watching: "サーバーを見守り中", application_id: "app-general", token_env: "TEST_GENERAL_TOKEN", intents: 33281}
  read_aloud_a: {display_name: "言ノ葉", watching: "言葉を紡ぎ中", application_id: "app-a", token_env: "TEST_A_TOKEN", intents: 33409}
  read_aloud_b: {display_name: "紡言", watching: "声をお届け中", application_id: "app-b", token_env: "TEST_B_TOKEN", intents: 33409}
workers:
  general: {worker_id: "general", role: "general", bot_id: "general", secret_env: "TEST_GENERAL_SECRET"}
  read_aloud_a: {worker_id: "read_aloud_a", role: "read-aloud", bot_id: "read_aloud_a", secret_env: "TEST_A_SECRET"}
  read_aloud_b: {worker_id: "read_aloud_b", role: "read-aloud", bot_id: "read_aloud_b", secret_env: "TEST_B_SECRET"}
database: {path: "` + filepath.ToSlash(filepath.Join(t.TempDir(), "db.sqlite")) + `"}
`
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func setSecrets(t *testing.T) {
	t.Helper()
	for i, name := range []string{"TEST_GENERAL_TOKEN", "TEST_A_TOKEN", "TEST_B_TOKEN"} {
		t.Setenv(name, "discord-token-placeholder-"+string(rune('a'+i)))
	}
	for i, name := range []string{"TEST_GENERAL_SECRET", "TEST_A_SECRET", "TEST_B_SECRET"} {
		t.Setenv(name, strings.Repeat(string(rune('a'+i)), 64))
	}
}

// ===== 2. 設定読み込みの検証テスト =====
func TestLoadRequiresLoopbackAndThreeWorkers(t *testing.T) {
	setSecrets(t)
	cfg, err := Load(writeConfig(t, "127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Bots) != 3 || len(cfg.Workers) != 3 {
		t.Fatalf("unexpected bot/worker count: %d/%d", len(cfg.Bots), len(cfg.Workers))
	}
	if cfg.Gateway.EventPayloadRetentionHours != 24 {
		t.Fatalf("default event retention=%d", cfg.Gateway.EventPayloadRetentionHours)
	}
}

func TestLoadRejectsPublicBindAddress(t *testing.T) {
	setSecrets(t)
	if _, err := Load(writeConfig(t, "0.0.0.0")); err == nil {
		t.Fatal("expected non-loopback bind to be rejected")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	setSecrets(t)
	path := writeConfig(t, "127.0.0.1")
	config, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	config = append(config, []byte("unexpected_setting: true\n")...)
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unknown config field to be rejected")
	}
}

func TestLoadRejectsUnsupportedLogFormat(t *testing.T) {
	setSecrets(t)
	path := writeConfig(t, "127.0.0.1")
	config, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	config = append(config, []byte("logging:\n  format: xml\n")...)
	if err := os.WriteFile(path, config, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected unsupported logging format to be rejected")
	}
}
