package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"p1228/gateway/internal/config"
	"p1228/gateway/internal/discord"
	"p1228/gateway/internal/storage"
)

// ===== 1. 型定義とサーバー生成 =====
type Server struct {
	cfg        *config.Config
	store      *storage.Storage
	gateway    *discord.Manager
	log        *slog.Logger
	workers    map[string]config.WorkerConfig
	httpServer *http.Server
}
type authContext struct{ Worker config.WorkerConfig }

func New(cfg *config.Config, store *storage.Storage, gateway *discord.Manager, workers map[string]config.WorkerConfig, logger *slog.Logger) *Server {
	byID := map[string]config.WorkerConfig{}
	for _, w := range workers {
		byID[w.WorkerID] = w
	}
	addr := net.JoinHostPort(cfg.Gateway.ListenAddress, strconv.Itoa(cfg.Gateway.ListenPort))
	s := &Server{cfg: cfg, store: store, gateway: gateway, log: logger, workers: byID}
	s.httpServer = &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	return s
}
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }
func (s *Server) ListenAndServe() error {
	listener, err := net.Listen("tcp", s.httpServer.Addr)
	if err != nil {
		return err
	}
	s.log.Info("internal API listening", "address", s.httpServer.Addr)
	return s.httpServer.Serve(listener)
}

// ===== 2. ルーティングと認証 =====
func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.URL.Path == "/internal/v1/health" {
		if r.Method != "GET" {
			methodNotAllowed(w)
			return
		}
		if err := s.store.DB().PingContext(r.Context()); err != nil {
			writeError(w, 503, "STORAGE_UNAVAILABLE", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "status": "ready", "protocol_version": 1})
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/internal/v1/") {
		http.NotFound(w, r)
		return
	}
	auth, ok := s.authenticate(r)
	if !ok {
		writeError(w, 401, "UNAUTHORIZED", "worker authentication failed")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/internal/v1/")
	switch {
	case path == "worker/register" && r.Method == http.MethodPost:
		s.register(w, r, auth)
	case path == "worker/heartbeat" && r.Method == http.MethodPost:
		s.heartbeat(w, r, auth)
	case path == "events/claim" && r.Method == http.MethodPost:
		s.claim(w, r, auth)
	case strings.HasPrefix(path, "events/") && strings.HasSuffix(path, "/ack") && r.Method == http.MethodPost:
		s.eventAck(w, r, auth, path)
	case strings.HasPrefix(path, "events/") && strings.HasSuffix(path, "/nack") && r.Method == http.MethodPost:
		s.eventNack(w, r, auth, path)
	case path == "discord/operations" && r.Method == http.MethodPost:
		s.discordOperation(w, r, auth)
	case path == "voice/gateway-payload" && r.Method == http.MethodPost:
		s.voicePayload(w, r, auth)
	case strings.HasPrefix(path, "interactions/") && r.Method == http.MethodPost:
		s.interactionAction(w, r, auth, path)
	case strings.HasPrefix(path, "data/read-aloud/settings") && (r.Method == http.MethodGet || r.Method == http.MethodPut):
		s.settings(w, r, auth)
	case strings.HasPrefix(path, "data/dictionary/entries") && (r.Method == http.MethodGet || r.Method == http.MethodPost):
		s.dictionary(w, r, auth)
	case strings.HasPrefix(path, "data/star/settings") && (r.Method == http.MethodGet || r.Method == http.MethodPut):
		s.starSettings(w, r, auth)
	case path == "data/star/blacklist" && (r.Method == http.MethodGet || r.Method == http.MethodPost || r.Method == http.MethodDelete):
		s.starBlacklist(w, r, auth)
	case path == "data/star/sticky" && (r.Method == http.MethodGet || r.Method == http.MethodPut || r.Method == http.MethodPost || r.Method == http.MethodDelete):
		s.starSticky(w, r, auth)
	case path == "data/star/points" && r.Method == http.MethodPost:
		s.starPoints(w, r, auth)
	case path == "data/star/shop" && (r.Method == http.MethodGet || r.Method == http.MethodPut || r.Method == http.MethodPost):
		s.starShop(w, r, auth)
	default:
		writeError(w, 404, "NOT_FOUND", "endpoint not found")
	}
}
func (s *Server) authenticate(r *http.Request) (authContext, bool) {
	id := r.Header.Get("X-P1228-Worker-ID")
	w, ok := s.workers[id]
	if !ok {
		return authContext{}, false
	}
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return authContext{}, false
	}
	a := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if a == "" {
		return authContext{}, false
	}
	// ===== Compare fixed-length digests: comparing the raw secrets with =====
	// ===== ConstantTimeCompare returns early when lengths differ, which would =====
	// ===== leak the configured secret's length through timing. =====
	presented := sha256.Sum256([]byte(a))
	expected := sha256.Sum256([]byte(w.Secret))
	if subtle.ConstantTimeCompare(presented[:], expected[:]) != 1 {
		return authContext{}, false
	}
	return authContext{Worker: w}, true
}

