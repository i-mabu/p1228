package discord

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"log/slog"
	"p1228/gateway/internal/config"
)

func TestRoleOperationsRestrictedToGeneralBot(t *testing.T) {
	manager := NewManager(&config.Config{Bots: map[string]config.BotConfig{
		"read_aloud_a": {Token: "test-token"},
	}}, nil, slog.Default())
	operation := map[string]json.RawMessage{
		"operation": json.RawMessage(`"add_role"`),
		"guild_id":  json.RawMessage(`"guild-1"`),
		"user_id":   json.RawMessage(`"user-1"`),
		"role_id":   json.RawMessage(`"role-1"`),
	}
	_, _, err := manager.DiscordOperation(context.Background(), "read_aloud_a", operation)
	if err == nil || !strings.Contains(err.Error(), "only allowed for the general bot") {
		t.Fatalf("read-aloud role operation error=%v", err)
	}
}

func TestEditMessageRejectsContentOverDiscordLimit(t *testing.T) {
	manager := NewManager(&config.Config{Bots: map[string]config.BotConfig{
		"general": {Token: "test-token"},
	}}, nil, slog.Default())
	content, err := json.Marshal(strings.Repeat("日", 2001))
	if err != nil {
		t.Fatal(err)
	}
	operation := map[string]json.RawMessage{
		"operation":  json.RawMessage(`"edit_message"`),
		"channel_id": json.RawMessage(`"channel-1"`),
		"message_id": json.RawMessage(`"message-1"`),
		"content":    content,
	}

	_, _, err = manager.DiscordOperation(context.Background(), "general", operation)
	if err == nil {
		t.Fatal("expected content length validation error")
	}
}

func TestDiscordOperationRejectsWrongContentType(t *testing.T) {
	manager := NewManager(&config.Config{Bots: map[string]config.BotConfig{
		"general": {Token: "test-token"},
	}}, nil, slog.Default())
	operation := map[string]json.RawMessage{
		"operation":  json.RawMessage(`"edit_message"`),
		"channel_id": json.RawMessage(`"channel-1"`),
		"message_id": json.RawMessage(`"message-1"`),
		"content":    json.RawMessage(`42`),
	}

	_, _, err := manager.DiscordOperation(context.Background(), "general", operation)
	if err == nil || !strings.Contains(err.Error(), "content must be a string") {
		t.Fatalf("wrong content type error=%v", err)
	}
}

func TestEditMessageRequiresContentField(t *testing.T) {
	manager := NewManager(&config.Config{Bots: map[string]config.BotConfig{
		"general": {Token: "test-token"},
	}}, nil, slog.Default())
	operation := map[string]json.RawMessage{
		"operation":  json.RawMessage(`"edit_message"`),
		"channel_id": json.RawMessage(`"channel-1"`),
		"message_id": json.RawMessage(`"message-1"`),
	}

	_, _, err := manager.DiscordOperation(context.Background(), "general", operation)
	if err == nil || !strings.Contains(err.Error(), "content is required") {
		t.Fatalf("missing content error=%v", err)
	}
}

func TestSendMessageRejectsNonArrayComponents(t *testing.T) {
	manager := NewManager(&config.Config{Bots: map[string]config.BotConfig{
		"general": {Token: "test-token"},
	}}, nil, slog.Default())
	operation := map[string]json.RawMessage{
		"operation":  json.RawMessage(`"send_message"`),
		"channel_id": json.RawMessage(`"channel-1"`),
		"content":    json.RawMessage(`"hello"`),
		"components": json.RawMessage(`{"type":1}`),
	}

	_, _, err := manager.DiscordOperation(context.Background(), "general", operation)
	if err == nil || !strings.Contains(err.Error(), "components must be a JSON array") {
		t.Fatalf("wrong components type error=%v", err)
	}
}

func TestFeatureDispatchesAreScopedToGeneralBot(t *testing.T) {
	tests := []struct {
		botID string
		event string
		want  bool
	}{
		{botID: "general", event: "GUILD_MEMBER_ADD", want: true},
		{botID: "read_aloud_a", event: "GUILD_MEMBER_ADD", want: false},
		{botID: "general", event: "VOICE_STATE_UPDATE", want: true},
		{botID: "read_aloud_b", event: "VOICE_STATE_UPDATE", want: false},
		{botID: "read_aloud_a", event: "MESSAGE_CREATE", want: true},
		{botID: "general", event: "MESSAGE_DELETE_BULK", want: true},
	}
	for _, test := range tests {
		if got := shouldQueue(test.botID, test.event); got != test.want {
			t.Errorf("shouldQueue(%q, %q)=%v, want %v", test.botID, test.event, got, test.want)
		}
	}
}

func TestWorkerFailurePresenceUsesWatchingActivity(t *testing.T) {
	payload, err := json.Marshal(workerPresenceUpdate(config.BotConfig{Watching: "通常の視聴中"}, true))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Op int `json:"op"`
		D  struct {
			Activities []struct {
				Name string `json:"name"`
				Type int    `json:"type"`
			} `json:"activities"`
		} `json:"d"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Op != 3 || len(decoded.D.Activities) != 1 {
		t.Fatalf("unexpected failure presence payload: %s", payload)
	}
	if got := decoded.D.Activities[0]; got.Type != 3 || got.Name != "障害対応中：Worker応答なし" {
		t.Fatalf("unexpected failure activity: %+v", got)
	}
}

func TestWorkerRecoveryPresenceRestoresConfiguredActivity(t *testing.T) {
	payload := workerPresenceUpdate(config.BotConfig{Watching: "通常の視聴中"}, false)
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "通常の視聴中") || strings.Contains(string(data), "障害対応中") {
		t.Fatalf("recovery presence did not restore configured activity: %s", data)
	}
}

func TestPresenceUpdateUsesWatchingActivity(t *testing.T) {
	payload, err := json.Marshal(presenceUpdate(config.BotConfig{Watching: "サーバーを見守り中"}))
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Op int `json:"op"`
		D  struct {
			Status     string `json:"status"`
			Activities []struct {
				Name string `json:"name"`
				Type int    `json:"type"`
			} `json:"activities"`
		} `json:"d"`
	}
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Op != 3 || decoded.D.Status != "online" || len(decoded.D.Activities) != 1 {
		t.Fatalf("unexpected presence payload: %s", payload)
	}
	if activity := decoded.D.Activities[0]; activity.Type != 3 || activity.Name != "サーバーを見守り中" {
		t.Fatalf("unexpected activity: %+v", activity)
	}
}

func TestWorkerMissingDetectionWaitsForStartupGrace(t *testing.T) {
	startedAt := time.Now()
	if workerMissingAfterStartupGrace(startedAt, startedAt.Add(workerStartupGrace-time.Second)) {
		t.Fatal("Worker should not be marked missing during startup grace")
	}
	if !workerMissingAfterStartupGrace(startedAt, startedAt.Add(workerStartupGrace+time.Second)) {
		t.Fatal("Worker that never registered should be marked missing after startup grace")
	}
	if workerMissingAfterStartupGrace(time.Time{}, time.Now().Add(time.Hour)) {
		t.Fatal("zero startup time must not trigger a false outage")
	}
}
