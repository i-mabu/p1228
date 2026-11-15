import assert from 'node:assert/strict';
import test from 'node:test';
import { eventGuildId, formatMemberMessage, resolveVoiceLogChannel, starCommands } from '../src/general/star-features.js';

test('guild ID resolution never treats a DM message ID as a guild ID', () => {
  assert.equal(eventGuildId('MESSAGE_CREATE', { id: 'message-1', channel_id: 'dm-channel' }), '');
  assert.equal(eventGuildId('GUILD_CREATE', { id: 'guild-1' }), 'guild-1');
  assert.equal(eventGuildId('MESSAGE_CREATE', { id: 'message-1', guild_id: 'guild-1' }), 'guild-1');
});

test('voice log routing prioritizes channel, then category, then default', () => {
  const settings = {
    'log.voice': 'default-log',
    'log.voice.category.category-1': 'category-log',
    'log.voice.channel.channel-1': 'channel-log',
  };
  assert.equal(resolveVoiceLogChannel(settings, 'channel-1', 'category-1'), 'channel-log');
  assert.equal(resolveVoiceLogChannel(settings, 'channel-2', 'category-1'), 'category-log');
  assert.equal(resolveVoiceLogChannel(settings, 'channel-2', ''), 'default-log');
  assert.equal(resolveVoiceLogChannel({}, 'channel-2', ''), '');
});

test('member message placeholders use current member and server values', () => {
  assert.equal(
    formatMemberMessage('Welcome {user} ({username}) to {server} [{user_id}]', '123', 'Tomo', 'Server'),
    'Welcome <@123> (Tomo) to Server [123]',
  );
  assert.equal(formatMemberMessage('x'.repeat(2100), '1', 'Tomo', 'Server').length, 2000);
});

test('member message truncation does not split Unicode surrogate pairs', () => {
  const formatted = formatMemberMessage(`${'x'.repeat(1999)}😀`, '1', 'Tomo', 'Server');
  assert.equal(Array.from(formatted).length, 2000);
  assert.equal(formatted.endsWith('😀'), true);
});

test('StarTomo slash commands have unique names and include requested features', () => {
  const names = starCommands.map((command) => command.name);
  assert.equal(new Set(names).size, names.length);
  for (const name of ['welcome', 'goodbye', 'autorole', 'blacklist', 'logs', 'sticky', 'role-panel', 'omikuji', 'points', 'lottery', 'shop', 'buy']) {
    assert.ok(names.includes(name), `missing command ${name}`);
  }
});
