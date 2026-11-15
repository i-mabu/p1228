package discord

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
	"p1228/gateway/internal/config"
	"p1228/gateway/internal/storage"
)

// ===== 1. 型定義と定数 =====
const gatewayVersion = "10"

type Manager struct {
	cfg          *config.Config
	store        *storage.Storage
	log          *slog.Logger
	interactions *InteractionStore
	// Shared HTTP client: creating a client per REST call discards
	// keep-alive connections and repeats the TLS handshake every time.
	httpClient   *http.Client
	mu           sync.RWMutex
	connections  map[string]*wsConnection
	restMu       map[string]*sync.Mutex
	voiceEvents  map[string]chan storage.Event
	workerFailed map[string]bool
	startedAt    time.Time
}

// ----- 1.1 WebSocket接続とペイロードの型 -----
type wsConnection struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}
type gatewayPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s"`
	T  string          `json:"t"`
}
type gatewayBotInfo struct {
	URL               string `json:"url"`
	Shards            int    `json:"shards"`
	SessionStartLimit struct {
		Total          int   `json:"total"`
		Remaining      int   `json:"remaining"`
		ResetAfter     int64 `json:"reset_after"`
		MaxConcurrency int   `json:"max_concurrency"`
	} `json:"session_start_limit"`
}

