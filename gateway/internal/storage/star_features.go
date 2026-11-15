package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
	"unicode/utf8"
)

// ===== 1. 型定義 =====
type StickyMessage struct {
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
	Content   string `json:"content"`
}

type ShopItem struct {
	ItemID string `json:"item_id"`
	Name   string `json:"name"`
	Price  int64  `json:"price"`
	RoleID string `json:"role_id"`
}

type PointAccount struct {
	Balance int64 `json:"balance"`
}

type PointResult struct {
	Balance int64 `json:"balance"`
	Earned  int64 `json:"earned"`
}

// ===== 2. ギルド設定とブラックリスト =====
func (s *Storage) GetGuildFeatureSettings(ctx context.Context, guildID string) (map[string]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT setting_key, setting_json FROM guild_feature_settings WHERE guild_id=?`, guildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := make(map[string]json.RawMessage)
	for rows.Next() {
		var key, raw string
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		settings[key] = json.RawMessage(raw)
	}
	return settings, rows.Err()
}

func (s *Storage) SetGuildFeatureSetting(ctx context.Context, guildID, key string, value json.RawMessage) error {
	if strings.TrimSpace(guildID) == "" || strings.TrimSpace(key) == "" || len(key) > 100 || len(value) > 16<<10 || !json.Valid(value) {
		return errors.New("guild_id, setting key and valid JSON value are required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO guild_feature_settings(guild_id,setting_key,setting_json,updated_at) VALUES(?,?,?,?) ON CONFLICT(guild_id,setting_key) DO UPDATE SET setting_json=excluded.setting_json,updated_at=excluded.updated_at`, guildID, key, string(value), now())
	return err
}

