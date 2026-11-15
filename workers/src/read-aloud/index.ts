import { logger } from '../core/logger.js';
import { runWorker } from '../core/worker.js';
import type { EventEnvelope, InteractionPayload, DiscordMessagePayload, VoiceServerPayload, VoiceStatePayload } from '../core/types.js';
import { GatewayClient } from '../core/client.js';
import { ReadAloudVoiceService } from './voice/service.js';

// ===== 1. イベント処理とコマンド実行 =====
async function main(): Promise<void> {
  const client = new GatewayClient();
  const voice = new ReadAloudVoiceService(client);
  await runWorker(async (event: EventEnvelope, client: GatewayClient) => {
    if (event.event_type === 'VOICE_STATE_UPDATE' || event.event_type === 'VOICE_SERVER_UPDATE') {
      voice.routeVoiceEvent(event.event_type, event.payload as unknown as VoiceStatePayload | VoiceServerPayload);
      return;
    }

    if (event.event_type === 'INTERACTION_CREATE') {
      const interaction = event.payload as unknown as InteractionPayload & { interaction_ref?: string };
      if (!interaction.interaction_ref) return;
      if (interaction.type === 4) {
        await client.interaction(interaction.interaction_ref, 'initial', { type: 8, data: { choices: [] } });
        return;
      }
      if (interaction.type !== 2) {
        await client.interaction(interaction.interaction_ref, 'initial', { type: 4, data: { content: 'この操作には対応していません。', flags: 64 } });
        return;
      }
      const name = interaction.data?.name ?? '';
      const guildId = interaction.guild_id ?? '';
      const options = interaction.data?.options ?? [];
      const channelId = options.find((option) => option.name === 'channel')?.value;
      const text = options.find((option) => option.name === 'text')?.value;

      if (name === 'join') {
        if (typeof channelId !== 'string') {
          await client.interaction(interaction.interaction_ref, 'initial', { type: 4, data: { content: '接続先ボイスチャンネルを指定してください。', flags: 64 } });
          return;
        }
        await client.interaction(interaction.interaction_ref, 'initial', { type: 5, data: { flags: 64 } });
        try {
          await voice.join(guildId, channelId);
          await client.interaction(interaction.interaction_ref, 'edit-original', { content: 'ボイスチャンネルへの接続要求を送信しました。' });
        } catch (error) {
          await client.interaction(interaction.interaction_ref, 'edit-original', { content: `接続に失敗しました: ${error instanceof Error ? error.message : String(error)}` });
          throw error;
        }
        return;
      }
      if (name === 'leave') {
        await voice.leave(guildId);
        await client.interaction(interaction.interaction_ref, 'initial', { type: 4, data: { content: 'ボイスチャンネルから退出しました。', flags: 64 } });
        return;
      }
      if (name === 'say') {
        if (typeof text !== 'string' || !text.trim()) {
          await client.interaction(interaction.interaction_ref, 'initial', { type: 4, data: { content: '読み上げる文章を入力してください。', flags: 64 } });
          return;
        }
        await client.interaction(interaction.interaction_ref, 'initial', { type: 5, data: { flags: 64 } });
        try {
          const userId = interaction.member?.user?.id ?? interaction.user?.id;
          await voice.speak(guildId, text, undefined, userId);
          await client.interaction(interaction.interaction_ref, 'edit-original', { content: '読み上げキューに追加しました。' });
        } catch (error) {
          await client.interaction(interaction.interaction_ref, 'edit-original', { content: `読み上げに失敗しました: ${error instanceof Error ? error.message : String(error)}` });
          throw error;
        }
        return;
      }
      await client.interaction(interaction.interaction_ref, 'initial', { type: 4, data: { content: `未実装のコマンドです: ${name}`, flags: 64 } });
      return;
    }

    if (event.event_type === 'MESSAGE_CREATE') {
      const message = event.payload as unknown as DiscordMessagePayload;
      if (message.author?.bot || !message.guild_id || !message.content) return;
      await voice.maybeReadMessage(message.guild_id, message.author?.id, message.content);
    }
  }, client);
}

// ===== 2. 起動 =====
main().catch((error) => {
  logger.error('read_aloud_fatal', { error: error instanceof Error ? error.message : String(error) });
  process.exitCode = 1;
});
