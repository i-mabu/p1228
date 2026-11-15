package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testStorage(t *testing.T) *Storage {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "p1228.db"))
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, bot := range []string{"general", "read_aloud_a", "read_aloud_b"} {
		if err := store.UpsertBot(context.Background(), bot, bot, "application-"+bot); err != nil {
			t.Fatalf("UpsertBot(%s): %v", bot, err)
		}
	}
	return store
}

func makeEvent(botID, eventID, ordering string, seq int64) *Event {
	payload, _ := json.Marshal(map[string]any{"id": eventID, "content": "hello"})
	return &Event{ProtocolVersion: 1, EventID: eventID, CorrelationID: "corr-" + eventID, BotID: botID,
		EventType: "MESSAGE_CREATE", OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		OrderingKey: ordering, SessionID: "session-1", Sequence: seq, Payload: payload}
}

func TestRecordClaimAckAndOrdering(t *testing.T) {
	ctx := context.Background()
	store := testStorage(t)
	if err := store.RecordDispatch(ctx, Session{BotID: "general", SessionID: "session-1", Sequence: 1}, makeEvent("general", "evt-1", "general:guild:channel", 1)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := store.RecordDispatch(ctx, Session{BotID: "general", SessionID: "session-1", Sequence: 2}, makeEvent("general", "evt-2", "general:guild:channel", 2)); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimEvent(ctx, "general", "worker-1", 30, 8)
	if err != nil || first == nil {
		t.Fatalf("first claim = %#v, err=%v", first, err)
	}
	second, err := store.ClaimEvent(ctx, "general", "worker-1", 30, 8)
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Fatal("later event with same ordering_key bypassed unacked earlier event")
	}
	if err := store.AckEvent(ctx, "general", first.Event.EventID, first.LeaseToken); err != nil {
		t.Fatal(err)
	}
	second, err = store.ClaimEvent(ctx, "general", "worker-1", 30, 8)
	if err != nil || second == nil || second.Event.EventID != "evt-2" {
		t.Fatalf("second claim = %#v, err=%v", second, err)
	}
	if err := store.AckEvent(ctx, "read_aloud_a", second.Event.EventID, second.LeaseToken); err == nil {
		t.Fatal("cross-bot ACK must be rejected")
	}
	if err := store.AckEvent(ctx, "general", second.Event.EventID, second.LeaseToken); err != nil {
		t.Fatal(err)
	}
}

func TestNackMovesEventToDeadLetter(t *testing.T) {
	ctx := context.Background()
	store := testStorage(t)
	if err := store.RecordDispatch(ctx, Session{BotID: "general", SessionID: "s", Sequence: 7}, makeEvent("general", "evt-dead", "k", 7)); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimEvent(ctx, "general", "w", 30, 1)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %#v", err, claimed)
	}
	if err := store.NackEvent(ctx, "general", claimed.Event.EventID, claimed.LeaseToken, "test failure", 1); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM event_dead_letters WHERE event_id='evt-dead'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("dead letter count=%d, want 1", count)
	}
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM event_inbox WHERE event_id='evt-dead' AND status='dead'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("dead inbox count=%d, want 1", count)
	}
}

func TestSharedReadAloudSettingsAndDictionary(t *testing.T) {
	ctx := context.Background()
	store := testStorage(t)
	_, revision, err := store.GetReadAloudSettings(ctx, "guild", "123")
	if err != nil || revision != 0 {
		t.Fatalf("default settings revision=%d err=%v", revision, err)
	}
	settings := json.RawMessage(`{"enabled":true,"speed":1.2,"volume":0.8,"speaker_id":3}`)
	revision, err = store.PutReadAloudSettings(ctx, "guild", "123", settings, "read_aloud_a")
	if err != nil || revision != 1 {
		t.Fatalf("write settings revision=%d err=%v", revision, err)
	}
	loaded, revision, err := store.GetReadAloudSettings(ctx, "guild", "123")
	if err != nil || revision != 1 || string(loaded) != string(settings) {
		t.Fatalf("settings did not roundtrip: %s rev=%d err=%v", loaded, revision, err)
	}
	if err := store.PutDictionaryEntry(ctx, "guild", "123", "AI", "えーあい", "read_aloud_a"); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListDictionary(ctx, "guild", "123")
	if err != nil || len(entries) != 1 || entries[0].Reading != "えーあい" {
		t.Fatalf("dictionary entries=%#v err=%v", entries, err)
	}
}