// ----- 2.1 ブラックリスト -----
func (s *Storage) AddGuildBlacklist(ctx context.Context, guildID, userID, reason string) error {
	if strings.TrimSpace(guildID) == "" || strings.TrimSpace(userID) == "" || utf8.RuneCountInString(reason) > 500 {
		return errors.New("valid guild_id and user_id are required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO guild_user_blacklist(guild_id,user_id,reason,created_at) VALUES(?,?,?,?) ON CONFLICT(guild_id,user_id) DO UPDATE SET reason=excluded.reason,created_at=excluded.created_at`, guildID, userID, reason, now())
	return err
}

func (s *Storage) RemoveGuildBlacklist(ctx context.Context, guildID, userID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM guild_user_blacklist WHERE guild_id=? AND user_id=?`, guildID, userID)
	return err
}

func (s *Storage) IsGuildBlacklisted(ctx context.Context, guildID, userID string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM guild_user_blacklist WHERE guild_id=? AND user_id=?)`, guildID, userID).Scan(&exists)
	return exists == 1, err
}

// ===== 3. スティッキーメッセージ =====
func (s *Storage) SetStickyMessage(ctx context.Context, sticky StickyMessage) error {
	if sticky.GuildID == "" || sticky.ChannelID == "" || strings.TrimSpace(sticky.Content) == "" || utf8.RuneCountInString(sticky.Content) > 2000 {
		return errors.New("guild_id, channel_id and message content are required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO sticky_messages(guild_id,channel_id,message_id,content,updated_at) VALUES(?,?,?, ?,?) ON CONFLICT(guild_id,channel_id) DO UPDATE SET message_id=excluded.message_id,content=excluded.content,updated_at=excluded.updated_at`, sticky.GuildID, sticky.ChannelID, sticky.MessageID, sticky.Content, now())
	return err
}

func (s *Storage) GetStickyMessage(ctx context.Context, guildID, channelID string) (*StickyMessage, error) {
	var sticky StickyMessage
	err := s.db.QueryRowContext(ctx, `SELECT guild_id,channel_id,message_id,content FROM sticky_messages WHERE guild_id=? AND channel_id=?`, guildID, channelID).Scan(&sticky.GuildID, &sticky.ChannelID, &sticky.MessageID, &sticky.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &sticky, err
}

func (s *Storage) ListStickyMessages(ctx context.Context, guildID string) ([]StickyMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT guild_id,channel_id,message_id,content FROM sticky_messages WHERE guild_id=?`, guildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []StickyMessage
	for rows.Next() {
		var sticky StickyMessage
		if err := rows.Scan(&sticky.GuildID, &sticky.ChannelID, &sticky.MessageID, &sticky.Content); err != nil {
			return nil, err
		}
		result = append(result, sticky)
	}
	return result, rows.Err()
}

func (s *Storage) RemoveStickyMessage(ctx context.Context, guildID, channelID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM sticky_messages WHERE guild_id=? AND channel_id=?`, guildID, channelID)
	return err
}

func (s *Storage) UpdateStickyMessageID(ctx context.Context, guildID, channelID, oldMessageID, newMessageID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	result, err := s.db.ExecContext(ctx, `UPDATE sticky_messages SET message_id=?,updated_at=? WHERE guild_id=? AND channel_id=? AND message_id=?`, newMessageID, now(), guildID, channelID, oldMessageID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return errors.New("sticky message configuration changed before its message ID could be updated")
	}
	return nil
}

// ===== 4. ポイントとゲーム =====
func (s *Storage) GetPointAccount(ctx context.Context, guildID, userID string) (PointAccount, error) {
	var account PointAccount
	err := s.db.QueryRowContext(ctx, `SELECT balance FROM point_accounts WHERE guild_id=? AND user_id=?`, guildID, userID).Scan(&account.Balance)
	if errors.Is(err, sql.ErrNoRows) {
		return account, nil
	}
	return account, err
}

func (s *Storage) EarnPoints(ctx context.Context, guildID, userID, eventID string, points, cooldownSeconds, dailyLimit int64) (PointResult, error) {
	if guildID == "" || userID == "" || eventID == "" || points < 1 || cooldownSeconds < 0 || dailyLimit < 1 {
		return PointResult{}, errors.New("invalid point earning parameters")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PointResult{}, err
	}
	defer tx.Rollback()
	current := time.Now().UTC()
	date := current.Format("2006-01-02")
	var result PointResult
	var balance, earnedToday int64
	var earnedDate, lastEarned string
	err = tx.QueryRowContext(ctx, `SELECT balance,earned_today,earned_date,last_earned_at FROM point_accounts WHERE guild_id=? AND user_id=?`, guildID, userID).Scan(&balance, &earnedToday, &earnedDate, &lastEarned)
	if errors.Is(err, sql.ErrNoRows) {
		earnedToday = 0
		earnedDate = date
	} else if err != nil {
		return result, err
	}
	if earnedDate != date {
		earnedToday = 0
	}
	var lastEvent string
	err = tx.QueryRowContext(ctx, `SELECT event_key FROM point_ledger WHERE event_key=?`, "earn:"+eventID).Scan(&lastEvent)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	allowed := errors.Is(err, sql.ErrNoRows) && earnedToday < dailyLimit
	if allowed && lastEarned != "" {
		last, parseErr := time.Parse("2006-01-02T15:04:05.000000000Z", lastEarned)
		if parseErr != nil {
			return result, fmt.Errorf("invalid stored point timestamp: %w", parseErr)
		}
		allowed = current.Sub(last) >= time.Duration(cooldownSeconds)*time.Second
	}
	if allowed {
		if points > dailyLimit-earnedToday {
			points = dailyLimit - earnedToday
		}
		if points > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO point_ledger(transaction_id,event_key,guild_id,user_id,delta,reason,created_at) VALUES(?,?,?,?,?,'message reward',?)`, randomID("pt_"), "earn:"+eventID, guildID, userID, points, now()); err != nil {
				return result, err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO point_accounts(guild_id,user_id,balance,earned_today,earned_date,last_earned_at) VALUES(?,?,?,?,?,?) ON CONFLICT(guild_id,user_id) DO UPDATE SET balance=point_accounts.balance+excluded.balance,earned_today=excluded.earned_today,earned_date=excluded.earned_date,last_earned_at=excluded.last_earned_at`, guildID, userID, points, earnedToday+points, date, now())
			if err != nil {
				return result, err
			}
			balance += points
			result.Earned = points
		}
	}
	result.Balance = balance
	return result, tx.Commit()
}

// ----- 4.1 抽選 -----
func (s *Storage) DrawLottery(ctx context.Context, guildID, userID, eventID string, cost, prize, chance int64) (PointResult, bool, error) {
	if guildID == "" || userID == "" || eventID == "" || cost < 1 || prize < 0 || chance < 0 || chance > 100 {
		return PointResult{}, false, errors.New("invalid lottery parameters")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PointResult{}, false, err
	}
	defer tx.Rollback()
	var saved string
	err = tx.QueryRowContext(ctx, `SELECT result_json FROM star_feature_actions WHERE event_key=?`, "lottery:"+eventID).Scan(&saved)
	if err == nil {
		var result struct {
			Points PointResult `json:"points"`
			Won    bool        `json:"won"`
		}
		if err := json.Unmarshal([]byte(saved), &result); err != nil {
			return PointResult{}, false, err
		}
		return result.Points, result.Won, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return PointResult{}, false, err
	}
	var balance int64
	if err := tx.QueryRowContext(ctx, `SELECT balance FROM point_accounts WHERE guild_id=? AND user_id=?`, guildID, userID).Scan(&balance); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PointResult{Balance: 0}, false, errors.New("insufficient points")
		}
		return PointResult{}, false, err
	}
	if balance < cost {
		return PointResult{Balance: balance}, false, errors.New("insufficient points")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(100))
	if err != nil {
		return PointResult{}, false, err
	}
	won := n.Int64() < chance
	reward := -cost
	if won {
		reward += prize
	}
	reason := "lottery:lost"
	if won {
		reason = "lottery:won"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO point_ledger(transaction_id,event_key,guild_id,user_id,delta,reason,created_at) VALUES(?,?,?,?,?,?,?)`, randomID("pt_"), "lottery:"+eventID, guildID, userID, reward, reason, now()); err != nil {
		return PointResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE point_accounts SET balance=balance+? WHERE guild_id=? AND user_id=?`, reward, guildID, userID); err != nil {
		return PointResult{}, false, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT balance FROM point_accounts WHERE guild_id=? AND user_id=?`, guildID, userID).Scan(&balance); err != nil {
		return PointResult{}, false, err
	}
	points := PointResult{Balance: balance}
	resultJSON, err := json.Marshal(map[string]any{"points": points, "won": won})
	if err != nil {
		return PointResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO star_feature_actions(event_key,result_json,created_at) VALUES(?,?,?)`, "lottery:"+eventID, string(resultJSON), now()); err != nil {
		return PointResult{}, false, err
	}
	return points, won, tx.Commit()
}

