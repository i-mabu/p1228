package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/001_initial.sql
var initialMigration string

//go:embed migrations/002_star_features.sql
var starFeaturesMigration string

//go:embed migrations/003_shop_role_fulfillment.sql
var shopRoleFulfillmentMigration string

//go:embed migrations/004_backfill_legacy_shop_fulfillment.sql
var legacyShopFulfillmentMigration string

type Storage struct {
	db      *sql.DB
	writeMu sync.Mutex
}

type Event struct {
	ProtocolVersion int             `json:"protocol_version"`
	EventID         string          `json:"event_id"`
	CorrelationID   string          `json:"correlation_id"`
	BotID           string          `json:"bot_id"`
	BotUserID       string          `json:"bot_user_id,omitempty"`
	EventType       string          `json:"event_type"`
	OccurredAt      string          `json:"occurred_at"`
	OrderingKey     string          `json:"ordering_key"`
	SessionID       string          `json:"session_id,omitempty"`
	Sequence        int64           `json:"sequence"`
	Payload         json.RawMessage `json:"payload"`
	LeaseToken      string          `json:"lease_token,omitempty"`
	Attempts        int             `json:"attempts,omitempty"`
	Ephemeral       bool            `json:"ephemeral,omitempty"`
}

type Session struct {
	BotID, SessionID, ResumeURL, BotUserID string
	Sequence                               int64
}
type ClaimedEvent struct {
	Event      Event
	LeaseToken string
	Attempts   int
}
type DictionaryEntry struct {
	EntryID string `json:"entry_id"`
	Word    string `json:"word"`
	Reading string `json:"reading"`
}

func Open(path string) (*Storage, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000", "PRAGMA synchronous=FULL"} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite pragma: %w", err)
		}
	}
	s := &Storage{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Storage) Close() error { return s.db.Close() }

