import { createHash } from 'node:crypto';
import { AudioPlayerStatus, createAudioPlayer, createAudioResource, entersState, joinVoiceChannel, StreamType, VoiceConnectionStatus, type AudioPlayer, type VoiceConnection } from '@discordjs/voice';
import { AudioEngineClient } from '../audio/engine.js';
import { logger } from '../../core/logger.js';
import type { GatewayClient } from '../../core/client.js';
import type { ReadAloudSettings, VoiceServerPayload, VoiceStatePayload } from '../../core/types.js';
import { createP1228AdapterCreator, P1228VoiceAdapter } from './adapter.js';

// ===== 1. セッション型定義 =====
interface GuildVoiceSession {
  connection: VoiceConnection;
  player: AudioPlayer;
  adapter: P1228VoiceAdapter | null;
  channelId: string;
  queue: Array<{ text: string; userId?: string; settings?: Partial<ReadAloudSettings> }>;
  processing: boolean;
}

// ===== 2. 音声サービスの実装 =====
export class ReadAloudVoiceService {
  private readonly sessions = new Map<string, GuildVoiceSession>();
  private readonly engine = new AudioEngineClient();

  constructor(private readonly client: GatewayClient) {}

  async join(guildId: string, channelId: string): Promise<void> {
    if (!guildId || !channelId) throw new Error('guild_id and channel_id are required');
    await this.leave(guildId);
    let adapter: P1228VoiceAdapter | null = null;
    const connection = joinVoiceChannel({
      guildId,
      channelId,
      adapterCreator: createP1228AdapterCreator(this.client, guildId, (value) => {
        adapter = value;
      }),
      selfDeaf: true,
      selfMute: false,
      // @discordjs/voice DAVE encryption is enabled by default. Do not disable it.
    });
    const player = createAudioPlayer();
    connection.subscribe(player);
    const session: GuildVoiceSession = { connection, player, adapter, channelId, queue: [], processing: false };
    this.sessions.set(guildId, session);
    connection.on('stateChange', (_oldState, newState) => {
      if (newState.status === VoiceConnectionStatus.Destroyed) {
        if (this.sessions.get(guildId) === session) this.sessions.delete(guildId);
        logger.info('voice_connection_destroyed', { bot_id: this.client.botID, guild_id: guildId });
      }
    });
    connection.on('error', (error) => logger.error('voice_connection_error', { guild_id: guildId, error: error.message }));
    player.on('error', (error) => logger.error('audio_player_error', { guild_id: guildId, error: error.message }));
    logger.info('voice_join_requested', { bot_id: this.client.botID, guild_id: guildId, channel_id: channelId });
    // この待機は Worker のイベントループをブロックしないこと。
    // VOICE_STATE_UPDATE / VOICE_SERVER_UPDATE は同じループで処理されてから
    // 接続が Ready になるため、ここでは非同期で待機する。
    void entersState(connection, VoiceConnectionStatus.Ready, 25_000)
      .then(() => {
        logger.info('voice_connected', { bot_id: this.client.botID, guild_id: guildId, channel_id: channelId });
      })
      .catch((error: unknown) => {
        logger.error('voice_connection_timeout', { bot_id: this.client.botID, guild_id: guildId, error: error instanceof Error ? error.message : String(error) });
        if (this.sessions.get(guildId) === session) {
          this.sessions.delete(guildId);
          connection.destroy();
        }
      });
  }

  async leave(guildId: string): Promise<void> {
    const session = this.sessions.get(guildId);
    if (!session) return;
    this.sessions.delete(guildId);
    session.queue.length = 0;
    session.player.stop(true);
    session.connection.destroy();
    logger.info('voice_left', { bot_id: this.client.botID, guild_id: guildId });
  }

  // ----- 2.1 音声イベントの振り分け -----
  routeVoiceEvent(type: string, payload: VoiceStatePayload | VoiceServerPayload): void {
    const guildId = payload.guild_id;
    const session = this.sessions.get(guildId);
    if (!session?.adapter) return;
    if (type === 'VOICE_STATE_UPDATE') session.adapter.onVoiceStateUpdate(payload as VoiceStatePayload);
    if (type === 'VOICE_SERVER_UPDATE') session.adapter.onVoiceServerUpdate(payload as VoiceServerPayload);
  }

  isConnected(guildId: string): boolean {
    const session = this.sessions.get(guildId);
    return session?.connection.state.status === VoiceConnectionStatus.Ready;
  }

  // ----- 2.2 読み上げキュー -----
  async speak(guildId: string, rawText: string, settingsOverride?: Partial<ReadAloudSettings>, userId?: string): Promise<void> {
    const session = this.sessions.get(guildId);
    if (!session) throw new Error('BOT is not connected to a voice channel. Use /join first.');
    const text = rawText.trim().slice(0, 5_000);
    if (!text) throw new Error('読み上げる文章が空です。');
    if (session.queue.length >= 50) throw new Error('読み上げキューが上限（50件）に達しています。');
    session.queue.push({ text, ...(userId ? { userId } : {}), ...(settingsOverride ? { settings: settingsOverride } : {}) });
    void this.pumpQueue(guildId, session);
  }

