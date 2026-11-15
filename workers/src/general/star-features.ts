import { logger } from '../core/logger.js';
import type { GatewayClient } from '../core/client.js';
import type {
  EventEnvelope,
  GuildMemberPayload,
  InteractionPayload,
  ShopItem,
  StickyMessage,
  VoiceLogPayload,
} from '../core/types.js';

type InteractionWithRef = InteractionPayload & { interaction_ref: string; message?: { id: string } };
type GuildChannel = { id: string; type: number; parent_id?: string | null };
type RoleCache = Map<string, Set<string>>;

const guildChannelParents = new Map<string, string>();
const guildNames = new Map<string, string>();
const memberRoles: RoleCache = new Map();
const voiceChannels = new Map<string, string>();

const option = (name: string, description: string, type: number, required = false, extra: Record<string, unknown> = {}) => ({
  name, description, type, required, ...extra,
});
const subcommand = (name: string, description: string, options: unknown[] = []) => ({ type: 1, name, description, options });

export const starCommands: Record<string, unknown>[] = [
  {
    name: 'welcome', description: '参加時メッセージを設定します', type: 1,
    options: [
      subcommand('set', '参加時メッセージを設定', [option('channel', '送信先', 7, true, { channel_types: [0] }), option('message', '本文 ({user}, {username}, {server} が使用可能)', 3, true, { max_length: 1000 })]),
      subcommand('disable', '参加時メッセージを無効化'),
    ],
  },
  {
    name: 'goodbye', description: '退出時メッセージを設定します', type: 1,
    options: [
      subcommand('set', '退出時メッセージを設定', [option('channel', '送信先', 7, true, { channel_types: [0] }), option('message', '本文 ({user}, {username}, {server} が使用可能)', 3, true, { max_length: 1000 })]),
      subcommand('disable', '退出時メッセージを無効化'),
    ],
  },
  {
    name: 'autorole', description: '参加時の自動ロールを設定します', type: 1,
    options: [
      subcommand('set', '自動付与するロールを設定', [option('role', '付与するロール', 8, true)]),
      subcommand('disable', '自動ロールを無効化'),
    ],
  },
  {
    name: 'blacklist', description: '自動ロール付与のブラックリストを管理します', type: 1,
    options: [
      subcommand('add', 'ユーザーをブラックリストに追加', [option('user', '対象ユーザー', 6, true), option('reason', '理由', 3, false, { max_length: 500 })]),
      subcommand('remove', 'ユーザーをブラックリストから削除', [option('user', '対象ユーザー', 6, true)]),
    ],
  },
  {
    name: 'logs', description: 'イベントログの送信先を設定します', type: 1,
    options: [
      subcommand('set', 'ログ送信先を設定', [
        option('event', 'ログ種別', 3, true, { choices: [
          { name: 'メッセージ編集', value: 'message_edit' }, { name: 'メッセージ削除', value: 'message_delete' },
          { name: 'メンバー参加', value: 'member_join' }, { name: 'メンバー退出', value: 'member_leave' },
          { name: 'ロール変更', value: 'role_update' }, { name: '自動ロール拒否', value: 'autorole_refused' },
          { name: 'VCログ（共通）', value: 'voice' }, { name: 'VCカテゴリー別', value: 'voice_category' },
          { name: 'VCチャンネル別', value: 'voice_channel' },
        ] }),
        option('channel', 'ログ送信先チャンネル', 7, true, { channel_types: [0] }),
        option('target_id', 'VCのカテゴリーIDまたはチャンネルID（VC種別の場合）', 3, false),
      ]),
      subcommand('disable', 'ログ送信先を解除', [
        option('event', 'ログ種別', 3, true, { choices: [
          { name: 'メッセージ編集', value: 'message_edit' }, { name: 'メッセージ削除', value: 'message_delete' },
          { name: 'メンバー参加', value: 'member_join' }, { name: 'メンバー退出', value: 'member_leave' },
          { name: 'ロール変更', value: 'role_update' }, { name: '自動ロール拒否', value: 'autorole_refused' },
          { name: 'VCログ（共通）', value: 'voice' }, { name: 'VCカテゴリー別', value: 'voice_category' },
          { name: 'VCチャンネル別', value: 'voice_channel' },
        ] }),
        option('target_id', 'VCのカテゴリーIDまたはチャンネルID（VC種別の場合）', 3, false),
      ]),
    ],
  },
  {
    name: 'sticky', description: 'チャンネル下部に固定メッセージを表示します', type: 1,
    options: [
      subcommand('set', '固定メッセージを設定', [option('channel', '対象チャンネル', 7, true, { channel_types: [0] }), option('message', '固定する本文', 3, true, { max_length: 2000 })]),
      subcommand('remove', '固定メッセージを解除', [option('channel', '対象チャンネル', 7, true, { channel_types: [0] })]),
    ],
  },
  {
    name: 'role-panel', description: 'ロール付与パネルを投稿します', type: 1,
    options: [
      subcommand('create', '最大5個のロールボタンを作成', [
        option('channel', '投稿先', 7, true, { channel_types: [0] }), option('title', 'パネルの説明', 3, true, { max_length: 500 }),
        option('role1', 'ロール1', 8, true), option('label1', 'ボタン1の表示名', 3, false, { max_length: 80 }),
        option('role2', 'ロール2', 8), option('label2', 'ボタン2の表示名', 3, false, { max_length: 80 }),
        option('role3', 'ロール3', 8), option('label3', 'ボタン3の表示名', 3, false, { max_length: 80 }),
        option('role4', 'ロール4', 8), option('label4', 'ボタン4の表示名', 3, false, { max_length: 80 }),
        option('role5', 'ロール5', 8), option('label5', 'ボタン5の表示名', 3, false, { max_length: 80 }),
      ]),
    ],
  },
  {
    name: 'game-config', description: 'ポイント獲得と抽選のルールを設定します', type: 1,
    options: [
      option('points_per_message', 'メッセージ1回の獲得ポイント', 4, false, { min_value: 1, max_value: 1000 }),
      option('cooldown_seconds', '獲得クールダウン（秒）', 4, false, { min_value: 0, max_value: 86400 }),
      option('daily_limit', '1日の獲得上限', 4, false, { min_value: 1, max_value: 100000 }),
      option('lottery_cost', '抽選に必要なポイント', 4, false, { min_value: 1, max_value: 100000 }),
      option('lottery_prize', '当選時の追加ポイント', 4, false, { min_value: 0, max_value: 1000000 }),
      option('lottery_chance', '当選確率（%）', 4, false, { min_value: 0, max_value: 100 }),
    ],
  },
  { name: 'omikuji', description: '1日1回おみくじを引きます', type: 1 },
  { name: 'points', description: 'ポイント残高を確認します', type: 1 },
  { name: 'lottery', description: 'ポイントを使って抽選します', type: 1 },
  {
    name: 'shop', description: 'ポイントショップを表示・管理します', type: 1,
    options: [
      subcommand('list', '販売中の商品を表示'),
      subcommand('add', 'ロール商品を登録', [
        option('item_id', '商品ID（英数字・ハイフン）', 3, true, { max_length: 50 }),
        option('name', '商品名', 3, true, { max_length: 100 }),
        option('price', '価格', 4, true, { min_value: 1, max_value: 1000000 }),
        option('role', '購入時に付与するロール', 8, true),
      ]),
    ],
  },
  { name: 'buy', description: 'ポイントショップの商品を購入します', type: 1, options: [option('item_id', '商品ID', 3, true, { max_length: 50 })] },
];