func (s *Storage) UpsertBot(ctx context.Context, botID, displayName, applicationID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO bots(bot_id,display_name,application_id) VALUES(?,?,?) ON CONFLICT(bot_id) DO UPDATE SET display_name=excluded.display_name,application_id=excluded.application_id`, botID, displayName, applicationID)
	return err
}

func (s *Storage) PruneExpiredEvents(ctx context.Context, retentionHours int) (int64, error) {
	if retentionHours < 1 {
		return 0, errors.New("event retention must be at least one hour")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	cutoff := formatTime(time.Now().Add(-time.Duration(retentionHours) * time.Hour))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Events that never reached ACK by the payload-retention deadline are dead-lettered
	// without preserving message bodies indefinitely. The audit record remains, payload is scrubbed.
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO event_dead_letters(event_id,bot_id,event_type,reason,attempts,payload_json,created_at) SELECT event_id,bot_id,event_type,'event expired before processing/ACK by payload retention policy',attempts,payload_json,received_at FROM event_inbox WHERE received_at < ? AND status IN ('pending','processing')`, cutoff); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE event_dead_letters SET payload_json='{}' WHERE created_at < ?`, cutoff); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM event_inbox WHERE received_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	deleted, _ := res.RowsAffected()
	if _, err = tx.ExecContext(ctx, `DELETE FROM event_idempotency_keys WHERE expires_at < ?`, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return 0, err
	}
	return deleted, tx.Commit()
}

func (s *Storage) DB() *sql.DB { return s.db }

func (s *Storage) migrate(ctx context.Context) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, initialMigration); err != nil {
		return fmt.Errorf("apply migration 1: %w", err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(1, ?)", now()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, starFeaturesMigration); err != nil {
		return fmt.Errorf("apply migration 2: %w", err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO schema_migrations(version, applied_at) VALUES(2, ?)", now()); err != nil {
		return err
	}
	var migrationThreeApplied int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version=3").Scan(&migrationThreeApplied); err != nil {
		return err
	}
	if migrationThreeApplied == 0 {
		if _, err = tx.ExecContext(ctx, shopRoleFulfillmentMigration); err != nil {
			return fmt.Errorf("apply migration 3: %w", err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(3, ?)", now()); err != nil {
			return err
		}
	}
	var migrationFourApplied int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version=4").Scan(&migrationFourApplied); err != nil {
		return err
	}
	if migrationFourApplied == 0 {
		if _, err = tx.ExecContext(ctx, legacyShopFulfillmentMigration); err != nil {
			return fmt.Errorf("apply migration 4: %w", err)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version, applied_at) VALUES(4, ?)", now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func randomID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b[:])
}

// Fixed-width UTC timestamps sort lexicographically in the same order as time.
func formatTime(value time.Time) string { return value.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func now() string                       { return formatTime(time.Now()) }

// RecordDispatch commits the inbox record and sequence checkpoint atomically.
func (s *Storage) RecordDispatch(ctx context.Context, sess Session, ev *Event) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO bots(bot_id, display_name, application_id, bot_user_id) VALUES(?,?,?,?) ON CONFLICT(bot_id) DO UPDATE SET bot_user_id=CASE WHEN excluded.bot_user_id<>'' THEN excluded.bot_user_id ELSE bots.bot_user_id END`, sess.BotID, sess.BotID, "", sess.BotUserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO gateway_sessions(bot_id,session_id,resume_gateway_url,last_sequence,bot_user_id,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(bot_id) DO UPDATE SET session_id=CASE WHEN excluded.session_id<>'' THEN excluded.session_id ELSE gateway_sessions.session_id END,resume_gateway_url=CASE WHEN excluded.resume_gateway_url<>'' THEN excluded.resume_gateway_url ELSE gateway_sessions.resume_gateway_url END,last_sequence=MAX(gateway_sessions.last_sequence,excluded.last_sequence),bot_user_id=CASE WHEN excluded.bot_user_id<>'' THEN excluded.bot_user_id ELSE gateway_sessions.bot_user_id END,updated_at=excluded.updated_at`, sess.BotID, sess.SessionID, sess.ResumeURL, sess.Sequence, sess.BotUserID, now()); err != nil {
		return err
	}
	if ev != nil {
		_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO event_inbox(event_id,correlation_id,bot_id,session_id,sequence,event_type,ordering_key,occurred_at,received_at,bot_user_id,payload_json,status) VALUES(?,?,?,?,?,?,?,?,?,?,?,'pending')`, ev.EventID, ev.CorrelationID, ev.BotID, ev.SessionID, ev.Sequence, ev.EventType, ev.OrderingKey, ev.OccurredAt, now(), ev.BotUserID, string(ev.Payload))
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Storage) LoadSession(ctx context.Context, botID string) (Session, error) {
	var v Session
	err := s.db.QueryRowContext(ctx, `SELECT bot_id,session_id,resume_gateway_url,last_sequence,bot_user_id FROM gateway_sessions WHERE bot_id=?`, botID).Scan(&v.BotID, &v.SessionID, &v.ResumeURL, &v.Sequence, &v.BotUserID)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{BotID: botID, Sequence: -1}, nil
	}
	return v, err
}

func (s *Storage) ClearSession(ctx context.Context, botID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `UPDATE gateway_sessions SET session_id='',resume_gateway_url='',last_sequence=-1,updated_at=? WHERE bot_id=?`, now(), botID)
	return err
}