// ===== 2. 接続管理 =====
func NewManager(cfg *config.Config, store *storage.Storage, log *slog.Logger) *Manager {
	locks := map[string]*sync.Mutex{}
	for id := range cfg.Bots {
		locks[id] = &sync.Mutex{}
	}
	voiceEvents := map[string]chan storage.Event{}
	for id := range cfg.Bots {
		voiceEvents[id] = make(chan storage.Event, 128)
	}
	return &Manager{cfg: cfg, store: store, log: log, interactions: NewInteractionStore(cfg.Bots), httpClient: &http.Client{Timeout: 15 * time.Second}, connections: map[string]*wsConnection{}, restMu: locks, voiceEvents: voiceEvents, workerFailed: map[string]bool{}, startedAt: time.Now()}
}
func (m *Manager) Start(ctx context.Context) {
	go m.monitorWorkers(ctx)
	for botID := range m.cfg.Bots {
		id := botID
		go m.runBot(ctx, id)
	}
}
func (m *Manager) runBot(ctx context.Context, botID string) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := m.connect(ctx, botID)
		if ctx.Err() != nil {
			return
		}
		m.log.Warn("gateway connection ended", "bot_id", botID, "error", err, "retry_in", backoff.String())
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		// A connection that stayed up for a long time was healthy; do not
		// keep punishing its reconnect with the maximum backoff. Otherwise
		// double the backoff, capped at 60s (the previous check let it
		// overshoot to 64s).
		if time.Since(started) > 5*time.Minute {
			backoff = 2 * time.Second
		} else {
			backoff = min(backoff*2, 60*time.Second)
		}
	}
}
func (m *Manager) connect(ctx context.Context, botID string) error {
	bot := m.cfg.Bots[botID]
	info, err := m.getGatewayBot(ctx, bot.Token)
	if err != nil {
		return fmt.Errorf("get gateway bot: %w", err)
	}
	if info.URL == "" {
		return errors.New("Discord returned an empty Gateway URL")
	}
	sess, err := m.store.LoadSession(ctx, botID)
	if err != nil {
		return err
	}
	if sess.SessionID == "" && info.SessionStartLimit.Remaining <= 0 {
		wait := time.Duration(info.SessionStartLimit.ResetAfter) * time.Millisecond
		if wait > 0 {
			m.log.Warn("gateway identify limit exhausted", "bot_id", botID, "wait", wait.String())
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return errors.New("Gateway identify limit reset; retrying")
	}
	endpoint := info.URL
	if sess.SessionID != "" && sess.ResumeURL != "" {
		endpoint = sess.ResumeURL
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("v", gatewayVersion)
	q.Set("encoding", "json")
	u.RawQuery = q.Encode()
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, u.String(), http.Header{})
	if err != nil {
		if resp != nil {
			return fmt.Errorf("Gateway websocket handshake (%s): %w", resp.Status, err)
		}
		return err
	}
	wc := &wsConnection{conn: conn}
	m.mu.Lock()
	m.connections[botID] = wc
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.connections[botID] == wc {
			delete(m.connections, botID)
		}
		m.mu.Unlock()
		_ = conn.Close()
	}()
	conn.SetReadLimit(8 << 20)
	frames := make(chan []byte, 64)
	readErr := make(chan error, 1)

	// ----- 2.1 受信ゴルーチン -----
	go func() {
		for {
			_, b, e := conn.ReadMessage()
			if e != nil {
				select {
				case readErr <- e:
				default:
				}
				return
			}
			select {
			case frames <- b:
			case <-ctx.Done():
				return
			}
		}
	}()
	var hello gatewayPayload
	select {
	case b := <-frames:
		if err := json.Unmarshal(b, &hello); err != nil {
			return err
		}
	case e := <-readErr:
		return e
	case <-time.After(15 * time.Second):
		return errors.New("Gateway HELLO timeout")
	case <-ctx.Done():
		return ctx.Err()
	}
	if hello.Op != 10 {
		return fmt.Errorf("expected HELLO opcode 10, got %d", hello.Op)
	}
	var hd struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	if err := json.Unmarshal(hello.D, &hd); err != nil {
		return err
	}
	if hd.HeartbeatInterval < 1000 {
		return fmt.Errorf("invalid heartbeat interval: %d", hd.HeartbeatInterval)
	}

	// ----- 2.2 ResumeとIdentify -----
	if sess.SessionID != "" && sess.Sequence >= 0 {
		if err := wc.writeJSON(map[string]any{"op": 6, "d": map[string]any{"token": bot.Token, "session_id": sess.SessionID, "seq": sess.Sequence}}); err != nil {
			return err
		}
		m.log.Info("gateway resume requested", "bot_id", botID, "sequence", sess.Sequence)
	} else {
		if err := wc.writeJSON(map[string]any{"op": 2, "d": map[string]any{"token": bot.Token, "intents": bot.Intents, "properties": map[string]string{"os": "linux", "browser": "p1228-gateway", "device": "p1228-gateway"}, "shard": []int{0, 1}}}); err != nil {
			return err
		}
		m.log.Info("gateway identify sent", "bot_id", botID, "shards", info.Shards, "session_start_remaining", info.SessionStartLimit.Remaining)
	}
	ticker := time.NewTicker(time.Duration(hd.HeartbeatInterval) * time.Millisecond)
	defer ticker.Stop()
	acked := true
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !acked {
				return errors.New("Gateway heartbeat ACK timeout")
			}
			acked = false
			s, _ := m.store.LoadSession(ctx, botID)
			var seq any
			if s.Sequence >= 0 {
				seq = s.Sequence
			}
			if err := wc.writeJSON(map[string]any{"op": 1, "d": seq}); err != nil {
				return err
			}
		case e := <-readErr:
			return e
		case b := <-frames:
			var p gatewayPayload
			if err := json.Unmarshal(b, &p); err != nil {
				m.log.Warn("invalid gateway frame", "bot_id", botID, "error", err)
				continue
			}
			switch p.Op {
			case 0:
				if p.S != nil {
					if err := m.handleDispatch(ctx, botID, p); err != nil {
						return fmt.Errorf("persist dispatch %s: %w", p.T, err)
					}
				}
				if p.T == "READY" || p.T == "RESUMED" {
					if err := wc.writeJSON(m.workerPresence(botID, bot)); err != nil {
						return fmt.Errorf("set watching presence: %w", err)
					}
				}
			case 1:
				s, _ := m.store.LoadSession(ctx, botID)
				var seq any
				if s.Sequence >= 0 {
					seq = s.Sequence
				}
				if err := wc.writeJSON(map[string]any{"op": 1, "d": seq}); err != nil {
					return err
				}
				acked = false
			case 7:
				return errors.New("Gateway requested reconnect")
			case 9:
				var resumable bool
				_ = json.Unmarshal(p.D, &resumable)
				if !resumable {
					if err := m.store.ClearSession(ctx, botID); err != nil {
						return err
					}
				}
				return fmt.Errorf("invalid session (resumable=%t)", resumable)
			case 11:
				acked = true
			}
		}
	}
}