// ===== 3. イベント受付ハンドラ =====
func (s *Server) register(w http.ResponseWriter, r *http.Request, a authContext) {
	var req struct {
		InstanceID string `json:"instance_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.InstanceID) == "" || len(req.InstanceID) > 128 {
		writeError(w, 400, "INVALID_REQUEST", "instance_id is required")
		return
	}
	if err := s.store.RegisterWorker(r.Context(), req.InstanceID, storage.WorkerIdentity{WorkerID: a.Worker.WorkerID, Role: a.Worker.Role, BotID: a.Worker.BotID}); err != nil {
		writeError(w, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "worker_id": a.Worker.WorkerID, "bot_id": a.Worker.BotID})
}
func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request, a authContext) {
	var req struct {
		InstanceID string `json:"instance_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.store.Heartbeat(r.Context(), req.InstanceID, a.Worker.WorkerID, a.Worker.BotID); err != nil {
		writeError(w, 409, "WORKER_NOT_REGISTERED", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "timestamp": time.Now().UTC()})
}
func (s *Server) claim(w http.ResponseWriter, r *http.Request, a authContext) {
	var req struct {
		InstanceID string `json:"instance_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.InstanceID == "" {
		writeError(w, 400, "INVALID_REQUEST", "instance_id is required")
		return
	}
	registered, regErr := s.store.IsWorkerRegistered(r.Context(), req.InstanceID, a.Worker.WorkerID, a.Worker.BotID)
	if regErr != nil || !registered {
		writeError(w, 403, "WORKER_NOT_REGISTERED", "worker instance is not registered for this identity")
		return
	}
	if a.Worker.Role == "read-aloud" {
		ephemeral := s.gateway.ClaimVoiceEvent(a.Worker.BotID)
		if ephemeral != nil {
			writeJSON(w, 200, map[string]any{"success": true, "event": ephemeral})
			return
		}
	}
	ev, err := s.store.ClaimEvent(r.Context(), a.Worker.BotID, req.InstanceID, s.cfg.Gateway.EventLeaseSeconds, s.cfg.Gateway.MaxEventAttempts)
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", err.Error())
		return
	}
	if ev == nil {
		writeJSON(w, 200, map[string]any{"success": true, "event": nil})
		return
	}
	ev.Event.LeaseToken = ev.LeaseToken
	ev.Event.Attempts = ev.Attempts
	writeJSON(w, 200, map[string]any{"success": true, "event": ev.Event})
}
func (s *Server) eventAck(w http.ResponseWriter, r *http.Request, a authContext, path string) {
	eventID := strings.TrimSuffix(strings.TrimPrefix(path, "events/"), "/ack")
	eventID = strings.Trim(eventID, "/")
	var req struct {
		LeaseToken string `json:"lease_token"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.store.AckEvent(r.Context(), a.Worker.BotID, eventID, req.LeaseToken); err != nil {
		writeError(w, 409, "LEASE_CONFLICT", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}
func (s *Server) eventNack(w http.ResponseWriter, r *http.Request, a authContext, path string) {
	eventID := strings.TrimSuffix(strings.TrimPrefix(path, "events/"), "/nack")
	eventID = strings.Trim(eventID, "/")
	var req struct {
		LeaseToken string `json:"lease_token"`
		Reason     string `json:"reason"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.store.NackEvent(r.Context(), a.Worker.BotID, eventID, req.LeaseToken, truncate(req.Reason, 1000), s.cfg.Gateway.MaxEventAttempts); err != nil {
		writeError(w, 409, "LEASE_CONFLICT", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

// ===== 4. Discord操作・設定・辞書ハンドラ =====
func (s *Server) discordOperation(w http.ResponseWriter, r *http.Request, a authContext) {
	var req map[string]json.RawMessage
	if !decode(w, r, &req) {
		return
	}
	var id, op string
	_ = json.Unmarshal(req["operation_id"], &id)
	_ = json.Unmarshal(req["operation"], &op)
	if id == "" || op == "" {
		writeError(w, 400, "INVALID_REQUEST", "operation_id and operation are required")
		return
	}
	created, reserveErr := s.store.CreateOperation(r.Context(), id, a.Worker.BotID, op)
	if reserveErr != nil {
		writeError(w, 500, "INTERNAL_ERROR", reserveErr.Error())
		return
	}
	if !created {
		status, responseRef, err := s.store.GetOperationResult(r.Context(), id, a.Worker.BotID, op)
		if err != nil {
			writeError(w, 409, "DUPLICATE_OPERATION", "operation_id has already been used by another operation")
			return
		}
		if status == "succeeded" {
			if op == "send_message" {
				if responseRef == "" {
					writeError(w, 502, "OPERATION_RESULT_MISSING", "the completed message operation has no stored message ID")
					return
				}
				writeJSON(w, 200, map[string]any{"id": responseRef})
				return
			}
			writeJSON(w, 200, map[string]any{"success": true, "status": http.StatusOK})
			return
		}
		writeError(w, 409, "DUPLICATE_OPERATION", "operation_id has already been used; do not replay a request with an unknown external outcome")
		return
	}
	status, body, err := s.gateway.DiscordOperation(r.Context(), a.Worker.BotID, req)
	if err != nil {
		state := "failed"
		if errors.Is(err, net.ErrClosed) || errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "timeout") {
			state = "unknown"
		}
		if persistErr := s.persistOperationState(id, a.Worker.BotID, op, state, "", truncate(err.Error(), 1000)); persistErr != nil {
			s.log.Error("discord operation outcome persistence failed", "operation_id", id, "bot_id", a.Worker.BotID, "error", persistErr)
		}
		writeError(w, 502, "DISCORD_API_ERROR", err.Error())
		return
	}
	ref := ""
	var obj map[string]any
	if json.Unmarshal(body, &obj) == nil {
		if v, ok := obj["id"].(string); ok {
			ref = v
		}
	}
	if err := s.persistOperationState(id, a.Worker.BotID, op, "succeeded", ref, ""); err != nil {
		s.log.Error("discord operation outcome persistence failed after success", "operation_id", id, "bot_id", a.Worker.BotID, "error", err)
		writeError(w, 500, "OPERATION_OUTCOME_NOT_RECORDED", "Discord operation may have succeeded but its result was not recorded; do not retry with a new operation ID")
		return
	}
	if status == http.StatusNoContent {
		writeJSON(w, 200, map[string]any{"success": true, "status": status})
		return
	}
	w.WriteHeader(200)
	_, _ = w.Write(body)
}

func (s *Server) persistOperationState(id, botID, operation, status, responseRef, errorMessage string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.store.UpdateOperation(ctx, id, botID, operation, status, responseRef, errorMessage)
}

func (s *Server) voicePayload(w http.ResponseWriter, r *http.Request, a authContext) {
	if a.Worker.Role != "read-aloud" {
		writeError(w, 403, "FORBIDDEN", "voice gateway access is only allowed for read-aloud workers")
		return
	}
	var req struct {
		Payload json.RawMessage `json:"payload"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Payload) == 0 {
		writeError(w, 400, "INVALID_REQUEST", "payload is required")
		return
	}
	if err := s.gateway.SendVoicePayload(a.Worker.BotID, req.Payload); err != nil {
		writeError(w, 503, "GATEWAY_UNAVAILABLE", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}
func (s *Server) interactionAction(w http.ResponseWriter, r *http.Request, a authContext, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 {
		writeError(w, 404, "NOT_FOUND", "interaction endpoint not found")
		return
	}
	ref, action := parts[1], parts[2]
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeError(w, 413, "PAYLOAD_TOO_LARGE", "interaction body is too large")
		return
	}
	if err := s.gateway.InteractionAction(r.Context(), a.Worker.BotID, ref, action, body); err != nil {
		writeError(w, 409, "INTERACTION_ERROR", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}
func (s *Server) settings(w http.ResponseWriter, r *http.Request, a authContext) {
	if a.Worker.Role != "read-aloud" {
		writeError(w, 403, "FORBIDDEN", "settings API is only allowed for read-aloud workers")
		return
	}
	scope := r.URL.Query().Get("scope")
	scopeID := r.URL.Query().Get("scope_id")
	if r.Method == http.MethodGet {
		data, rev, err := s.store.GetReadAloudSettings(r.Context(), scope, scopeID)
		if err != nil {
			writeError(w, 400, "INVALID_REQUEST", err.Error())
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write(mustJSON(map[string]any{"success": true, "scope": scope, "scope_id": scopeID, "revision": rev, "settings": json.RawMessage(data)}))
		return
	}
	var req struct {
		Scope    string          `json:"scope"`
		ScopeID  string          `json:"scope_id"`
		Settings json.RawMessage `json:"settings"`
	}
	if !decode(w, r, &req) {
		return
	}
	rev, err := s.store.PutReadAloudSettings(r.Context(), req.Scope, req.ScopeID, req.Settings, a.Worker.WorkerID)
	if err != nil {
		writeError(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "revision": rev})
}
func (s *Server) dictionary(w http.ResponseWriter, r *http.Request, a authContext) {
	if a.Worker.Role != "read-aloud" {
		writeError(w, 403, "FORBIDDEN", "dictionary API is only allowed for read-aloud workers")
		return
	}
	scope, scopeID := r.URL.Query().Get("scope"), r.URL.Query().Get("scope_id")
	if r.Method == http.MethodGet {
		items, err := s.store.ListDictionary(r.Context(), scope, scopeID)
		if err != nil {
			writeError(w, 500, "INTERNAL_ERROR", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "entries": items})
		return
	}
	var req struct {
		Scope   string `json:"scope"`
		ScopeID string `json:"scope_id"`
		Word    string `json:"word"`
		Reading string `json:"reading"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.store.PutDictionaryEntry(r.Context(), req.Scope, req.ScopeID, req.Word, req.Reading, a.Worker.WorkerID); err != nil {
		writeError(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

func (s *Server) requireGeneralWorker(w http.ResponseWriter, a authContext) bool {
	if a.Worker.BotID != "general" || a.Worker.Role != "general" {
		writeError(w, 403, "FORBIDDEN", "StarTomo features are only available to the general worker")
		return false
	}
	return true
}

// ===== 5. 星燈機能ハンドラ =====
func (s *Server) starSettings(w http.ResponseWriter, r *http.Request, a authContext) {
	if !s.requireGeneralWorker(w, a) {
		return
	}
	if r.Method == http.MethodGet {
		guildID := r.URL.Query().Get("guild_id")
		if guildID == "" {
			writeError(w, 400, "INVALID_REQUEST", "guild_id is required")
			return
		}
		settings, err := s.store.GetGuildFeatureSettings(r.Context(), guildID)
		if err != nil {
			writeError(w, 500, "INTERNAL_ERROR", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "settings": settings})
		return
	}
	var req struct {
		GuildID string          `json:"guild_id"`
		Key     string          `json:"key"`
		Value   json.RawMessage `json:"value"`
	}
	if !decode(w, r, &req) {
		return
	}
	if err := s.store.SetGuildFeatureSetting(r.Context(), req.GuildID, req.Key, req.Value); err != nil {
		writeError(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

func (s *Server) starBlacklist(w http.ResponseWriter, r *http.Request, a authContext) {
	if !s.requireGeneralWorker(w, a) {
		return
	}
	if r.Method == http.MethodGet {
		guildID, userID := r.URL.Query().Get("guild_id"), r.URL.Query().Get("user_id")
		if guildID == "" || userID == "" {
			writeError(w, 400, "INVALID_REQUEST", "guild_id and user_id are required")
			return
		}
		blocked, err := s.store.IsGuildBlacklisted(r.Context(), guildID, userID)
		if err != nil {
			writeError(w, 500, "INTERNAL_ERROR", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "blacklisted": blocked})
		return
	}
	var req struct {
		GuildID string `json:"guild_id"`
		UserID  string `json:"user_id"`
		Reason  string `json:"reason"`
	}
	if !decode(w, r, &req) {
		return
	}
	var err error
	if r.Method == http.MethodPost {
		err = s.store.AddGuildBlacklist(r.Context(), req.GuildID, req.UserID, req.Reason)
	} else {
		err = s.store.RemoveGuildBlacklist(r.Context(), req.GuildID, req.UserID)
	}
	if err != nil {
		writeError(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

func (s *Server) starSticky(w http.ResponseWriter, r *http.Request, a authContext) {
	if !s.requireGeneralWorker(w, a) {
		return
	}
	if r.Method == http.MethodGet {
		guildID := r.URL.Query().Get("guild_id")
		if guildID == "" {
			writeError(w, 400, "INVALID_REQUEST", "guild_id is required")
			return
		}
		items, err := s.store.ListStickyMessages(r.Context(), guildID)
		if err != nil {
			writeError(w, 500, "INTERNAL_ERROR", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "items": items})
		return
	}
	var req struct {
		GuildID      string `json:"guild_id"`
		ChannelID    string `json:"channel_id"`
		MessageID    string `json:"message_id"`
		OldMessageID string `json:"old_message_id"`
		Content      string `json:"content"`
	}
	if !decode(w, r, &req) {
		return
	}
	var err error
	switch r.Method {
	case http.MethodPut:
		err = s.store.SetStickyMessage(r.Context(), storage.StickyMessage{GuildID: req.GuildID, ChannelID: req.ChannelID, MessageID: req.MessageID, Content: req.Content})
	case http.MethodPost:
		err = s.store.UpdateStickyMessageID(r.Context(), req.GuildID, req.ChannelID, req.OldMessageID, req.MessageID)
	case http.MethodDelete:
		err = s.store.RemoveStickyMessage(r.Context(), req.GuildID, req.ChannelID)
	}
	if err != nil {
		writeError(w, 400, "INVALID_REQUEST", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true})
}

func (s *Server) starPoints(w http.ResponseWriter, r *http.Request, a authContext) {
	if !s.requireGeneralWorker(w, a) {
		return
	}
	var req struct {
		Action          string `json:"action"`
		GuildID         string `json:"guild_id"`
		UserID          string `json:"user_id"`
		EventID         string `json:"event_id"`
		Points          int64  `json:"points"`
		CooldownSeconds int64  `json:"cooldown_seconds"`
		DailyLimit      int64  `json:"daily_limit"`
		Cost            int64  `json:"cost"`
		Prize           int64  `json:"prize"`
		Chance          int64  `json:"chance"`
	}
	if !decode(w, r, &req) {
		return
	}
	var result any
	var err error
	switch req.Action {
	case "balance":
		var account storage.PointAccount
		account, err = s.store.GetPointAccount(r.Context(), req.GuildID, req.UserID)
		result = account
	case "earn":
		result, err = s.store.EarnPoints(r.Context(), req.GuildID, req.UserID, req.EventID, req.Points, req.CooldownSeconds, req.DailyLimit)
	case "lottery":
		var account storage.PointResult
		var won bool
		account, won, err = s.store.DrawLottery(r.Context(), req.GuildID, req.UserID, req.EventID, req.Cost, req.Prize, req.Chance)
		result = map[string]any{"balance": account.Balance, "won": won}
	case "omikuji":
		var fortune string
		fortune, err = s.store.DrawOmikuji(r.Context(), req.GuildID, req.UserID, req.EventID)
		result = map[string]any{"fortune": fortune}
	default:
		writeError(w, 400, "INVALID_REQUEST", "unsupported point action")
		return
	}
	if err != nil {
		writeError(w, 409, "FEATURE_ACTION_FAILED", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "result": result})
}

func (s *Server) starShop(w http.ResponseWriter, r *http.Request, a authContext) {
	if !s.requireGeneralWorker(w, a) {
		return
	}
	if r.Method == http.MethodGet {
		items, err := s.store.ListShopItems(r.Context(), r.URL.Query().Get("guild_id"))
		if err != nil {
			writeError(w, 500, "INTERNAL_ERROR", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "items": items})
		return
	}
	if r.Method == http.MethodPut {
		var req struct {
			GuildID string           `json:"guild_id"`
			Item    storage.ShopItem `json:"item"`
		}
		if !decode(w, r, &req) {
			return
		}
		if err := s.store.UpsertShopItem(r.Context(), req.GuildID, req.Item); err != nil {
			writeError(w, 400, "INVALID_REQUEST", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"success": true})
		return
	}
	var req struct {
		Action  string `json:"action"`
		GuildID string `json:"guild_id"`
		UserID  string `json:"user_id"`
		ItemID  string `json:"item_id"`
		EventID string `json:"event_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Action == "fulfill" {
		if err := s.store.CompleteShopRoleGrant(r.Context(), req.GuildID, req.UserID, req.ItemID); err != nil {
			writeError(w, 409, "SHOP_FULFILLMENT_FAILED", err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"success": true, "role_pending": false})
		return
	}
	if req.Action != "" {
		writeError(w, 400, "INVALID_REQUEST", "unsupported shop action")
		return
	}
	item, account, rolePending, err := s.store.BuyShopItem(r.Context(), req.GuildID, req.UserID, req.ItemID, req.EventID)
	if err != nil {
		writeError(w, 409, "PURCHASE_FAILED", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"success": true, "item": item, "balance": account.Balance, "role_pending": rolePending})
}

// ===== 6. レスポンス用ヘルパーと終了処理 =====
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		writeError(w, 400, "INVALID_REQUEST", err.Error())
		return false
	}
	var trailing any
	if err := d.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, 400, "INVALID_REQUEST", "request body must contain exactly one JSON value")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"protocol_version": 1, "success": false, "error": map[string]any{"code": code, "message": message, "retryable": status >= 500}})
}
func methodNotAllowed(w http.ResponseWriter) {
	writeError(w, 405, "METHOD_NOT_ALLOWED", "method not allowed")
}
func truncate(v string, n int) string {
	// ===== Cut on a rune boundary so persisted error text stays valid UTF-8 =====
	// ===== even when the reason contains multi-byte characters. =====
	for len(v) > n {
		_, size := utf8.DecodeLastRuneInString(v)
		v = v[:len(v)-size]
	}
	return v
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}