func (s *Storage) ClaimEvent(ctx context.Context, botID, instanceID string, leaseSeconds, maxAttempts int) (*ClaimedEvent, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	n := time.Now().UTC()
	nstr := formatTime(n)
	until := formatTime(n.Add(time.Duration(leaseSeconds) * time.Second))
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO event_dead_letters(event_id,bot_id,event_type,reason,attempts,payload_json,created_at) SELECT event_id,bot_id,event_type,'lease expired beyond retry limit',attempts,payload_json,? FROM event_inbox WHERE bot_id=? AND status='processing' AND lease_until<=? AND attempts>=?`, nstr, botID, nstr, maxAttempts); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE event_inbox SET status='dead',lease_until=NULL,lease_owner=NULL,lease_token=NULL,last_error='lease expired beyond retry limit' WHERE bot_id=? AND status='processing' AND lease_until<=? AND attempts>=?`, botID, nstr, maxAttempts); err != nil {
		return nil, err
	}
	var e Event
	var payload string
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT e.event_id,e.correlation_id,e.bot_id,e.session_id,e.sequence,e.event_type,e.ordering_key,e.occurred_at,e.bot_user_id,e.payload_json,e.attempts FROM event_inbox e WHERE e.bot_id=? AND e.status IN ('pending','processing') AND (e.not_before IS NULL OR e.not_before<=?) AND (e.lease_until IS NULL OR e.lease_until<=?) AND e.attempts<? AND NOT EXISTS (SELECT 1 FROM event_inbox p WHERE p.bot_id=e.bot_id AND p.ordering_key=e.ordering_key AND p.status NOT IN ('acked','dead') AND p.received_at<e.received_at) ORDER BY CASE WHEN e.event_type='INTERACTION_CREATE' THEN 0 ELSE 1 END, e.received_at LIMIT 1`, botID, nstr, nstr, maxAttempts).Scan(&e.EventID, &e.CorrelationID, &e.BotID, &e.SessionID, &e.Sequence, &e.EventType, &e.OrderingKey, &e.OccurredAt, &e.BotUserID, &payload, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	token := randomID("lease_")
	if _, err := tx.ExecContext(ctx, `UPDATE event_inbox SET status='processing',attempts=attempts+1,lease_until=?,lease_owner=?,lease_token=?,not_before=NULL WHERE event_id=?`, until, instanceID, token, e.EventID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	e.ProtocolVersion = 1
	e.Payload = json.RawMessage(payload)
	e.LeaseToken = token
	e.Attempts = attempts + 1
	return &ClaimedEvent{Event: e, LeaseToken: token, Attempts: attempts + 1}, nil
}

func (s *Storage) AckEvent(ctx context.Context, botID, eventID, token string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE event_inbox SET status='acked',lease_until=NULL,lease_owner=NULL,lease_token=NULL,last_error=NULL WHERE bot_id=? AND event_id=? AND status='processing' AND lease_token=?`, botID, eventID, token)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("event lease is missing, expired or owned by another worker")
	}
	return nil
}

func (s *Storage) NackEvent(ctx context.Context, botID, eventID, token, reason string, maxAttempts int) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var eventBotID, eventType, payload string
	var attempts int
	if err := tx.QueryRowContext(ctx, `SELECT bot_id,event_type,payload_json,attempts FROM event_inbox WHERE bot_id=? AND event_id=? AND status='processing' AND lease_token=?`, botID, eventID, token).Scan(&eventBotID, &eventType, &payload, &attempts); err != nil {
		return err
	}
	if attempts >= maxAttempts {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO event_dead_letters(event_id,bot_id,event_type,reason,attempts,payload_json,created_at) VALUES(?,?,?,?,?,?,?)`, eventID, eventBotID, eventType, reason, attempts, payload, now()); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE event_inbox SET status='dead',lease_until=NULL,lease_owner=NULL,lease_token=NULL,last_error=? WHERE bot_id=? AND event_id=?`, reason, botID, eventID)
	} else {
		delay := time.Duration(1<<min(attempts, 8)) * time.Second
		_, err = tx.ExecContext(ctx, `UPDATE event_inbox SET status='pending',not_before=?,lease_until=NULL,lease_owner=NULL,lease_token=NULL,last_error=? WHERE bot_id=? AND event_id=?`, formatTime(time.Now().Add(delay)), reason, botID, eventID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Storage) RegisterWorker(ctx context.Context, instanceID string, w WorkerIdentity) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var existingWorker, existingBot string
	err := s.db.QueryRowContext(ctx, `SELECT worker_id,bot_id FROM worker_instances WHERE instance_id=?`, instanceID).Scan(&existingWorker, &existingBot)
	if err == nil && (existingWorker != w.WorkerID || existingBot != w.BotID) {
		return errors.New("worker instance id is already registered to another worker")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO worker_instances(instance_id,worker_id,role,bot_id,started_at,last_heartbeat,status) VALUES(?,?,?,?,?,?, 'online') ON CONFLICT(instance_id) DO UPDATE SET last_heartbeat=excluded.last_heartbeat,status='online'`, instanceID, w.WorkerID, w.Role, w.BotID, now(), now())
	return err
}

type WorkerIdentity struct{ WorkerID, Role, BotID string }

func (s *Storage) IsWorkerRegistered(ctx context.Context, instanceID, workerID, botID string) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM worker_instances WHERE instance_id=? AND worker_id=? AND bot_id=? AND status='online'`, instanceID, workerID, botID).Scan(&count)
	return count == 1, err
}

