import { randomUUID } from 'node:crypto';
import type { BotId, DictionaryEntry, EventEnvelope, ReadAloudSettings, ShopItem, StickyMessage } from './types.js';

// ===== 1. Gatewayクライアント =====
export class GatewayClient {
  readonly instanceId = randomUUID();
  readonly baseURL: string;
  private readonly workerId: string;
  private readonly secret: string;
  private readonly botId: BotId;

  constructor() {
    this.baseURL = (process.env.P1228_GATEWAY_URL ?? 'http://127.0.0.1:8787').replace(/\/+$/, '');
    this.workerId = required('P1228_WORKER_ID');
    this.secret = required('P1228_WORKER_SECRET');
    this.botId = required('P1228_BOT_ID') as BotId;
    if (!['general', 'read_aloud_a', 'read_aloud_b'].includes(this.botId)) throw new Error('P1228_BOT_ID is invalid');
  }

  async request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
    const response = await fetch(`${this.baseURL}/internal/v1/${path}`, {
      method,
      headers: {
        'Content-Type': 'application/json',
        'X-P1228-Worker-ID': this.workerId,
        Authorization: `Bearer ${this.secret}`,
      },
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      signal: AbortSignal.timeout(15_000),
    });
    const text = await response.text();
    let parsed: unknown = {};
    try {
      parsed = text ? JSON.parse(text) : {};
    } catch {
      parsed = { raw: text.slice(0, 500) };
    }
    if (!response.ok) {
      const detail = parsed && typeof parsed === 'object' && 'error' in parsed ? JSON.stringify((parsed as { error: unknown }).error) : `HTTP ${response.status}`;
      throw new Error(`Gateway API ${method} ${path}: ${detail}`);
    }
    return parsed as T;
  }

  // ----- 1.1 登録とハートビート -----
  async register(): Promise<void> {
    await this.request('worker/register', 'POST', { instance_id: this.instanceId });
  }

  async heartbeat(): Promise<void> {
    await this.request('worker/heartbeat', 'POST', { instance_id: this.instanceId });
  }

  // ----- 1.2 イベントの受付 -----
  async claim(): Promise<EventEnvelope | null> {
    const result = await this.request<{ event: EventEnvelope | null }>('events/claim', 'POST', { instance_id: this.instanceId });
    return result.event;
  }

  async ack(event: EventEnvelope): Promise<void> {
    await this.request(`events/${encodeURIComponent(event.event_id)}/ack`, 'POST', { lease_token: event.lease_token });
  }

  async nack(event: EventEnvelope, reason: string): Promise<void> {
    await this.request(`events/${encodeURIComponent(event.event_id)}/nack`, 'POST', { lease_token: event.lease_token, reason: reason.slice(0, 1000) });
  }

  // ----- 1.3 Discord操作 -----
  async discordOperation<T = Record<string, unknown>>(operation: string, fields: Record<string, unknown>, operationId: string = randomUUID()): Promise<T> {
    return this.request<T>('discord/operations', 'POST', { operation_id: operationId, operation, ...fields });
  }

  async interaction(ref: string, action: 'initial' | 'edit-original' | 'followups', data: Record<string, unknown>): Promise<void> {
    await this.request(`interactions/${encodeURIComponent(ref)}/${action}`, 'POST', data);
  }

  async voiceGatewayPayload(payload: unknown): Promise<void> {
    await this.request('voice/gateway-payload', 'POST', { payload });
  }

  // ----- 1.4 設定と辞書の取得 -----
  async getSettings(scope: 'global' | 'guild' | 'user', scopeId: string): Promise<{ revision: number; settings: ReadAloudSettings }> {
    const qs = new URLSearchParams({ scope, scope_id: scopeId });
    return this.request(`data/read-aloud/settings?${qs.toString()}`);
  }

  async getDictionary(scope: 'global' | 'guild' | 'user', scopeId: string): Promise<DictionaryEntry[]> {
    const qs = new URLSearchParams({ scope, scope_id: scopeId });
    const result = await this.request<{ entries: DictionaryEntry[] }>(`data/dictionary/entries?${qs.toString()}`);
    return result.entries;
  }

  // ----- 1.5 星燈機能：設定 -----
  async getStarSettings(guildId: string): Promise<Record<string, unknown>> {
    const query = new URLSearchParams({ guild_id: guildId });
    const result = await this.request<{ settings: Record<string, unknown> }>(`data/star/settings?${query}`);
    return result.settings;
  }

  async setStarSetting(guildId: string, key: string, value: unknown): Promise<void> {
    await this.request('data/star/settings', 'PUT', { guild_id: guildId, key, value });
  }

  // ----- 1.6 星燈機能：ブラックリスト -----
  async isBlacklisted(guildId: string, userId: string): Promise<boolean> {
    const query = new URLSearchParams({ guild_id: guildId, user_id: userId });
    const result = await this.request<{ blacklisted: boolean }>(`data/star/blacklist?${query}`);
    return result.blacklisted;
  }

  async setBlacklist(guildId: string, userId: string, reason: string): Promise<void> {
    await this.request('data/star/blacklist', 'POST', { guild_id: guildId, user_id: userId, reason });
  }

  async removeBlacklist(guildId: string, userId: string): Promise<void> {
    await this.request('data/star/blacklist', 'DELETE', { guild_id: guildId, user_id: userId });
  }

  // ----- 1.7 星燈機能：スティッキー -----
  async getStickyMessages(guildId: string): Promise<StickyMessage[]> {
    const query = new URLSearchParams({ guild_id: guildId });
    const result = await this.request<{ items: StickyMessage[] }>(`data/star/sticky?${query}`);
    return result.items ?? [];
  }

  async setStickyMessage(sticky: StickyMessage): Promise<void> {
    await this.request('data/star/sticky', 'PUT', sticky);
  }

  async updateStickyMessageId(sticky: StickyMessage, oldMessageId: string, messageId: string): Promise<void> {
    await this.request('data/star/sticky', 'POST', {
      guild_id: sticky.guild_id,
      channel_id: sticky.channel_id,
      old_message_id: oldMessageId,
      message_id: messageId,
    });
  }

  async removeStickyMessage(guildId: string, channelId: string): Promise<void> {
    await this.request('data/star/sticky', 'DELETE', { guild_id: guildId, channel_id: channelId });
  }

  // ----- 1.8 星燈機能：ポイント -----
  async starPoints<T>(action: string, fields: Record<string, unknown>): Promise<T> {
    const result = await this.request<{ result: T }>('data/star/points', 'POST', { action, ...fields });
    return result.result;
  }

  // ----- 1.9 星燈機能：ショップ -----
  async getShop(guildId: string): Promise<ShopItem[]> {
    const query = new URLSearchParams({ guild_id: guildId });
    const result = await this.request<{ items: ShopItem[] }>(`data/star/shop?${query}`);
    return result.items ?? [];
  }

  async addShopItem(guildId: string, item: ShopItem): Promise<void> {
    await this.request('data/star/shop', 'PUT', { guild_id: guildId, item });
  }

  async buyShopItem(guildId: string, userId: string, itemId: string, eventId: string): Promise<{ item: ShopItem; balance: number; role_pending: boolean }> {
    return this.request('data/star/shop', 'POST', { guild_id: guildId, user_id: userId, item_id: itemId, event_id: eventId });
  }

  async completeShopItem(guildId: string, userId: string, itemId: string): Promise<void> {
    await this.request('data/star/shop', 'POST', { action: 'fulfill', guild_id: guildId, user_id: userId, item_id: itemId });
  }

  // ----- 1.10 ロール付与 -----
  async addRole(guildId: string, userId: string, roleId: string, operationId: string = randomUUID()): Promise<void> {
    await this.discordOperation('add_role', { guild_id: guildId, user_id: userId, role_id: roleId }, operationId);
  }

  get workerID(): string {
    return this.workerId;
  }
  get botID(): BotId {
    return this.botId;
  }
}

// ===== 2. 環境変数の取得 =====
function required(name: string): string {
  const value = process.env[name]?.trim();
  if (!value) throw new Error(`Required environment variable ${name} is missing`);
  return value;
}
