package discord

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"p1228/gateway/internal/config"
)

// ===== 1. 型定義とストア生成 =====
type InteractionStore struct {
	mu        sync.Mutex
	refs      map[string]*interactionRef
	client    *http.Client
	lastSweep time.Time
}

type interactionRef struct {
	BotID, InteractionID, Token, ApplicationID string
	Expires                                    time.Time
	InitialSent                                bool
	InitialPending                             bool
	InitialUnknown                             bool
	AutoDeferred                               bool
	AutoResponseType                           int
}

func NewInteractionStore(_ map[string]config.BotConfig) *InteractionStore {
	return &InteractionStore{refs: map[string]*interactionRef{}, client: &http.Client{Timeout: 8 * time.Second}}
}

func (s *InteractionStore) Create(botID, interactionID, token, applicationID string, ttl time.Duration) string {
	ref := randomToken("ix_")
	now := time.Now()
	s.mu.Lock()
	// ===== Expired refs are also rejected lazily on use, so sweeping the whole =====
	// ===== map on every Create (O(n) per interaction) is wasteful. Sweep at =====
	// ===== most once a minute, or sooner if the map grows unexpectedly large. =====
	if len(s.refs) > 1024 || now.Sub(s.lastSweep) > time.Minute {
		s.lastSweep = now
		for key, value := range s.refs {
			if now.After(value.Expires) {
				delete(s.refs, key)
			}
		}
	}
	s.refs[ref] = &interactionRef{BotID: botID, InteractionID: interactionID, Token: token, ApplicationID: applicationID, Expires: now.Add(ttl)}
	s.mu.Unlock()
	return ref
}

// ===== 2. 応答と遅延処理 =====
// AutoDefer ===== claims the one-shot initial response only if the Worker has not replied. =====
// ===== Application commands and modal submits use type 5; components use type 6. =====
func (s *InteractionStore) AutoDefer(ctx context.Context, expectedBotID, ref string, responseType int) error {
	s.mu.Lock()
	current, ok := s.refs[ref]
	if !ok || current.BotID != expectedBotID || time.Now().After(current.Expires) || current.InitialSent || current.InitialPending || current.InitialUnknown {
		s.mu.Unlock()
		return errors.New("interaction was already acknowledged, pending, uncertain, expired, or not found")
	}
	current.InitialPending = true
	value := *current
	s.mu.Unlock()
	callback := map[string]any{"type": responseType}
	if responseType == 8 { // Autocomplete cannot be deferred; send an empty-choice timeout fallback.
		callback["data"] = map[string]any{"choices": []any{}}
	}
	body, _ := json.Marshal(callback)
	err := s.request(ctx, http.MethodPost, interactionCallbackPath(&value), body)
	s.mu.Lock()
	if current.InitialPending {
		current.InitialPending = false
		if err != nil {
			// ===== A transport failure may mean Discord accepted the callback but its response =====
			// ===== was lost. Never issue another initial callback or a follow-up in that state. =====
			current.InitialUnknown = true
		} else {
			current.InitialSent = true
			current.AutoDeferred = responseType == 5 || responseType == 6
			current.AutoResponseType = responseType
		}
	}
	s.mu.Unlock()
	return err
}