func (s *Storage) Heartbeat(ctx context.Context, instanceID, workerID, botID string) error {
	// Take the same write lock as every other mutating method so heartbeat
	// updates stay serialized with registrations and event claims.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE worker_instances SET last_heartbeat=?,status='online' WHERE instance_id=? AND worker_id=? AND bot_id=?`, now(), instanceID, workerID, botID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("worker instance is not registered for this identity")
	}
	return nil
}

// LatestWorkerHeartbeats returns the most recent heartbeat for each bot.
// Multiple instances of a worker are tolerated; the newest one represents
// the bot's liveness.
func (s *Storage) LatestWorkerHeartbeats(ctx context.Context) (map[string]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT bot_id, MAX(last_heartbeat) FROM worker_instances WHERE status='online' GROUP BY bot_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]time.Time)
	for rows.Next() {
		var botID, raw string
		if err := rows.Scan(&botID, &raw); err != nil {
			return nil, err
		}
		last, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return nil, fmt.Errorf("parse worker heartbeat for %s: %w", botID, err)
		}
		out[botID] = last
	}
	return out, rows.Err()
}

// CreateOperation reserves operation_id exactly once before any external side effect.
// A duplicate request is rejected rather than replayed because a timeout may have an unknown outcome.
func (s *Storage) CreateOperation(ctx context.Context, id, botID, operation string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO discord_operations(operation_id,bot_id,operation,status,created_at,updated_at) VALUES(?,?,?,'pending',?,?)`, id, botID, operation, now(), now())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *Storage) GetOperationResult(ctx context.Context, id, botID, operation string) (string, string, error) {
	var status, responseRef string
	err := s.db.QueryRowContext(ctx, `SELECT status,COALESCE(response_ref,'') FROM discord_operations WHERE operation_id=? AND bot_id=? AND operation=?`, id, botID, operation).Scan(&status, &responseRef)
	return status, responseRef, err
}

func (s *Storage) UpdateOperation(ctx context.Context, id, botID, operation, status, responseRef, errorMessage string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO discord_operations(operation_id,bot_id,operation,status,response_ref,error_message,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(operation_id) DO UPDATE SET status=excluded.status,response_ref=excluded.response_ref,error_message=excluded.error_message,updated_at=excluded.updated_at`, id, botID, operation, status, responseRef, errorMessage, now(), now())
	return err
}

