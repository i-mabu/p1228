// ===== 1. 秘匿情報のマスキング =====
const secretKeys = /token|secret|authorization|password|content|message_body/i;

function sanitize(value: unknown, key = ''): unknown {
  if (secretKeys.test(key)) return '[REDACTED]';
  // ===== Keep the key context: an array stored under a sensitive key (e.g. =====
  // ===== "tokens": [...]) must have its elements redacted too. =====
  if (Array.isArray(value)) return value.map((item) => sanitize(item, key));
  if (value && typeof value === 'object') {
    return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, sanitize(v, k)]));
  }
  return value;
}

function sanitizeFields(fields: Record<string, unknown>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(fields).map(([key, value]) => [key, sanitize(value, key)]));
}

// ===== 2. ロガーの公開インターフェース =====
export const logger = {
  info(event: string, fields: Record<string, unknown> = {}) {
    write('INFO', event, fields);
  },
  warn(event: string, fields: Record<string, unknown> = {}) {
    write('WARN', event, fields);
  },
  error(event: string, fields: Record<string, unknown> = {}) {
    write('ERROR', event, fields);
  },
};

// ===== 3. ログ出力 =====
function write(level: string, event: string, fields: Record<string, unknown>): void {
  const line = JSON.stringify({ timestamp: new Date().toISOString(), level, service: process.env.P1228_WORKER_ID ?? 'worker', event, ...sanitizeFields(fields) });
  process.stdout.write(`${line}\n`);
}