// ----- 2.1 アクション実行 -----
func (s *InteractionStore) Do(ctx context.Context, expectedBotID, ref, action string, body json.RawMessage) error {
	s.mu.Lock()
	current, ok := s.refs[ref]
	if !ok || current.BotID != expectedBotID || time.Now().After(current.Expires) {
		s.mu.Unlock()
		return errors.New("interaction reference not found, expired, or belongs to another bot")
	}
	if action == "initial" && current.InitialPending {
		s.mu.Unlock()
		return errors.New("interaction initial response is still pending")
	}
	if action == "initial" && current.InitialUnknown {
		s.mu.Unlock()
		return errors.New("interaction acknowledgement outcome is unknown; refusing duplicate response")
	}
	value := *current
	method, path := "", ""
	requestBody := []byte(body)
	reservedInitial := false
	consumeAutoDefer := false
	switch action {
	case "initial":
		if current.InitialSent {
			if current.AutoResponseType == 6 {
				// ===== A deferred component update already acknowledges the click. A repeated =====
				// ===== type-6 response is redundant; a late message response becomes a follow-up. =====
				var callback struct {
					Type int `json:"type"`
				}
				if json.Unmarshal(body, &callback) == nil && callback.Type == 6 {
					s.mu.Unlock()
					return nil
				}
				method = http.MethodPost
				path = followupPath(&value)
				requestBody = unwrapInitialCallback(body)
				consumeAutoDefer = true
			} else if current.AutoResponseType == 8 {
				// ===== The empty autocomplete fallback is already the final initial response. =====
				// ===== Autocomplete cannot be edited or deferred after type 8. =====
				current.AutoResponseType = 0
				s.mu.Unlock()
				return nil
			} else {
				if !current.AutoDeferred {
					s.mu.Unlock()
					return errors.New("interaction already acknowledged")
				}
				// ===== The Gateway already sent a defer after the safety deadline. Treat this late =====
				// ===== initial response as the edit of the deferred original response. =====
				consumeAutoDefer = true
				method = http.MethodPatch
				path = originalResponsePath(&value)
				requestBody = unwrapInitialCallback(body)
			}
		} else {
			current.InitialPending = true
			reservedInitial = true
			current.AutoResponseType = 0
			method = http.MethodPost
			path = interactionCallbackPath(&value)
		}
	case "edit-original":
		if !current.InitialSent {
			s.mu.Unlock()
			return errors.New("interaction has not been acknowledged")
		}
		method = http.MethodPatch
		path = originalResponsePath(&value)
	case "followups":
		if !current.InitialSent {
			s.mu.Unlock()
			return errors.New("interaction has not been acknowledged")
		}
		method = http.MethodPost
		path = followupPath(&value)
	default:
		s.mu.Unlock()
		return errors.New("unsupported interaction action")
	}
	s.mu.Unlock()
	err := s.request(ctx, method, path, requestBody)
	if reservedInitial || (consumeAutoDefer && err == nil) {
		s.mu.Lock()
		if reservedInitial {
			current.InitialPending = false
			if err != nil {
				current.InitialUnknown = true
			} else {
				current.InitialSent = true
			}
		}
		if consumeAutoDefer && err == nil {
			current.AutoDeferred = false
			current.AutoResponseType = 0
		}
		s.mu.Unlock()
	}
	return err
}

// ===== 3. リクエスト送信とパス解決 =====
func (s *InteractionStore) request(ctx context.Context, method, path string, body []byte) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		requestBody := body
		if len(requestBody) == 0 {
			requestBody = []byte("{}")
		}
		req, err := http.NewRequestWithContext(ctx, method, "https://discord.com/api/v10"+path, strings.NewReader(string(requestBody)))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		// ===== Interaction endpoints are authenticated by the short-lived interaction token in the URL. =====
		resp, err := s.client.Do(req)
		if err != nil {
			last = err
			break
		}
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			var retry struct {
				RetryAfter float64 `json:"retry_after"`
			}
			_ = json.Unmarshal(responseBody, &retry)
			wait := time.Duration(retry.RetryAfter * float64(time.Second))
			if wait <= 0 {
				wait = time.Second
			}
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		last = fmt.Errorf("Discord interaction API returned HTTP %d", resp.StatusCode)
		break
	}
	return last
}

// ----- 3.1 パス生成 -----
func interactionCallbackPath(v *interactionRef) string {
	return fmt.Sprintf("/interactions/%s/%s/callback", url.PathEscape(v.InteractionID), url.PathEscape(v.Token))
}
func originalResponsePath(v *interactionRef) string {
	return fmt.Sprintf("/webhooks/%s/%s/messages/@original", url.PathEscape(v.ApplicationID), url.PathEscape(v.Token))
}
func followupPath(v *interactionRef) string {
	return fmt.Sprintf("/webhooks/%s/%s", url.PathEscape(v.ApplicationID), url.PathEscape(v.Token))
}
func unwrapInitialCallback(body json.RawMessage) []byte {
	var v struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &v) == nil && len(v.Data) > 0 && json.Valid(v.Data) {
		return v.Data
	}
	return []byte(body)
}

func randomToken(prefix string) string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b[:])
}