func validScope(scope, scopeID string) bool {
	return (scope == "global" && scopeID == "global") || ((scope == "guild" || scope == "user") && strings.TrimSpace(scopeID) != "")
}
func (s *Storage) GetReadAloudSettings(ctx context.Context, scope, scopeID string) (json.RawMessage, int64, error) {
	if !validScope(scope, scopeID) {
		return nil, 0, errors.New("invalid scope")
	}
	var raw string
	var rev int64
	err := s.db.QueryRowContext(ctx, `SELECT settings_json,revision FROM read_aloud_settings WHERE scope=? AND scope_id=?`, scope, scopeID).Scan(&raw, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return json.RawMessage(`{"enabled":false,"speed":1.0,"volume":1.0,"speaker_id":1}`), 0, nil
	}
	return json.RawMessage(raw), rev, err
}
func (s *Storage) PutReadAloudSettings(ctx context.Context, scope, scopeID string, settings json.RawMessage, actor string) (int64, error) {
	if !validScope(scope, scopeID) {
		return 0, errors.New("invalid scope")
	}
	if !json.Valid(settings) {
		return 0, errors.New("settings must be valid JSON")
	}
	var validated struct {
		Enabled   *bool    `json:"enabled"`
		Speed     *float64 `json:"speed"`
		Volume    *float64 `json:"volume"`
		SpeakerID *int     `json:"speaker_id"`
	}
	if err := json.Unmarshal(settings, &validated); err != nil || validated.Enabled == nil || validated.Speed == nil || validated.Volume == nil || validated.SpeakerID == nil {
		return 0, errors.New("settings require enabled, speed, volume and speaker_id")
	}
	if math.IsNaN(*validated.Speed) || math.IsInf(*validated.Speed, 0) || *validated.Speed < 0.5 || *validated.Speed > 2.0 {
		return 0, errors.New("speed must be between 0.5 and 2.0")
	}
	if math.IsNaN(*validated.Volume) || math.IsInf(*validated.Volume, 0) || *validated.Volume < 0.0 || *validated.Volume > 2.0 {
		return 0, errors.New("volume must be between 0.0 and 2.0")
	}
	if *validated.SpeakerID < 0 || *validated.SpeakerID > 100000 {
		return 0, errors.New("speaker_id must be between 0 and 100000")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO read_aloud_settings(scope,scope_id,settings_json,revision,updated_at) VALUES(?,?,?,1,?) ON CONFLICT(scope,scope_id) DO UPDATE SET settings_json=excluded.settings_json,revision=read_aloud_settings.revision+1,updated_at=excluded.updated_at`, scope, scopeID, string(settings), now()); err != nil {
		return 0, err
	}
	var rev int64
	if err = tx.QueryRowContext(ctx, `SELECT revision FROM read_aloud_settings WHERE scope=? AND scope_id=?`, scope, scopeID).Scan(&rev); err != nil {
		return 0, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(actor,action,target,details_json,created_at) VALUES(?,?,?,?,?)`, actor, "read_aloud_settings.update", scope+":"+scopeID, fmt.Sprintf(`{"revision":%d}`, rev), now()); err != nil {
		return 0, err
	}
	return rev, tx.Commit()
}
func (s *Storage) ListDictionary(ctx context.Context, scope, scopeID string) ([]DictionaryEntry, error) {
	if scope == "" {
		scope = "global"
	}
	if scopeID == "" {
		scopeID = "global"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT entry_id,word,reading FROM dictionary_entries WHERE scope=? AND scope_id=? ORDER BY word COLLATE NOCASE`, scope, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DictionaryEntry{}
	for rows.Next() {
		var e DictionaryEntry
		if err := rows.Scan(&e.EntryID, &e.Word, &e.Reading); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Storage) PutDictionaryEntry(ctx context.Context, scope, scopeID, word, reading, actor string) error {
	if scope == "" {
		scope = "global"
	}
	if scopeID == "" {
		scopeID = "global"
	}
	if !validScope(scope, scopeID) || strings.TrimSpace(word) == "" || strings.TrimSpace(reading) == "" || len(word) > 512 || len(reading) > 512 {
		return errors.New("valid scope, word and reading are required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	newDictionaryID := randomID("dict_")
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO dictionaries(dictionary_id,scope,scope_id,revision,created_at,updated_at) VALUES(?,?,?,1,?,?)`, newDictionaryID, scope, scopeID, now(), now()); err != nil {
		return err
	}
	var dictionaryID string
	if err := tx.QueryRowContext(ctx, `SELECT dictionary_id FROM dictionaries WHERE scope=? AND scope_id=?`, scope, scopeID).Scan(&dictionaryID); err != nil {
		return err
	}
	entryID := randomID("entry_")
	if _, err := tx.ExecContext(ctx, `INSERT INTO dictionary_entries(entry_id,dictionary_id,scope,scope_id,word,reading,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(scope,scope_id,word) DO UPDATE SET reading=excluded.reading,updated_at=excluded.updated_at`, entryID, dictionaryID, scope, scopeID, word, reading, now(), now()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE dictionaries SET revision=revision+1,updated_at=? WHERE dictionary_id=?`, now(), dictionaryID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(actor,action,target,details_json,created_at) VALUES(?,?,?,?,?)`, actor, "dictionary.upsert", scope+":"+scopeID+":"+word, `{"updated":true}`, now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Storage) CountByStatus(ctx context.Context, botID string) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status,COUNT(*) FROM event_inbox WHERE bot_id=? GROUP BY status`, botID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		m[k] = n
	}
	return m, rows.Err()
}
func (s *Storage) DeadLetterIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT event_id FROM event_dead_letters ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, rows.Err()
}
