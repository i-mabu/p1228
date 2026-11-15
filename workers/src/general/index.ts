import { logger } from '../core/logger.js';
import { runWorker } from '../core/worker.js';
import type { EventEnvelope, InteractionPayload } from '../core/types.js';
import type { GatewayClient } from '../core/client.js';
import { handleStarEvent } from './star-features.js';

// ===== 1. イベント振り分け =====
async function handle(event: EventEnvelope, client: GatewayClient): Promise<void> {
  if (event.event_type === 'INTERACTION_CREATE') {
    const interaction = event.payload as unknown as InteractionPayload & { interaction_ref?: string };
    if (!interaction.interaction_ref) {
      logger.warn('interaction_reference_missing', { event_id: event.event_id });
      return;
    }
    const command = interaction.type === 2 ? (interaction.data?.name ?? '') : '';
    if (interaction.type === 2 && command === 'ping') {
      await client.interaction(interaction.interaction_ref, 'initial', { type: 4, data: { content: 'Pong! 星燈は正常に稼働しています。', flags: 64 } });
      logger.info('command_handled', { bot_id: client.botID, command, event_id: event.event_id });
      return;
    }
    await handleStarEvent(client, event);
    return;
  }

  if (
    event.event_type !== 'INTERACTION_CREATE' &&
    event.event_type !== 'MESSAGE_CREATE' &&
    event.event_type !== 'MESSAGE_UPDATE' &&
    event.event_type !== 'MESSAGE_DELETE' &&
    event.event_type !== 'MESSAGE_DELETE_BULK' &&
    !event.event_type.startsWith('GUILD_MEMBER_') &&
    event.event_type !== 'VOICE_STATE_UPDATE' &&
    !event.event_type.startsWith('CHANNEL_') &&
    event.event_type !== 'GUILD_CREATE' &&
    event.event_type !== 'GUILD_DELETE'
  )
    return;
  await handleStarEvent(client, event);
}

// ===== 2. Workerの起動 =====
runWorker(handle).catch((error: unknown) => {
  logger.error('worker_fatal', { error: error instanceof Error ? error.message : String(error) });
  process.exitCode = 1;
});