// ----- 4.2 おみくじ -----
func (s *Storage) DrawOmikuji(ctx context.Context, guildID, userID, eventID string) (string, error) {
	if guildID == "" || userID == "" || eventID == "" {
		return "", errors.New("guild_id, user_id and event_id are required")
	}
	results := []string{"大吉", "中吉", "小吉", "吉", "末吉", "凶"}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(len(results))))
	if err != nil {
		return "", err
	}
	result := results[n.Int64()]
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var saved string
	err = tx.QueryRowContext(ctx, `SELECT result_json FROM star_feature_actions WHERE event_key=?`, "omikuji:"+eventID).Scan(&saved)
	if err == nil {
		var result struct {
			Fortune string `json:"fortune"`
		}
		if err := json.Unmarshal([]byte(saved), &result); err != nil {
			return "", err
		}
		return result.Fortune, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	date := time.Now().UTC().Format("2006-01-02")
	_, err = tx.ExecContext(ctx, `INSERT INTO omikuji_draws(guild_id,user_id,draw_date,result) VALUES(?,?,?,?)`, guildID, userID, date, result)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return "", errors.New("omikuji already drawn today")
		}
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO point_ledger(transaction_id,event_key,guild_id,user_id,delta,reason,created_at) VALUES(?,?,?,?,0,'omikuji',?)`, randomID("pt_"), "omikuji:"+eventID, guildID, userID, now()); err != nil {
		return "", err
	}
	resultJSON, err := json.Marshal(map[string]string{"fortune": result})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO star_feature_actions(event_key,result_json,created_at) VALUES(?,?,?)`, "omikuji:"+eventID, string(resultJSON), now()); err != nil {
		return "", err
	}
	return result, tx.Commit()
}

