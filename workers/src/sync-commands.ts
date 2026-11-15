import { GatewayClient } from './core/client.js';
import { logger } from './core/logger.js';
import { starCommands } from './general/star-features.js';
import type { BotId } from './core/types.js';

const client = new GatewayClient();
const botId = client.botID;
const commands = botId === 'general'
  ? [{ name: 'ping', description: '星燈の応答状態を確認します', type: 1 }, ...starCommands]
  : [
      { name: 'join', description: '指定したボイスチャンネルに接続します', type: 1, options: [{ name: 'channel', description: '接続先チャンネル', type: 7, required: true, channel_types: [2, 13] }] },
      { name: 'leave', description: 'ボイスチャンネルから退出します', type: 1 },
      { name: 'say', description: '指定した文章を読み上げます', type: 1, options: [{ name: 'text', description: '読み上げる文章', type: 3, required: true, max_length: 1000 }] },
    ];

// Deploy scripts pass a unified P1228_APPLICATION_ID per Worker, but a
// direct run (npm run sync:commands with .env) only has the per-BOT
// variables from .env.example. Accept both so the direct run works too.
const perBotApplicationIdEnv: Record<BotId, string> = {
  general: 'P1228_GENERAL_APPLICATION_ID',
  read_aloud_a: 'P1228_READ_ALOUD_A_APPLICATION_ID',
  read_aloud_b: 'P1228_READ_ALOUD_B_APPLICATION_ID',
};
const applicationId = process.env.P1228_APPLICATION_ID ?? process.env[perBotApplicationIdEnv[botId]] ?? '';
if (!applicationId) {
  throw new Error(`Application ID is missing: set P1228_APPLICATION_ID or ${perBotApplicationIdEnv[botId]} for bot ${botId}`);
}

await client.discordOperation('sync_commands', { application_id: applicationId, commands });
logger.info('application_commands_synced', { bot_id: botId, count: commands.length });