const gameDefaults = {
  points_per_message: 1,
  cooldown_seconds: 60,
  daily_limit: 50,
  lottery_cost: 10,
  lottery_prize: 50,
  lottery_chance: 20,
};

function commandOptions(interaction: InteractionPayload): Map<string, string | number> {
  const options = interaction.data?.options ?? [];
  const sub = options.find((item) => item.type === 1);
  return new Map((sub?.options ?? options).flatMap((item) => item.value === undefined ? [] : [[item.name, item.value] as [string, string | number]]));
}

function setting<T>(settings: Record<string, unknown>, key: string, fallback: T): T {
  return (settings[key] as T | undefined) ?? fallback;
}

function optionString(options: Map<string, string | number>, name: string): string {
  const value = options.get(name);
  return value === undefined ? '' : String(value);
}

function isGuildAdmin(interaction: InteractionPayload): boolean {
  try {
    const permissions = BigInt(interaction.member?.permissions ?? '0');
    return (permissions & 8n) !== 0n || (permissions & 32n) !== 0n;
  } catch {
    return false;
  }
}

function interactionRef(interaction: InteractionWithRef): string {
  return interaction.interaction_ref;
}

export function eventGuildId(eventType: string, payload: Record<string, unknown>): string {
  if (typeof payload.guild_id === 'string') return payload.guild_id;
  if ((eventType === 'GUILD_CREATE' || eventType === 'GUILD_DELETE') && typeof payload.id === 'string') return payload.id;
  return '';
}