  private async pumpQueue(guildId: string, session: GuildVoiceSession): Promise<void> {
    if (session.processing) return;
    session.processing = true;
    try {
      while (session.queue.length > 0 && this.sessions.get(guildId) === session) {
        const item = session.queue.shift();
        if (!item) continue;
        // Isolate failures per item: one text that the audio engine
        // rejects must not abort playback of everything queued behind it.
        try {
          await entersState(session.connection, VoiceConnectionStatus.Ready, 25_000);
          const settingsSnapshot = await this.getSettings(guildId, item.userId);
          const dictionaryResult = await this.applyDictionary(guildId, item.userId, item.text);
          const settings = settingsSnapshot.settings;
          const audio = await this.engine.synthesize({
            text: dictionaryResult.text,
            speakerId: item.settings?.speaker_id ?? settings.speaker_id,
            speed: item.settings?.speed ?? settings.speed,
            volume: item.settings?.volume ?? settings.volume,
            cacheRevision: `${settingsSnapshot.revision}:${dictionaryResult.revision}`,
          });
          const resource = createAudioResource(audio, { inputType: StreamType.Arbitrary });
          session.player.play(resource);
          await entersState(session.player, AudioPlayerStatus.Playing, 30_000);
          await entersState(session.player, AudioPlayerStatus.Idle, 10 * 60_000);
          logger.info('audio_playback_completed', { bot_id: this.client.botID, guild_id: guildId, queue_remaining: session.queue.length });
        } catch (itemError) {
          logger.error('audio_item_failed', { bot_id: this.client.botID, guild_id: guildId, error: itemError instanceof Error ? itemError.message : String(itemError) });
          // If the session itself is gone, stop instead of failing every
          // remaining queued item one by one.
          if (this.sessions.get(guildId) !== session || session.connection.state.status === VoiceConnectionStatus.Destroyed) break;
        }
      }
    } catch (error) {
      logger.error('audio_queue_failed', { bot_id: this.client.botID, guild_id: guildId, error: error instanceof Error ? error.message : String(error) });
    } finally {
      session.processing = false;
      if (session.queue.length > 0 && this.sessions.get(guildId) === session) void this.pumpQueue(guildId, session);
    }
  }

  // ----- 2.3 メッセージ読み上げの判定 -----
  async maybeReadMessage(guildId: string, userId: string | undefined, text: string): Promise<void> {
    if (!guildId || !text.trim() || !this.sessions.has(guildId)) return;
    const snapshot = await this.getSettings(guildId, userId);
    if (!snapshot.settings.enabled) return;
    await this.speak(guildId, text, undefined, userId);
  }

  // ----- 2.4 設定と辞書の適用 -----
  private async getSettings(guildId: string, userId?: string): Promise<{ revision: string; settings: ReadAloudSettings }> {
    const scopes: Array<{ scope: 'global' | 'guild' | 'user'; scopeId: string }> = [
      { scope: 'global', scopeId: 'global' },
      { scope: 'guild', scopeId: guildId },
      ...(userId ? [{ scope: 'user' as const, scopeId: userId }] : []),
    ];
    const snapshots = await Promise.all(scopes.map(({ scope, scopeId }) => this.client.getSettings(scope, scopeId)));
    const defaults: ReadAloudSettings = { enabled: false, speed: 1.0, volume: 1.0, speaker_id: 1 };
    const resolved = snapshots.reduce((value, snapshot) => (snapshot.revision > 0 ? { ...value, ...snapshot.settings } : value), defaults);
    return { revision: snapshots.map((snapshot) => snapshot.revision).join(':'), settings: resolved };
  }

  private async applyDictionary(guildId: string, userId: string | undefined, source: string): Promise<{ text: string; revision: string }> {
    const [global, guild, user] = await Promise.all([
      this.client.getDictionary('global', 'global'),
      this.client.getDictionary('guild', guildId),
      userId ? this.client.getDictionary('user', userId) : Promise.resolve([]),
    ]);
    // Higher scopes override lower scopes for duplicate words: global -> guild -> user.
    const byWord = new Map<string, (typeof global)[number]>();
    for (const entry of [...global, ...guild, ...user]) byWord.set(entry.word, entry);
    const entries = [...byWord.values()].sort((a, b) => b.word.length - a.word.length || a.word.localeCompare(b.word));
    const revision = createHash('sha256').update(JSON.stringify(entries)).digest('hex');
    let value = source;
    for (const entry of entries) value = value.replaceAll(entry.word, entry.reading);
    return { text: value, revision };
  }
}