// ===== 3. Worker監視とプレゼンス =====
const workerHeartbeatTimeout = 35 * time.Second
const workerMonitorInterval = 5 * time.Second
const workerStartupGrace = 60 * time.Second

func workerMissingAfterStartupGrace(startedAt, now time.Time) bool {
	return !startedAt.IsZero() && now.Sub(startedAt) > workerStartupGrace
}

// ----- 3.1 プレゼンス生成 -----
func presenceUpdate(bot config.BotConfig) map[string]any {
	return workerPresenceUpdate(bot, false)
}

func workerPresenceUpdate(bot config.BotConfig, failed bool) map[string]any {
	activity := bot.Watching
	if failed {
		activity = "障害対応中：Worker応答なし"
	}
	return map[string]any{
		"op": 3,
		"d": map[string]any{
			"since":      nil,
			"activities": []map[string]any{{"name": activity, "type": 3}},
			"status":     "online",
			"afk":        false,
		},
	}
}

func (m *Manager) workerPresence(botID string, bot config.BotConfig) map[string]any {
	m.mu.RLock()
	failed := m.workerFailed[botID]
	m.mu.RUnlock()
	return workerPresenceUpdate(bot, failed)
}

// ----- 3.2 Worker監視ループ -----
func (m *Manager) monitorWorkers(ctx context.Context) {
	ticker := time.NewTicker(workerMonitorInterval)
	defer ticker.Stop()
	m.checkWorkerHeartbeats(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.checkWorkerHeartbeats(ctx)
		}
	}
}

func (m *Manager) checkWorkerHeartbeats(ctx context.Context) {
	heartbeats, err := m.store.LatestWorkerHeartbeats(ctx)
	if err != nil {
		m.log.Error("worker heartbeat status query failed", "error", err)
		return
	}
	now := time.Now()
	for _, worker := range m.cfg.Workers {
		last, ok := heartbeats[worker.BotID]
		// Allow a bounded startup/provisioning window, then report a configured
		// Worker that never registered instead of leaving its presence healthy forever.
		if !ok {
			if workerMissingAfterStartupGrace(m.startedAt, now) {
				m.setWorkerFailure(worker.BotID, true, time.Time{})
			}
			continue
		}
		m.setWorkerFailure(worker.BotID, now.Sub(last) > workerHeartbeatTimeout, last)
	}
}

func (m *Manager) setWorkerFailure(botID string, failed bool, lastHeartbeat time.Time) {
	m.mu.Lock()
	previous, known := m.workerFailed[botID]
	if known && previous == failed {
		m.mu.Unlock()
		return
	}
	m.workerFailed[botID] = failed
	conn := m.connections[botID]
	m.mu.Unlock()

	if failed {
		m.log.Error("worker heartbeat timeout; degraded presence enabled", "bot_id", botID, "last_heartbeat", lastHeartbeat.UTC().Format(time.RFC3339Nano))
	} else if known {
		m.log.Info("worker heartbeat recovered; normal presence restored", "bot_id", botID)
	}
	if conn == nil {
		return
	}
	bot, ok := m.cfg.Bots[botID]
	if !ok {
		return
	}
	if err := conn.writeJSON(workerPresenceUpdate(bot, failed)); err != nil {
		m.log.Warn("worker status presence update failed", "bot_id", botID, "error", err)
	}
}

