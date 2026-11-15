export type BotId = 'general' | 'read_aloud_a' | 'read_aloud_b';
export type WorkerRole = 'general' | 'read-aloud';

export interface EventEnvelope<T = Record<string, unknown>> {
  protocol_version: 1;
  event_id: string;
  correlation_id: string;
  bot_id: BotId;
  bot_user_id?: string;
  event_type: string;
  occurred_at: string;
  ordering_key: string;
  session_id?: string;
  sequence: number;
  payload: T;
  lease_token: string;
  attempts: number;
  ephemeral?: boolean;
}

export interface InteractionPayload {
  id: string;
  application_id: string;
  type: number;
  token: string;
  guild_id?: string;
  channel_id?: string;
  member?: { user?: { id?: string }; permissions?: string };
  user?: { id?: string };
  channel?: { id?: string };
  data?: {
    name?: string;
    type?: number;
    custom_id?: string;
    options?: Array<{ name: string; type: number; value?: string | number; options?: Array<{ name: string; type: number; value?: string | number }> }>;
  };
}

export interface DiscordMessagePayload {
  id: string;
  guild_id?: string;
  channel_id: string;
  content?: string;
  author?: { id: string; bot?: boolean; username?: string };
}

export interface StickyMessage {
  guild_id: string;
  channel_id: string;
  message_id: string;
  content: string;
}

export interface ShopItem {
  item_id: string;
  name: string;
  price: number;
  role_id: string;
}

export interface GuildMemberPayload {
  guild_id: string;
  user?: { id: string; username?: string; global_name?: string; bot?: boolean };
  roles?: string[];
  nick?: string | null;
}

export interface VoiceLogPayload {
  guild_id: string;
  user_id: string;
  channel_id: string | null;
}

export interface VoiceStatePayload {
  guild_id: string;
  user_id: string;
  channel_id: string | null;
  session_id?: string;
  deaf?: boolean;
  mute?: boolean;
  self_deaf?: boolean;
  self_mute?: boolean;
}

export interface VoiceServerPayload {
  guild_id: string;
  token: string;
  endpoint: string;
}

export interface ReadAloudSettings {
  enabled: boolean;
  speed: number;
  volume: number;
  speaker_id: number;
}

export interface DictionaryEntry {
  entry_id: string;
  word: string;
  reading: string;
}
