import { GatewayClient } from './core/client.js';
import { logger } from './core/logger.js';
import { starCommands } from './general/star-features.js';
import type { BotId } from './core/types.js';

// ===== 1. コマンド定義 =====
const client = new GatewayClient();
const botId = client.botID;
const commands =
  botId === 'general'
    ? [{ name: 'ping', description: '星燈の応答状態を確認します', type: 1 }, ...starCommands]
    : [
        {
          name: 'join',
          description: '指定したボイスチャンネルに接続します',
          type: 1,
          options: [{ name: 'channel', description: '接続先チャンネル', type: 7, required: true, channel_types: [2, 13] }],
        },
        { name: 'leave', description: 'ボイスチャンネルから退出します', type: 1 },
        { name: 'say', description: '指定した文章を読み上げます', type: 1, options: [{ name: 'text', description: '読み上げる文章', type: 3, required: true, max_length: 1000 }] },
      ];

// ===== 2. アプリケーションIDの解決 =====
// ===== 配置スクリプト経由では Worker ごとに統一された P1228_APPLICATION_ID が渡るが、 =====
// ===== 直接実行時（.env を利用した npm run sync:commands）では BOT ごとの変数だけが存在する。 =====
// ===== 両方に対応して、直接実行時の失敗を防ぐ。 =====
const perBotApplicationIdEnv: Record<BotId, string> = {
  general: 'P1228_GENERAL_APPLICATION_ID',
  read_aloud_a: 'P1228_READ_ALOUD_A_APPLICATION_ID',
  read_aloud_b: 'P1228_READ_ALOUD_B_APPLICATION_ID',
};
const applicationId = process.env.P1228_APPLICATION_ID ?? process.env[perBotApplicationIdEnv[botId]] ?? '';
if (!applicationId) {
  throw new Error(`Application ID is missing: set P1228_APPLICATION_ID or ${perBotApplicationIdEnv[botId]} for bot ${botId}`);
}

// ===== 3. コマンド登録の実行 =====
await client.discordOperation('sync_commands', { application_id: applicationId, commands });
logger.info('application_commands_synced', { bot_id: botId, count: commands.length });