// ----- 3.3 Gateway情報の取得 -----
func (m *Manager) getGatewayBot(ctx context.Context, token string) (gatewayBotInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://discord.com/api/v10/gateway/bot", nil)
	if err != nil {
		return gatewayBotInfo{}, err
	}
	req.Header.Set("Authorization", "Bot "+token)
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return gatewayBotInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return gatewayBotInfo{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var v gatewayBotInfo
	err = json.NewDecoder(resp.Body).Decode(&v)
	return v, err
}

// ===== 4. イベント振り分け =====
func (m *Manager) handleDispatch(ctx context.Context, botID string, p gatewayPayload) error {
	var d map[string]any
	if err := json.Unmarshal(p.D, &d); err != nil {
		return err
	}
	sess, err := m.store.LoadSession(ctx, botID)
	if err != nil {
		return err
	}

	// ----- 4.1 セッション確立イベント -----
	if p.T == "READY" {
		if v, ok := d["session_id"].(string); ok {
			sess.SessionID = v
		}
		if v, ok := d["resume_gateway_url"].(string); ok {
			sess.ResumeURL = v
		}
		if user, ok := d["user"].(map[string]any); ok {
			if id, ok := user["id"].(string); ok {
				sess.BotUserID = id
			}
		}
		m.log.Info("gateway ready", "bot_id", botID, "user_id", sess.BotUserID)
	}
	if p.T == "RESUMED" {
		m.log.Info("gateway resumed", "bot_id", botID)
	}
	if p.S != nil {
		sess.Sequence = *p.S
	}
	var ev *storage.Event
	if shouldQueue(botID, p.T) {

		// ----- 4.2 インタラクションの振り分け -----
		if p.T == "INTERACTION_CREATE" {
			iid, _ := d["id"].(string)
			token, _ := d["token"].(string)
			appID, _ := d["application_id"].(string)
			if iid != "" && token != "" {
				ref := m.interactions.Create(botID, iid, token, appID, time.Duration(m.cfg.Gateway.InteractionTTLSeconds)*time.Second)
				d["interaction_ref"] = ref
				interactionType, _ := d["type"].(float64)
				deferType := 0
				switch int(interactionType) {
				case 2, 5:
					deferType = 5
				case 3:
					deferType = 6
				case 4:
					deferType = 8 // Autocomplete must receive choices; empty fallback beats a 3s timeout.
				}
				if deferType != 0 {
					go func() {
						timer := time.NewTimer(2 * time.Second)
						defer timer.Stop()
						select {
						case <-timer.C:
							ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
							defer cancel()
							if err := m.interactions.AutoDefer(ctx2, botID, ref, deferType); err != nil {
								m.log.Debug("interaction auto-defer skipped or failed", "bot_id", botID, "error", err)
							}
						}
					}()
				}
			}
			delete(d, "token")
		}
		if botID == "general" && p.T == "VOICE_STATE_UPDATE" {
			// The general Worker only needs channel transitions for logging. Keep Voice
			// session identifiers in the existing short-lived read-aloud path.
			delete(d, "session_id")
		}
		payload, err := json.Marshal(d)
		if err != nil {
			return err
		}
		sid := sess.SessionID
		if sid == "" {
			sid = "no-session"
		}
		h := sha256.Sum256([]byte(sid))
		// 8 bytes (64 bits) of the session hash: with only 4 bytes, two
		// different sessions could collide by birthday bound (~65k sessions)
		// while their sequence numbers overlap, and the duplicate event_id
		// primary key would silently drop a real event (INSERT OR IGNORE).
		eventID := fmt.Sprintf("%s_%s_%d", botID, hex.EncodeToString(h[:8]), *p.S)
		guildID := stringField(d, "guild_id")
		channelID := stringField(d, "channel_id")
		ordering := botID + ":" + guildID + ":" + channelID
		if guildID == "" {
			ordering = botID + ":" + p.T + ":" + eventID
		}
		if p.T == "INTERACTION_CREATE" {
			ordering = botID + ":interaction:" + stringField(d, "id")
		}
		ev = &storage.Event{ProtocolVersion: 1, EventID: eventID, CorrelationID: randomToken("corr_"), BotID: botID, BotUserID: sess.BotUserID, EventType: p.T, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), OrderingKey: ordering, SessionID: sid, Sequence: *p.S, Payload: payload}
	}
	if err := m.store.RecordDispatch(ctx, sess, ev); err != nil {
		return err
	}
	if (botID == "read_aloud_a" || botID == "read_aloud_b") && (p.T == "VOICE_SERVER_UPDATE" || p.T == "VOICE_STATE_UPDATE") {
		// The adapter only needs this BOT's own voice-state updates. Filtering unrelated
		// guild-member state changes keeps the short-lived in-memory queue bounded.

		// ----- 4.3 音声状態イベント -----
		if p.T == "VOICE_STATE_UPDATE" && stringField(d, "user_id") != sess.BotUserID {
			return nil
		}
		payload, _ := json.Marshal(d)
		event := storage.Event{ProtocolVersion: 1, EventID: fmt.Sprintf("voice_%s_%d", botID, sess.Sequence), CorrelationID: randomToken("corr_"), BotID: botID, BotUserID: sess.BotUserID, EventType: p.T, OccurredAt: time.Now().UTC().Format(time.RFC3339Nano), OrderingKey: botID + ":voice:" + stringField(d, "guild_id"), SessionID: sess.SessionID, Sequence: sess.Sequence, Payload: payload, Ephemeral: true}
		select {
		case m.voiceEvents[botID] <- event:
		default:
			m.log.Error("voice event buffer full; voice session must reconnect", "bot_id", botID)
		}
	}
	return nil
}