async function reply(client: GatewayClient, interaction: InteractionWithRef, content: string): Promise<void> {
  await client.interaction(interactionRef(interaction), 'initial', { type: 4, data: { content, flags: 64 } });
}

async function sendMessage(client: GatewayClient, channelId: string, content: string, operationId: string, components?: unknown[]): Promise<{ id: string }> {
  return client.discordOperation<{ id: string }>('send_message', { channel_id: channelId, content, ...(components ? { components } : {}) }, operationId);
}

async function sendLog(client: GatewayClient, settings: Record<string, unknown>, key: string, content: string, eventId: string): Promise<void> {
  const channelId = setting<string>(settings, `log.${key}`, '');
  if (channelId) await sendMessage(client, channelId, content, `${eventId}:log:${key}`);
}

async function respondToCommand(client: GatewayClient, interaction: InteractionWithRef): Promise<void> {
  const guildId = interaction.guild_id ?? '';
  if (!guildId) {
    await reply(client, interaction, 'このコマンドはサーバー内で使用してください。');
    return;
  }
  const commandName = interaction.data?.name ?? '';
  const options = commandOptions(interaction);
  const subcommandName = interaction.data?.options?.find((item) => item.type === 1)?.name ?? '';
  const isAdminCommand = !['omikuji', 'points', 'lottery', 'buy'].includes(commandName)
    && !(commandName === 'shop' && subcommandName === 'list');
  if (isAdminCommand && !isGuildAdmin(interaction)) {
    await reply(client, interaction, 'この操作には「サーバー管理」権限が必要です。');
    return;
  }
  const settings = await client.getStarSettings(guildId);
  const userId = interaction.member?.user?.id ?? interaction.user?.id ?? '';
  const eventId = interaction.id;
  let message: string;

  if (commandName === 'welcome' || commandName === 'goodbye') {
    const key = commandName === 'welcome' ? 'member_join' : 'member_leave';
    const channelId = optionString(options, 'channel');
    if (subcommandName === 'disable') {
      await client.setStarSetting(guildId, key, null);
      message = `${commandName === 'welcome' ? '参加' : '退出'}メッセージを無効にしました。`;
    } else {
      await client.setStarSetting(guildId, key, { channel_id: channelId, message: optionString(options, 'message') });
      message = `${commandName === 'welcome' ? '参加' : '退出'}メッセージを設定しました。`;
    }
  } else if (commandName === 'autorole') {
    if (subcommandName === 'disable') {
      await client.setStarSetting(guildId, 'autorole', null);
      message = '自動ロールを無効にしました。';
    } else {
      await client.setStarSetting(guildId, 'autorole', { role_id: optionString(options, 'role') });
      message = '自動ロールを設定しました。';
    }
  } else if (commandName === 'blacklist') {
    const targetId = optionString(options, 'user');
    if (subcommandName === 'add') {
      await client.setBlacklist(guildId, targetId, optionString(options, 'reason'));
      message = 'ユーザーを自動ロールのブラックリストに追加しました。';
    } else {
      await client.removeBlacklist(guildId, targetId);
      message = 'ユーザーをブラックリストから削除しました。';
    }
  } else if (commandName === 'logs') {
    const eventType = optionString(options, 'event');
    const targetId = optionString(options, 'target_id');
    const key = eventType === 'voice_category' ? `voice.category.${targetId}`
      : eventType === 'voice_channel' ? `voice.channel.${targetId}` : eventType;
    if ((eventType === 'voice_category' || eventType === 'voice_channel') && !targetId) {
      await reply(client, interaction, 'VCカテゴリー別・チャンネル別の設定には target_id が必要です。');
      return;
    }
    if (subcommandName === 'disable') {
      await client.setStarSetting(guildId, `log.${key}`, null);
      message = 'ログ送信先を解除しました。';
    } else {
      await client.setStarSetting(guildId, `log.${key}`, optionString(options, 'channel'));
      message = 'ログ送信先を設定しました。VCはチャンネル別設定がカテゴリー別設定より優先されます。';
    }
  } else if (commandName === 'sticky') {
    const channelId = optionString(options, 'channel');
    if (subcommandName === 'remove') {
      const current = (await client.getStickyMessages(guildId)).find((item) => item.channel_id === channelId);
      await client.removeStickyMessage(guildId, channelId);
      if (current?.message_id) {
        await client.discordOperation('delete_message', { channel_id: channelId, message_id: current.message_id }, `${eventId}:sticky-remove`);
      }
      message = '固定メッセージを解除しました。';
    } else {
      const sticky: StickyMessage = { guild_id: guildId, channel_id: channelId, message_id: '', content: optionString(options, 'message') };
      const current = (await client.getStickyMessages(guildId)).find((item) => item.channel_id === channelId);
      await client.setStickyMessage(sticky);
      const sent = await moveSticky(client, { ...sticky, message_id: current?.message_id ?? '' }, eventId);
      await client.updateStickyMessageId(sticky, '', sent.id);
      message = '固定メッセージを設定しました。メンバーが投稿するたびに最下部へ再投稿します。';
    }
  } else if (commandName === 'role-panel') {
    const channelId = optionString(options, 'channel');
    const buttons = [];
    const roleIds: string[] = [];
    for (let index = 1; index <= 5; index++) {
      const roleId = optionString(options, `role${index}`);
      if (!roleId) continue;
      roleIds.push(roleId);
      buttons.push({ type: 2, style: 2, label: optionString(options, `label${index}`) || `ロール ${index}`, custom_id: `star-role:${roleId}` });
    }
    const sent = await sendMessage(client, channelId, optionString(options, 'title'), `${eventId}:role-panel`, [{ type: 1, components: buttons }]);
    await client.setStarSetting(guildId, `role-panel:${sent.id}`, roleIds);
    message = 'ロールパネルを投稿しました。';
  } else if (commandName === 'game-config') {
    const current = { ...gameDefaults, ...setting<Record<string, number>>(settings, 'game', gameDefaults) };
    for (const key of Object.keys(gameDefaults) as Array<keyof typeof gameDefaults>) {
      const value = options.get(key);
      if (typeof value === 'number') current[key] = value;
    }
    await client.setStarSetting(guildId, 'game', current);
    message = `ゲーム設定を更新しました。メッセージ獲得 ${current.points_per_message}pt / クールダウン ${current.cooldown_seconds}秒 / 日上限 ${current.daily_limit}pt / 抽選 ${current.lottery_cost}pt、当選確率 ${current.lottery_chance}%、追加報酬 ${current.lottery_prize}pt`;
  } else if (commandName === 'omikuji') {
    let result: { fortune: string };
    try {
      result = await client.starPoints<{ fortune: string }>('omikuji', { guild_id: guildId, user_id: userId, event_id: eventId });
    } catch (error) {
      if (await respondKnownGameError(client, interaction, error)) return;
      throw error;
    }
    message = `今日のおみくじは **${result.fortune}** です。`;
  } else if (commandName === 'points') {
    const result = await client.starPoints<{ balance: number }>('balance', { guild_id: guildId, user_id: userId });
    message = `所持ポイント: **${result.balance} pt**`;
  } else if (commandName === 'lottery') {
    const game = { ...gameDefaults, ...setting<Record<string, number>>(settings, 'game', gameDefaults) };
    let result: { balance: number; won: boolean };
    try {
      result = await client.starPoints<{ balance: number; won: boolean }>('lottery', {
        guild_id: guildId, user_id: userId, event_id: eventId, cost: game.lottery_cost, prize: game.lottery_prize, chance: game.lottery_chance,
      });
    } catch (error) {
      if (await respondKnownGameError(client, interaction, error)) return;
      throw error;
    }
    message = `${result.won ? `当選！追加で ${game.lottery_prize} pt 獲得しました。` : '今回ははずれです。'} 残高: **${result.balance} pt**`;
  } else if (commandName === 'shop' && subcommandName === 'add') {
    const item: ShopItem = {
      item_id: optionString(options, 'item_id'), name: optionString(options, 'name'),
      price: Number(options.get('price')), role_id: optionString(options, 'role'),
    };
    await client.addShopItem(guildId, item);
    message = `商品「${item.name}」を登録しました。`;
  } else if (commandName === 'shop') {
    const items = await client.getShop(guildId);
    const shown = items.slice(0, 10);
    message = shown.length ? shown.map((item) => `\`${item.item_id}\` — ${item.name}: ${item.price} pt`).join('\n') : '販売中の商品はありません。';
    if (items.length > shown.length) message += `\nほか ${items.length - shown.length} 件`;
  } else if (commandName === 'buy') {
    const itemId = optionString(options, 'item_id');
    let purchase: { item: ShopItem; balance: number; role_pending: boolean };
    try {
      purchase = await client.buyShopItem(guildId, userId, itemId, eventId);
    } catch (error) {
      if (await respondKnownGameError(client, interaction, error)) return;
      throw error;
    }
    if (!purchase.role_pending) {
      message = `「${purchase.item.name}」は購入済みで、ロール付与も完了しています。残高: **${purchase.balance} pt**`;
    } else {
      try {
        // Discord's add-role endpoint is an idempotent PUT. Re-running /buy for
        // a pending inventory item retries fulfillment without charging again.
        await client.addRole(guildId, userId, purchase.item.role_id, `${eventId}:shop-role`);
        await client.completeShopItem(guildId, userId, itemId);
        message = `「${purchase.item.name}」のロールを付与しました。残高: **${purchase.balance} pt**`;
      } catch (error) {
        logger.error('shop_role_grant_failed_after_purchase', { guild_id: guildId, user_id: userId, item_id: itemId, error: errorMessage(error) });
        message = `購入は記録されていますが、ロール付与は未完了です。再度 \`/buy item_id:${itemId}\` を実行すると、追加ポイントなしで再試行できます。残高: **${purchase.balance} pt**`;
      }
    }
  } else {
    message = 'このコマンドには対応していません。';
  }
  await reply(client, interaction, message);
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function expectedGameError(error: unknown): string | undefined {
  const detail = errorMessage(error).toLowerCase();
  if (detail.includes('insufficient points')) return 'ポイントが足りません。';
  if (detail.includes('omikuji already drawn today')) return 'おみくじは本日すでに引いています。';
  if (detail.includes('shop item already owned')) return 'この商品はすでに購入済みです。';
  if (detail.includes('shop item not found')) return '商品が見つかりません。';
  if (detail.includes('no rows in result set')) return '商品が見つかりません。';
  return undefined;
}

async function respondKnownGameError(client: GatewayClient, interaction: InteractionWithRef, error: unknown): Promise<boolean> {
  const message = expectedGameError(error);
  if (!message) return false;
  await reply(client, interaction, message);
  return true;
}

async function handleComponent(client: GatewayClient, interaction: InteractionWithRef): Promise<void> {
  const guildId = interaction.guild_id ?? '';
  const userId = interaction.member?.user?.id ?? interaction.user?.id ?? '';
  const messageId = interaction.message?.id ?? '';
  const customId = interaction.data?.custom_id ?? '';
  const match = customId.match(/^star-role:(\d+)$/);
  if (!guildId || !userId || !messageId || !match) {
    await client.interaction(interactionRef(interaction), 'initial', { type: 4, data: { content: '無効なロールパネルです。', flags: 64 } });
    return;
  }
  const settings = await client.getStarSettings(guildId);
  const allowedRoles = setting<unknown>(settings, `role-panel:${messageId}`, []);
  const roleId = match[1]!;
  if (!Array.isArray(allowedRoles) || !allowedRoles.includes(roleId)) {
    await client.interaction(interactionRef(interaction), 'initial', { type: 4, data: { content: 'このロールはパネルに登録されていません。', flags: 64 } });
    return;
  }
  if (await client.isBlacklisted(guildId, userId)) {
    logger.warn('role_panel_denied_by_blacklist', { guild_id: guildId, user_id: userId, event_id: interaction.id });
    await sendLog(client, settings, 'autorole_refused', `ブラックリスト対象者のロールパネル操作を拒否しました (\`${userId}\`)`, interaction.id);
    await client.interaction(interactionRef(interaction), 'initial', { type: 4, data: { content: 'このサーバーではロールを付与できません。', flags: 64 } });
    return;
  }
  await client.interaction(interactionRef(interaction), 'initial', { type: 6 });
  try {
    await client.addRole(guildId, userId, roleId, interaction.id);
    await client.interaction(interactionRef(interaction), 'followups', { content: 'ロールを付与しました。', flags: 64 });
  } catch (error) {
    logger.error('role_panel_grant_failed', { guild_id: guildId, user_id: userId, role_id: roleId, error: errorMessage(error) });
    await client.interaction(interactionRef(interaction), 'followups', { content: 'ロールを付与できませんでした。管理者にお問い合わせください。', flags: 64 });
  }
}

async function moveSticky(client: GatewayClient, sticky: StickyMessage, eventId: string): Promise<{ id: string }> {
  const oldMessageId = sticky.message_id;
  if (oldMessageId) {
    await client.updateStickyMessageId(sticky, oldMessageId, '');
    try {
      await client.discordOperation('delete_message', { channel_id: sticky.channel_id, message_id: oldMessageId }, `${eventId}:sticky-delete`);
    } catch (error) {
      logger.warn('sticky_previous_message_delete_failed', { guild_id: sticky.guild_id, channel_id: sticky.channel_id, error: errorMessage(error) });
    }
  }
  return sendMessage(client, sticky.channel_id, sticky.content, `${eventId}:sticky-post`);
}

export function formatMemberMessage(message: string, userId: string, username: string, serverName: string): string {
  const formatted = message.replaceAll('{user}', `<@${userId}>`).replaceAll('{user_id}', userId)
    .replaceAll('{username}', username).replaceAll('{server}', serverName);
  return Array.from(formatted).slice(0, 2000).join('');
}

export function resolveVoiceLogChannel(settings: Record<string, unknown>, channelId: string, categoryId: string): string {
  const channelOverride = setting<string>(settings, `log.voice.channel.${channelId}`, '');
  const categoryOverride = categoryId ? setting<string>(settings, `log.voice.category.${categoryId}`, '') : '';
  return channelOverride || categoryOverride || setting<string>(settings, 'log.voice', '');
}

async function handleGuildMember(client: GatewayClient, event: EventEnvelope, guildId: string, payload: GuildMemberPayload): Promise<void> {
  const user = payload.user;
  if (!user?.id || user.bot) return;
  const settings = await client.getStarSettings(guildId);
  const username = user.global_name || payload.nick || user.username || user.id;
  const memberKey = `${guildId}:${user.id}`;

  if (event.event_type === 'GUILD_MEMBER_ADD') {
    if (await client.isBlacklisted(guildId, user.id)) {
      logger.warn('automatic_role_denied_by_blacklist', { guild_id: guildId, user_id: user.id, event_id: event.event_id });
      await sendLog(client, settings, 'autorole_refused', `自動ロール付与を拒否しました: ${username} (\`${user.id}\`)`, event.event_id);
    } else {
      const autoRole = setting<{ role_id?: string } | null>(settings, 'autorole', null);
      if (autoRole?.role_id) {
        try {
          await client.addRole(guildId, user.id, autoRole.role_id, `${event.event_id}:autorole`);
        } catch (error) {
          logger.error('automatic_role_grant_failed', { guild_id: guildId, user_id: user.id, role_id: autoRole.role_id, error: errorMessage(error) });
          await sendLog(client, settings, 'autorole_refused', `自動ロールを付与できませんでした: ${username} (\`${user.id}\`)`, event.event_id);
        }
      }
    }
    const welcome = setting<{ channel_id?: string; message?: string } | null>(settings, 'member_join', null);
    if (welcome?.channel_id && welcome.message) {
      await sendMessage(client, welcome.channel_id, formatMemberMessage(welcome.message, user.id, username, guildNames.get(guildId) ?? guildId), `${event.event_id}:welcome`);
    }
    memberRoles.set(memberKey, new Set(payload.roles ?? []));
    await sendLog(client, settings, 'member_join', `参加: ${username} (\`${user.id}\`)`, event.event_id);
  } else if (event.event_type === 'GUILD_MEMBER_REMOVE') {
    const goodbye = setting<{ channel_id?: string; message?: string } | null>(settings, 'member_leave', null);
    if (goodbye?.channel_id && goodbye.message) {
      await sendMessage(client, goodbye.channel_id, formatMemberMessage(goodbye.message, user.id, username, guildNames.get(guildId) ?? guildId), `${event.event_id}:goodbye`);
    }
    memberRoles.delete(memberKey);
    await sendLog(client, settings, 'member_leave', `退出: ${username} (\`${user.id}\`)`, event.event_id);
  } else {
    const nextRoles = new Set(payload.roles ?? []);
    const previousRoles = memberRoles.get(memberKey);
    memberRoles.set(memberKey, nextRoles);
    if (previousRoles) {
      const added = [...nextRoles].filter((role) => !previousRoles.has(role));
      const removed = [...previousRoles].filter((role) => !nextRoles.has(role));
      if (added.length || removed.length) {
        await sendLog(client, settings, 'role_update', `ロール変更: ${username} (\`${user.id}\`)\n追加: ${added.join(', ') || 'なし'}\n削除: ${removed.join(', ') || 'なし'}`, event.event_id);
      }
    }
  }
}

async function handleVoiceState(client: GatewayClient, event: EventEnvelope, payload: VoiceLogPayload): Promise<void> {
  if (!payload.guild_id || !payload.user_id) return;
  const key = `${payload.guild_id}:${payload.user_id}`;
  const oldChannel = voiceChannels.get(key) ?? '';
  const newChannel = payload.channel_id ?? '';
  if (newChannel) voiceChannels.set(key, newChannel);
  else voiceChannels.delete(key);
  if (oldChannel === newChannel) return;

  const settings = await client.getStarSettings(payload.guild_id);
  const targetChannel = newChannel || oldChannel;
  const categoryId = guildChannelParents.get(`${payload.guild_id}:${targetChannel}`) ?? '';
  const logChannel = resolveVoiceLogChannel(settings, targetChannel, categoryId);
  if (!logChannel) return;
  let detail = `VC移動: <@${payload.user_id}>`;
  if (!oldChannel && newChannel) detail = `VC参加: <@${payload.user_id}> → <#${newChannel}>`;
  else if (oldChannel && !newChannel) detail = `VC退出: <@${payload.user_id}> ← <#${oldChannel}>`;
  else detail += ` <#${oldChannel}> → <#${newChannel}>`;
  await sendMessage(client, logChannel, detail, `${event.event_id}:voice-log`);
}

export async function handleStarEvent(client: GatewayClient, event: EventEnvelope): Promise<void> {
  if (event.event_type === 'INTERACTION_CREATE') {
    const interaction = event.payload as unknown as InteractionWithRef;
    if (!interaction.interaction_ref) return;
    if (interaction.type === 3) return handleComponent(client, interaction);
    if (interaction.type === 2) return respondToCommand(client, interaction);
    if (interaction.type === 4) {
      await client.interaction(interaction.interaction_ref, 'initial', { type: 8, data: { choices: [] } });
    }
    return;
  }

  const payload = event.payload as Record<string, unknown>;
  const guildId = eventGuildId(event.event_type, payload);
  if (event.event_type === 'GUILD_CREATE') {
    if (typeof payload.name === 'string') guildNames.set(guildId, payload.name);
    const channels = payload.channels;
    if (Array.isArray(channels)) {
      for (const channel of channels as GuildChannel[]) {
        if ((channel.type === 2 || channel.type === 13) && channel.parent_id) guildChannelParents.set(`${guildId}:${channel.id}`, channel.parent_id);
      }
    }
    const members = payload.members;
    if (Array.isArray(members)) {
      for (const member of members as GuildMemberPayload[]) {
        if (member.user?.id) memberRoles.set(`${guildId}:${member.user.id}`, new Set(member.roles ?? []));
      }
    }
    const voiceStates = payload.voice_states;
    if (Array.isArray(voiceStates)) {
      for (const state of voiceStates as VoiceLogPayload[]) {
        if (state.user_id && state.channel_id) voiceChannels.set(`${guildId}:${state.user_id}`, state.channel_id);
      }
    }
    return;
  }
  if (event.event_type === 'GUILD_DELETE') {
    const prefix = `${guildId}:`;
    for (const key of guildChannelParents.keys()) if (key.startsWith(prefix)) guildChannelParents.delete(key);
    for (const key of memberRoles.keys()) if (key.startsWith(prefix)) memberRoles.delete(key);
    for (const key of voiceChannels.keys()) if (key.startsWith(prefix)) voiceChannels.delete(key);
    guildNames.delete(guildId);
    return;
  }
  if (event.event_type.startsWith('CHANNEL_')) {
    const channelId = String(payload.id ?? '');
    if ((payload.type === 2 || payload.type === 13) && typeof payload.parent_id === 'string') guildChannelParents.set(`${guildId}:${channelId}`, payload.parent_id);
    else guildChannelParents.delete(`${guildId}:${channelId}`);
    return;
  }
  if (event.event_type.startsWith('GUILD_MEMBER_')) {
    await handleGuildMember(client, event, guildId, payload as unknown as GuildMemberPayload);
    return;
  }
  if (event.event_type === 'VOICE_STATE_UPDATE') {
    await handleVoiceState(client, event, payload as unknown as VoiceLogPayload);
    return;
  }
  if (event.event_type === 'MESSAGE_CREATE') {
    const message = payload as unknown as { id: string; channel_id: string; author?: { id?: string; username?: string; bot?: boolean }; content?: string };
    if (!guildId || !message.author?.id || message.author.bot) return;
    const settings = await client.getStarSettings(guildId);
    const game = { ...gameDefaults, ...setting<Record<string, number>>(settings, 'game', gameDefaults) };
    await client.starPoints('earn', {
      guild_id: guildId, user_id: message.author.id, event_id: event.event_id,
      points: game.points_per_message, cooldown_seconds: game.cooldown_seconds, daily_limit: game.daily_limit,
    });
    const sticky = (await client.getStickyMessages(guildId)).find((item) => item.channel_id === message.channel_id);
    if (sticky && sticky.message_id !== message.id) {
      const sent = await moveSticky(client, sticky, event.event_id);
      await client.updateStickyMessageId(sticky, '', sent.id);
    }
    return;
  }
  if (event.event_type === 'MESSAGE_UPDATE') {
    const message = payload as unknown as { id: string; channel_id: string };
    if (guildId) {
      await sendLog(client, await client.getStarSettings(guildId), 'message_edit', `メッセージ編集: <#${message.channel_id}> / \`${message.id}\``, event.event_id);
    }
    return;
  }
  if (event.event_type === 'MESSAGE_DELETE') {
    const message = payload as unknown as { id: string; channel_id: string };
    if (!guildId) return;
    const settings = await client.getStarSettings(guildId);
    await sendLog(client, settings, 'message_delete', `メッセージ削除: <#${message.channel_id}> / \`${message.id}\``, event.event_id);
    const sticky = (await client.getStickyMessages(guildId)).find((item) => item.channel_id === message.channel_id && item.message_id === message.id);
    if (sticky) {
      const sent = await moveSticky(client, sticky, event.event_id);
      await client.updateStickyMessageId(sticky, '', sent.id);
    }
    return;
  }
  if (event.event_type === 'MESSAGE_DELETE_BULK') {
    const channelId = String(payload.channel_id ?? '');
    const ids = Array.isArray(payload.ids) ? payload.ids.map(String) : [];
    if (!guildId || !channelId) return;
    const settings = await client.getStarSettings(guildId);
    await sendLog(client, settings, 'message_delete', `${ids.length}件のメッセージを一括削除: <#${channelId}>`, event.event_id);
    const sticky = (await client.getStickyMessages(guildId)).find((item) => item.channel_id === channelId && ids.includes(item.message_id));
    if (sticky) {
      const sent = await moveSticky(client, sticky, event.event_id);
      await client.updateStickyMessageId(sticky, '', sent.id);
    }
  }
}