func TestLatestWorkerHeartbeatsReturnsNewestPerBot(t *testing.T) {
	ctx := context.Background()
	store := testStorage(t)
	identity := WorkerIdentity{WorkerID: "general", Role: "general", BotID: "general"}
	if err := store.RegisterWorker(ctx, "instance-old", identity); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	if _, err := store.DB().ExecContext(ctx, `UPDATE worker_instances SET last_heartbeat=? WHERE instance_id=?`, old, "instance-old"); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterWorker(ctx, "instance-new", identity); err != nil {
		t.Fatal(err)
	}
	got, err := store.LatestWorkerHeartbeats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["general"]; !ok {
		t.Fatal("latest heartbeat for general bot was not returned")
	}
	if time.Since(got["general"]) > 5*time.Second {
		t.Fatalf("latest heartbeat is unexpectedly old: %s", got["general"])
	}
	if _, ok := got["read_aloud_a"]; ok {
		t.Fatal("heartbeat returned for an unregistered bot")
	}
}

func TestWorkerIdentityAndRetention(t *testing.T) {
	ctx := context.Background()
	store := testStorage(t)
	identity := WorkerIdentity{WorkerID: "general", Role: "general", BotID: "general"}
	if err := store.RegisterWorker(ctx, "instance-1", identity); err != nil {
		t.Fatal(err)
	}
	ok, err := store.IsWorkerRegistered(ctx, "instance-1", "general", "general")
	if err != nil || !ok {
		t.Fatalf("registered=%v err=%v", ok, err)
	}
	if err := store.Heartbeat(ctx, "instance-1", "general", "general"); err != nil {
		t.Fatal(err)
	}
	if err := store.Heartbeat(ctx, "instance-1", "read_aloud_a", "read_aloud_a"); err == nil {
		t.Fatal("cross-worker heartbeat must be rejected")
	}

	if err := store.RecordDispatch(ctx, Session{BotID: "general", SessionID: "s", Sequence: 1}, makeEvent("general", "evt-old", "k", 1)); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimEvent(ctx, "general", "instance-1", 30, 8)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %#v", err, claimed)
	}
	if err := store.AckEvent(ctx, "general", claimed.Event.EventID, claimed.LeaseToken); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`UPDATE event_inbox SET received_at=? WHERE event_id='evt-old'`, old); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.PruneExpiredEvents(ctx, 24)
	if err != nil || deleted != 1 {
		t.Fatalf("pruned=%d err=%v", deleted, err)
	}
}

func TestOperationIDCannotBeReplayed(t *testing.T) {
	ctx := context.Background()
	store := testStorage(t)
	created, err := store.CreateOperation(ctx, "op-1", "general", "send_message")
	if err != nil || !created {
		t.Fatalf("first reservation created=%v err=%v", created, err)
	}
	if err := store.UpdateOperation(ctx, "op-1", "general", "send_message", "unknown", "", "timeout"); err != nil {
		t.Fatal(err)
	}
	created, err = store.CreateOperation(ctx, "op-1", "general", "send_message")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("duplicate operation_id must not be executed again")
	}
}

func TestRetentionDeadLettersExpiredUnackedPayload(t *testing.T) {
	ctx := context.Background()
	store := testStorage(t)
	if err := store.RecordDispatch(ctx, Session{BotID: "general", SessionID: "s", Sequence: 3}, makeEvent("general", "evt-pending-old", "k", 3)); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339Nano)
	if _, err := store.DB().Exec(`UPDATE event_inbox SET received_at=? WHERE event_id='evt-pending-old'`, old); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.PruneExpiredEvents(ctx, 24)
	if err != nil || deleted != 1 {
		t.Fatalf("pruned=%d err=%v", deleted, err)
	}
	var reason, payload string
	if err := store.DB().QueryRow(`SELECT reason,payload_json FROM event_dead_letters WHERE event_id='evt-pending-old'`).Scan(&reason, &payload); err != nil {
		t.Fatal(err)
	}
	if reason == "" || payload != "{}" {
		t.Fatalf("expected metadata-only dead letter, reason=%q payload=%q", reason, payload)
	}
}