// ----- 4.4 キュー対象の判定 -----
func shouldQueue(botID, t string) bool {
	switch t {
	case "MESSAGE_CREATE", "MESSAGE_UPDATE", "MESSAGE_DELETE", "MESSAGE_DELETE_BULK", "INTERACTION_CREATE":
		return true
	case "GUILD_CREATE", "GUILD_DELETE", "GUILD_MEMBER_ADD", "GUILD_MEMBER_REMOVE", "GUILD_MEMBER_UPDATE",
		"CHANNEL_CREATE", "CHANNEL_UPDATE", "CHANNEL_DELETE", "VOICE_STATE_UPDATE":
		return botID == "general"
	default:
		return false
	}
}
func stringField(m map[string]any, k string) string { v, _ := m[k].(string); return v }

// ===== 5. 音声イベントとREST連携 =====
// ClaimVoiceEvent returns short-lived Voice Server Update data from memory only.
func (m *Manager) ClaimVoiceEvent(botID string) *storage.Event {
	ch := m.voiceEvents[botID]
	if ch == nil {
		return nil
	}
	for {
		select {
		case ev := <-ch:
			// Voice Server tokens and Voice session state are short-lived transport data.
			// Never deliver stale queue entries after a reconnect or worker stall.
			occurred, err := time.Parse(time.RFC3339Nano, ev.OccurredAt)
			if err != nil || time.Since(occurred) > 30*time.Second {
				m.log.Warn("stale ephemeral voice event dropped", "bot_id", botID, "event_type", ev.EventType)
				continue
			}
			return &ev
		default:
			return nil
		}
	}
}

// ----- 5.1 音声ペイロード送信 -----
func (m *Manager) SendVoicePayload(botID string, payload json.RawMessage) error {
	var v struct {
		Op int             `json:"op"`
		D  json.RawMessage `json:"d"`
	}
	if err := json.Unmarshal(payload, &v); err != nil {
		return err
	}
	if v.Op != 4 {
		return errors.New("only Voice State Update opcode 4 is accepted")
	}
	var d struct {
		GuildID string `json:"guild_id"`
	}
	if err := json.Unmarshal(v.D, &d); err != nil {
		return err
	}
	if d.GuildID == "" {
		return errors.New("voice payload requires guild_id")
	}
	if botID != "read_aloud_a" && botID != "read_aloud_b" {
		return errors.New("voice payload is only allowed for read-aloud bots")
	}
	m.mu.RLock()
	c := m.connections[botID]
	m.mu.RUnlock()
	if c == nil {
		return errors.New("bot Gateway is not connected")
	}
	return c.writeJSON(json.RawMessage(payload))
}
func (c *wsConnection) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteJSON(v)
}