// ===== 5. ショップ =====
func (s *Storage) UpsertShopItem(ctx context.Context, guildID string, item ShopItem) error {
	if guildID == "" || item.ItemID == "" || len(item.ItemID) > 50 || strings.TrimSpace(item.Name) == "" || utf8.RuneCountInString(item.Name) > 100 || item.Price < 1 || item.RoleID == "" {
		return errors.New("shop item requires item_id, name, positive price and role_id")
	}
	for _, character := range item.ItemID {
		if !(character >= 'a' && character <= 'z') &&
			!(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') &&
			character != '-' {
			return errors.New("shop item_id may contain only ASCII letters, numbers and hyphens")
		}
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO shop_items(guild_id,item_id,name,price,role_id,enabled,created_at) VALUES(?,?,?,?,?,1,?) ON CONFLICT(guild_id,item_id) DO UPDATE SET name=excluded.name,price=excluded.price,role_id=excluded.role_id,enabled=1`, guildID, item.ItemID, item.Name, item.Price, item.RoleID, now())
	return err
}

func (s *Storage) ListShopItems(ctx context.Context, guildID string) ([]ShopItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT item_id,name,price,role_id FROM shop_items WHERE guild_id=? AND enabled=1 ORDER BY item_id`, guildID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ShopItem
	for rows.Next() {
		var item ShopItem
		if err := rows.Scan(&item.ItemID, &item.Name, &item.Price, &item.RoleID); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ----- 5.1 購入処理 -----
func (s *Storage) BuyShopItem(ctx context.Context, guildID, userID, itemID, eventID string) (ShopItem, PointResult, bool, error) {
	if guildID == "" || userID == "" || itemID == "" || eventID == "" {
		return ShopItem{}, PointResult{}, false, errors.New("guild_id, user_id, item_id and event_id are required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ShopItem{}, PointResult{}, false, err
	}
	defer tx.Rollback()
	var saved string
	err = tx.QueryRowContext(ctx, `SELECT result_json FROM star_feature_actions WHERE event_key=?`, "shop:"+eventID).Scan(&saved)
	if err == nil {
		var result struct {
			Item        ShopItem    `json:"item"`
			Points      PointResult `json:"points"`
			RolePending bool        `json:"role_pending"`
		}
		if err := json.Unmarshal([]byte(saved), &result); err != nil {
			return ShopItem{}, PointResult{}, false, err
		}
		return result.Item, result.Points, result.RolePending, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ShopItem{}, PointResult{}, false, err
	}
	var item ShopItem
	var enabled int
	if err := tx.QueryRowContext(ctx, `SELECT item_id,name,price,role_id,enabled FROM shop_items WHERE guild_id=? AND item_id=?`, guildID, itemID).Scan(&item.ItemID, &item.Name, &item.Price, &item.RoleID, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ShopItem{}, PointResult{}, false, errors.New("shop item not found")
		}
		return ShopItem{}, PointResult{}, false, err
	}
	// A user who has never earned points has no point_accounts row yet.
	// Treat that as a zero balance so the price check below reports
	// "insufficient points" instead of leaking a raw sql.ErrNoRows error
	// (which the Worker misleadingly presented as "item not found").
	var balance int64
	if err := tx.QueryRowContext(ctx, `SELECT balance FROM point_accounts WHERE guild_id=? AND user_id=?`, guildID, userID).Scan(&balance); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ShopItem{}, PointResult{}, false, err
	}
	var roleStatus, purchasedRoleID string
	err = tx.QueryRowContext(ctx, `SELECT role_status,role_id FROM user_inventory WHERE guild_id=? AND user_id=? AND item_id=?`, guildID, userID, itemID).Scan(&roleStatus, &purchasedRoleID)
	if err == nil {
		if roleStatus != "pending" {
			return item, PointResult{Balance: balance}, false, errors.New("shop item already owned")
		}
		// Keep the originally purchased role even if the catalog item was later edited
		// or disabled. A pending fulfillment can be retried without another charge.
		if purchasedRoleID != "" {
			item.RoleID = purchasedRoleID
		}
		return item, PointResult{Balance: balance}, true, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ShopItem{}, PointResult{}, false, err
	}
	if enabled != 1 {
		return ShopItem{}, PointResult{}, false, errors.New("shop item is disabled")
	}
	if balance < item.Price {
		return item, PointResult{Balance: balance}, false, errors.New("insufficient points")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_inventory(guild_id,user_id,item_id,purchased_at,role_status,role_id) VALUES(?,?,?,?,'pending',?)`, guildID, userID, itemID, now(), item.RoleID); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return item, PointResult{Balance: balance}, false, errors.New("shop item already owned")
		}
		return ShopItem{}, PointResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO point_ledger(transaction_id,event_key,guild_id,user_id,delta,reason,created_at) VALUES(?,?,?,?,?,?,?)`, randomID("pt_"), "shop:"+eventID, guildID, userID, -item.Price, "shop purchase: "+itemID, now()); err != nil {
		return ShopItem{}, PointResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE point_accounts SET balance=balance-? WHERE guild_id=? AND user_id=?`, item.Price, guildID, userID); err != nil {
		return ShopItem{}, PointResult{}, false, err
	}
	points := PointResult{Balance: balance - item.Price}
	resultJSON, err := json.Marshal(map[string]any{"item": item, "points": points, "role_pending": true})
	if err != nil {
		return ShopItem{}, PointResult{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO star_feature_actions(event_key,result_json,created_at) VALUES(?,?,?)`, "shop:"+eventID, string(resultJSON), now()); err != nil {
		return ShopItem{}, PointResult{}, false, err
	}
	return item, points, true, tx.Commit()
}

// CompleteShopRoleGrant is idempotent so a retried Worker event can safely
// finish the purchase after Discord accepted the role assignment.
func (s *Storage) CompleteShopRoleGrant(ctx context.Context, guildID, userID, itemID string) error {
	if guildID == "" || userID == "" || itemID == "" {
		return errors.New("guild_id, user_id and item_id are required")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.ExecContext(ctx, `UPDATE user_inventory SET role_status='fulfilled' WHERE guild_id=? AND user_id=? AND item_id=? AND role_status='pending'`, guildID, userID, itemID)
	if err != nil {
		return err
	}
	if changed, err := res.RowsAffected(); err != nil {
		return err
	} else if changed == 1 {
		return nil
	}
	var status string
	if err := s.db.QueryRowContext(ctx, `SELECT role_status FROM user_inventory WHERE guild_id=? AND user_id=? AND item_id=?`, guildID, userID, itemID).Scan(&status); err != nil {
		return err
	}
	if status != "fulfilled" {
		return errors.New("shop inventory is not pending role fulfillment")
	}
	return nil
}