func TestStarFeatureSettingsBlacklistAndSticky(t *testing.T) {
	ctx := context.Background()
	store := testStorage(t)
	value := json.RawMessage(`{"channel_id":"channel-1","message":"Welcome {username}"}`)
	if err := store.SetGuildFeatureSetting(ctx, "guild-1", "welcome", value); err != nil {
		t.Fatal(err)
	}
	settings, err := store.GetGuildFeatureSettings(ctx, "guild-1")
	if err != nil || string(settings["welcome"]) != string(value) {
		t.Fatalf("settings=%s err=%v", settings["welcome"], err)
	}
	if err := store.AddGuildBlacklist(ctx, "guild-1", "user-1", "blocked"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddGuildBlacklist(ctx, "guild-1", "user-2", strings.Repeat("理", 500)); err != nil {
		t.Fatalf("500-character Unicode blacklist reason rejected: %v", err)
	}
	if err := store.AddGuildBlacklist(ctx, "guild-1", "user-3", strings.Repeat("理", 501)); err == nil {
		t.Fatal("expected 501-character blacklist reason to be rejected")
	}
	blocked, err := store.IsGuildBlacklisted(ctx, "guild-1", "user-1")
	if err != nil || !blocked {
		t.Fatalf("blacklisted=%v err=%v", blocked, err)
	}
	if err := store.RemoveGuildBlacklist(ctx, "guild-1", "user-1"); err != nil {
		t.Fatal(err)
	}
	blocked, err = store.IsGuildBlacklisted(ctx, "guild-1", "user-1")
	if err != nil || blocked {
		t.Fatalf("blacklisted after removal=%v err=%v", blocked, err)
	}
	sticky := StickyMessage{GuildID: "guild-1", ChannelID: "channel-1", MessageID: "message-1", Content: "Pinned"}
	if err := store.SetStickyMessage(ctx, sticky); err != nil {
		t.Fatal(err)
	}
	found, err := store.GetStickyMessage(ctx, sticky.GuildID, sticky.ChannelID)
	if err != nil || found == nil || found.MessageID != sticky.MessageID {
		t.Fatalf("sticky=%#v err=%v", found, err)
	}
	if err := store.UpdateStickyMessageID(ctx, sticky.GuildID, sticky.ChannelID, sticky.MessageID, "message-2"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateStickyMessageID(ctx, sticky.GuildID, sticky.ChannelID, sticky.MessageID, "message-3"); err == nil {
		t.Fatal("expected stale sticky message ID update to be rejected")
	}
	found, err = store.GetStickyMessage(ctx, sticky.GuildID, sticky.ChannelID)
	if err != nil || found == nil || found.MessageID != "message-2" {
		t.Fatalf("updated sticky=%#v err=%v", found, err)
	}
}

func TestPointEarningLotteryShopAndOmikujiAreIdempotent(t *testing.T) {
	ctx := context.Background()
	store := testStorage(t)
	first, err := store.EarnPoints(ctx, "guild-1", "user-1", "message-1", 1, 0, 100)
	if err != nil || first.Earned != 1 || first.Balance != 1 {
		t.Fatalf("first earn=%#v err=%v", first, err)
	}
	repeated, err := store.EarnPoints(ctx, "guild-1", "user-1", "message-1", 1, 0, 100)
	if err != nil || repeated.Earned != 0 || repeated.Balance != 1 {
		t.Fatalf("repeated earn=%#v err=%v", repeated, err)
	}
	capped, err := store.EarnPoints(ctx, "guild-1", "user-1", "message-2", 10, 0, 2)
	if err != nil || capped.Earned != 1 {
		t.Fatalf("daily cap award=%#v err=%v", capped, err)
	}
	account, err := store.GetPointAccount(ctx, "guild-1", "user-1")
	if err != nil || account.Balance != 2 {
		t.Fatalf("daily cap account=%#v err=%v", account, err)
	}
	if err := store.UpsertShopItem(ctx, "guild-1", ShopItem{ItemID: "color", Name: strings.Repeat("青", 100), Price: 1, RoleID: "role-1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertShopItem(ctx, "guild-1", ShopItem{ItemID: "bad_id", Name: "Invalid ID", Price: 1, RoleID: "role-1"}); err == nil {
		t.Fatal("expected shop item ID outside the documented character set to be rejected")
	}
	item, accountAfterPurchase, rolePending, err := store.BuyShopItem(ctx, "guild-1", "user-1", "color", "interaction-shop")
	if err != nil || item.RoleID != "role-1" || accountAfterPurchase.Balance != 1 || !rolePending {
		t.Fatalf("purchase=%#v account=%#v pending=%v err=%v", item, accountAfterPurchase, rolePending, err)
	}
	_, repeatedPurchase, repeatedPending, err := store.BuyShopItem(ctx, "guild-1", "user-1", "color", "interaction-shop")
	if err != nil || repeatedPurchase.Balance != 1 || !repeatedPending {
		t.Fatalf("repeated purchase balance=%d pending=%v err=%v", repeatedPurchase.Balance, repeatedPending, err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE shop_items SET enabled=0,role_id='role-2' WHERE guild_id='guild-1' AND item_id='color'`); err != nil {
		t.Fatal(err)
	}
	retriedItem, retriedPurchase, retryPending, err := store.BuyShopItem(ctx, "guild-1", "user-1", "color", "interaction-shop-retry")
	if err != nil || retriedItem.RoleID != "role-1" || retriedPurchase.Balance != 1 || !retryPending {
		t.Fatalf("pending fulfillment retry item=%#v balance=%d pending=%v err=%v", retriedItem, retriedPurchase.Balance, retryPending, err)
	}
	if err := store.CompleteShopRoleGrant(ctx, "guild-1", "user-1", "color"); err != nil {
		t.Fatal(err)
	}
	if err := store.CompleteShopRoleGrant(ctx, "guild-1", "user-1", "color"); err != nil {
		t.Fatalf("completion should be idempotent: %v", err)
	}
	if _, _, _, err := store.BuyShopItem(ctx, "guild-1", "user-1", "color", "interaction-shop-after-fulfillment"); err == nil || err.Error() != "shop item already owned" {
		t.Fatalf("fulfilled item should not be purchased or re-granted: %v", err)
	}
	fortune, err := store.DrawOmikuji(ctx, "guild-1", "user-1", "interaction-fortune")
	if err != nil || fortune == "" {
		t.Fatalf("fortune=%q err=%v", fortune, err)
	}
	repeatedFortune, err := store.DrawOmikuji(ctx, "guild-1", "user-1", "interaction-fortune")
	if err != nil || repeatedFortune != fortune {
		t.Fatalf("repeated fortune=%q err=%v", repeatedFortune, err)
	}
	if _, err := store.DrawOmikuji(ctx, "guild-1", "user-1", "interaction-fortune-2"); err == nil {
		t.Fatal("expected second daily fortune draw to be rejected")
	}
}

func TestShopFulfillmentMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p1228.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopening database should not reapply ALTER TABLE migration: %v", err)
	}
	defer second.Close()
	var status string
	if err := second.DB().QueryRow(`SELECT role_status FROM user_inventory LIMIT 1`).Scan(&status); err != nil && !strings.Contains(err.Error(), "no rows") {
		t.Fatalf("role_status column missing after reopen: %v", err)
	}
}

func TestLegacyShopInventoryIsBackfilledAsFulfilled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(initialMigration); err != nil {
		t.Fatalf("apply migration 1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(1,?)`, "2026-10-01T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(starFeaturesMigration); err != nil {
		t.Fatalf("apply migration 2: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(2,?)`, "2026-10-01T00:00:01.000000000Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_inventory(guild_id,user_id,item_id,purchased_at) VALUES('guild','legacy-user','legacy-item',?)`, "2026-11-01T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(shopRoleFulfillmentMigration); err != nil {
		t.Fatalf("apply migration 3: %v", err)
	}
	migrationThreeAt := "2026-11-10T00:00:00.000000000Z"
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(3,?)`, migrationThreeAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO user_inventory(guild_id,user_id,item_id,purchased_at,role_status,role_id) VALUES('guild','new-user','new-item',?,'pending','role-new')`, "2026-11-11T00:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() applies migration 4: %v", err)
	}
	defer store.Close()

	var legacyStatus, newStatus string
	if err := store.DB().QueryRow(`SELECT role_status FROM user_inventory WHERE user_id='legacy-user'`).Scan(&legacyStatus); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRow(`SELECT role_status FROM user_inventory WHERE user_id='new-user'`).Scan(&newStatus); err != nil {
		t.Fatal(err)
	}
	if legacyStatus != "fulfilled" {
		t.Fatalf("legacy purchase status=%q, want fulfilled", legacyStatus)
	}
	if newStatus != "pending" {
		t.Fatalf("post-migration purchase status=%q, want pending", newStatus)
	}
}

func TestBuyShopItemWithoutPointAccountReportsInsufficientPoints(t *testing.T) {
	ctx := context.Background()
	store := testStorage(t)
	if err := store.UpsertShopItem(ctx, "guild-1", ShopItem{ItemID: "color", Name: "Color", Price: 10, RoleID: "role-1"}); err != nil {
		t.Fatal(err)
	}
	// The buyer has never earned points, so no point_accounts row exists.
	// The purchase must fail with "insufficient points", not a raw
	// sql.ErrNoRows that the Worker presented as "item not found".
	_, _, _, err := store.BuyShopItem(ctx, "guild-1", "user-new", "color", "interaction-buy-new-user")
	if err == nil || err.Error() != "insufficient points" {
		t.Fatalf("buy without point account err=%v, want insufficient points", err)
	}
}

func TestBuyShopItemUnknownItemReportsNotFound(t *testing.T) {
	ctx := context.Background()
	store := testStorage(t)
	_, _, _, err := store.BuyShopItem(ctx, "guild-1", "user-1", "missing-item", "interaction-buy-missing")
	if err == nil || err.Error() != "shop item not found" {
		t.Fatalf("buy unknown item err=%v, want shop item not found", err)
	}
}