// ----- 5.2 REST操作 -----
func (m *Manager) DiscordOperation(ctx context.Context, botID string, op map[string]json.RawMessage) (int, []byte, error) {
	bot, ok := m.cfg.Bots[botID]
	if !ok {
		return 0, nil, errors.New("unknown bot")
	}
	var operation, channelID, messageID, content, applicationID, guildID, userID, roleID string
	for name, target := range map[string]*string{
		"operation": &operation, "channel_id": &channelID, "message_id": &messageID,
		"content": &content, "application_id": &applicationID, "guild_id": &guildID,
		"user_id": &userID, "role_id": &roleID,
	} {
		raw, exists := op[name]
		if !exists {
			continue
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return 0, nil, fmt.Errorf("%s must be a string", name)
		}
	}
	var method, path string
	var body []byte
	switch operation {
	case "send_message":
		if channelID == "" || strings.TrimSpace(content) == "" || utf8.RuneCountInString(content) > 2000 {
			return 0, nil, errors.New("channel_id and message content of at most 2000 characters are required")
		}
		method = http.MethodPost
		path = "/channels/" + url.PathEscape(channelID) + "/messages"
		message := map[string]any{"content": content, "allowed_mentions": map[string]any{"parse": []string{}}}
		if raw, exists := op["components"]; exists {
			var components []json.RawMessage
			if !json.Valid(raw) || json.Unmarshal(raw, &components) != nil || components == nil {
				return 0, nil, errors.New("components must be a JSON array")
			}
			message["components"] = json.RawMessage(raw)
		}
		body, _ = json.Marshal(message)
	case "edit_message":
		if channelID == "" || messageID == "" {
			return 0, nil, errors.New("channel_id and message_id are required")
		}
		if _, exists := op["content"]; !exists {
			return 0, nil, errors.New("content is required")
		}
		if utf8.RuneCountInString(content) > 2000 {
			return 0, nil, errors.New("message content must be at most 2000 characters")
		}
		method = http.MethodPatch
		path = "/channels/" + url.PathEscape(channelID) + "/messages/" + url.PathEscape(messageID)
		body, _ = json.Marshal(map[string]any{
			"content":          content,
			"allowed_mentions": map[string]any{"parse": []string{}},
		})
	case "delete_message":
		if channelID == "" || messageID == "" {
			return 0, nil, errors.New("channel_id and message_id are required")
		}
		method = http.MethodDelete
		path = "/channels/" + url.PathEscape(channelID) + "/messages/" + url.PathEscape(messageID)
	case "sync_commands":
		if applicationID == "" || applicationID != bot.ApplicationID {
			return 0, nil, errors.New("application_id does not match configured bot")
		}
		if len(op["commands"]) == 0 || !json.Valid(op["commands"]) {
			return 0, nil, errors.New("commands must be valid JSON")
		}
		method = http.MethodPut
		path = "/applications/" + url.PathEscape(applicationID) + "/commands"
		body = op["commands"]
	case "add_role", "remove_role":
		if botID != "general" {
			return 0, nil, errors.New("role management is only allowed for the general bot")
		}
		if guildID == "" || userID == "" || roleID == "" {
			return 0, nil, errors.New("guild_id, user_id and role_id are required")
		}
		method = http.MethodPut
		if operation == "remove_role" {
			method = http.MethodDelete
		}
		path = "/guilds/" + url.PathEscape(guildID) + "/members/" + url.PathEscape(userID) + "/roles/" + url.PathEscape(roleID)
	default:
		return 0, nil, fmt.Errorf("operation %q is not allowed", operation)
	}
	mu := m.restMu[botID]
	mu.Lock()
	defer mu.Unlock()
	return m.doREST(ctx, bot.Token, method, path, body)
}
func (m *Manager) doREST(ctx context.Context, token, method, path string, body []byte) (int, []byte, error) {
	client := m.httpClient
	for attempt := 0; attempt < 3; attempt++ {
		var rd io.Reader
		if len(body) > 0 {
			rd = strings.NewReader(string(body))
		}
		req, err := http.NewRequestWithContext(ctx, method, "https://discord.com/api/v10"+path, rd)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bot "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		resp.Body.Close()
		if resp.StatusCode == 429 {
			var x struct {
				RetryAfter float64 `json:"retry_after"`
			}
			_ = json.Unmarshal(b, &x)
			retryAfter := x.RetryAfter
			if header := resp.Header.Get("Retry-After"); header != "" {
				if parsed, parseErr := strconv.ParseFloat(header, 64); parseErr == nil && parsed > 0 {
					retryAfter = parsed
				}
			}
			wait := time.Duration(retryAfter * float64(time.Second))
			if wait <= 0 {
				wait = time.Second
			}
			select {
			case <-time.After(wait):
				continue
			case <-ctx.Done():
				return 0, nil, ctx.Err()
			}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return resp.StatusCode, b, fmt.Errorf("Discord API returned HTTP %d", resp.StatusCode)
		}
		return resp.StatusCode, b, nil
	}
	return 0, nil, errors.New("Discord API retry limit exceeded")
}
func (m *Manager) InteractionAction(ctx context.Context, botID, ref, action string, body json.RawMessage) error {
	return m.interactions.Do(ctx, botID, ref, action, body)
}
