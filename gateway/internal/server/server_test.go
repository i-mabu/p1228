package server

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"p1228/gateway/internal/config"
)

// ===== 1. テスト用ヘルパー =====
// Built by repetition so the fixture stays obviously fake.
var testWorkerSecret = strings.Repeat("test-secret-", 3)

func testServer() *Server {
	worker := config.WorkerConfig{WorkerID: "general", Role: "general", BotID: "general", Secret: testWorkerSecret}
	return &Server{workers: map[string]config.WorkerConfig{"general": worker}}
}

func requestWithAuth(workerID, authorization string) *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "/internal/v1/worker/heartbeat", nil)
	if workerID != "" {
		r.Header.Set("X-P1228-Worker-ID", workerID)
	}
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	return r
}

// ===== 2. 認証とユーティリティの検証テスト =====
func TestAuthenticateAcceptsExactSecret(t *testing.T) {
	s := testServer()
	if _, ok := s.authenticate(requestWithAuth("general", "Bearer "+testWorkerSecret)); !ok {
		t.Fatal("valid worker credentials were rejected")
	}
}

func TestAuthenticateRejectsWrongOrMalformedCredentials(t *testing.T) {
	s := testServer()
	cases := map[string]*http.Request{
		"wrong secret":    requestWithAuth("general", "Bearer wrong-value"),
		"short secret":    requestWithAuth("general", "Bearer x"),
		"missing bearer":  requestWithAuth("general", testWorkerSecret),
		"empty bearer":    requestWithAuth("general", "Bearer "),
		"unknown worker":  requestWithAuth("nobody", "Bearer "+testWorkerSecret),
		"missing headers": requestWithAuth("", ""),
	}
	for name, req := range cases {
		if _, ok := s.authenticate(req); ok {
			t.Errorf("%s: credentials were accepted", name)
		}
	}
}

func TestTruncateKeepsValidUTF8(t *testing.T) {
	value := strings.Repeat("あ", 500) // 1500 bytes
	got := truncate(value, 1000)
	if len(got) > 1000 {
		t.Fatalf("truncated length=%d bytes, want at most 1000", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncated value is not valid UTF-8: %q", got)
	}
	if short := truncate("short", 1000); short != "short" {
		t.Fatalf("short value changed: %q", short)
	}
}
