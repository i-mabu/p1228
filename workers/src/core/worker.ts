import { setTimeout as delay } from 'node:timers/promises';
import { GatewayClient } from './client.js';
import { logger } from './logger.js';
import type { EventEnvelope } from './types.js';

// ===== 1. イベントハンドラ型とWorker実行ループ =====
export type EventHandler = (event: EventEnvelope, client: GatewayClient) => Promise<void>;

export async function runWorker(handler: EventHandler, client = new GatewayClient()): Promise<void> {
  const abort = new AbortController();
  const shutdown = () => abort.abort();
  process.once('SIGINT', shutdown);
  process.once('SIGTERM', shutdown);

  await waitForGateway(client, abort.signal);
  await client.register();
  logger.info('worker_registered', { worker_id: client.workerID, bot_id: client.botID, instance_id: client.instanceId });

  const heartbeat = async () => {
    while (!abort.signal.aborted) {
      try {
        await client.heartbeat();
      } catch (error) {
        logger.warn('worker_heartbeat_failed', { error: errorMessage(error) });
      }
      await delay(10_000, undefined, { signal: abort.signal }).catch(() => undefined);
    }
  };
  const heartbeatTask = heartbeat();

  // ===== Idle polling backs off from 250ms to 1s while no events arrive, so a =====
  // ===== quiet Worker does not issue a constant 4 requests/second against the =====
  // ===== Gateway and SQLite. The delay resets as soon as an event is claimed. =====
  let idleDelayMs = 250;
  try {
    while (!abort.signal.aborted) {
      let event: EventEnvelope | null = null;
      try {
        event = await client.claim();
      } catch (error) {
        logger.warn('event_claim_failed', { error: errorMessage(error) });
        await delay(1_000, undefined, { signal: abort.signal }).catch(() => undefined);
        continue;
      }
      if (!event) {
        await delay(idleDelayMs, undefined, { signal: abort.signal }).catch(() => undefined);
        idleDelayMs = Math.min(idleDelayMs * 2, 1_000);
        continue;
      }
      idleDelayMs = 250;
      try {
        await handler(event, client);
        if (!event.ephemeral) await client.ack(event);
      } catch (error) {
        logger.error('event_processing_failed', { event_id: event.event_id, event_type: event.event_type, error: errorMessage(error) });
        if (!event.ephemeral) {
          try {
            await client.nack(event, errorMessage(error));
          } catch (nackError) {
            logger.error('event_nack_failed', { event_id: event.event_id, error: errorMessage(nackError) });
          }
        }
      }
    }
  } finally {
    abort.abort();
    await heartbeatTask;
    logger.info('worker_stopped', { worker_id: client.workerID, instance_id: client.instanceId });
  }
}

// ===== 2. Gateway接続待ち =====
async function waitForGateway(client: GatewayClient, signal: AbortSignal): Promise<void> {
  let backoff = 1_000;
  while (!signal.aborted) {
    try {
      const response = await fetch(`${client.baseURL}/internal/v1/health`, { signal: AbortSignal.timeout(3_000) });
      if (response.ok) return;
      throw new Error(`Gateway health returned ${response.status}`);
    } catch (error) {
      logger.warn('gateway_not_ready', { error: errorMessage(error), retry_ms: backoff });
      await delay(backoff, undefined, { signal }).catch(() => undefined);
      backoff = Math.min(backoff * 2, 30_000);
    }
  }
  throw new Error('Worker start aborted before Gateway became ready');
}

// ===== 3. エラーユーティリティ =====
export function errorMessage(error: unknown): string {
  if (error instanceof Error) return error.message;
  return String(error);
}
