import type {
  DiscordGatewayAdapterCreator,
  DiscordGatewayAdapterImplementerMethods,
  DiscordGatewayAdapterLibraryMethods,
} from '@discordjs/voice';
import type { GatewayClient } from '../../core/client.js';
import { logger } from '../../core/logger.js';
import type { VoiceServerPayload, VoiceStatePayload } from '../../core/types.js';

const MAX_OUTBOUND_QUEUE = 100;

type VoicePayload = { op: number; d: Record<string, unknown> };

/**
 * Custom Adapter boundary for p1228.
 * sendPayload is synchronous by design: it only appends to a bounded in-memory queue.
 * The async dispatcher transfers opcode 4 to Go; HTTP is never awaited by sendPayload.
 */
export class P1228VoiceAdapter {
  private readonly queue: VoicePayload[] = [];
  private readonly methods: DiscordGatewayAdapterLibraryMethods;
  private accepting = true;
  private draining = false;
  private readonly implementer: DiscordGatewayAdapterImplementerMethods;

  constructor(
    private readonly client: GatewayClient,
    private readonly guildId: string,
    methods: DiscordGatewayAdapterLibraryMethods,
  ) {
    this.methods = methods;
    this.implementer = {
      sendPayload: (payload: VoicePayload) => {
        if (!this.accepting || this.queue.length >= MAX_OUTBOUND_QUEUE) {
          logger.warn('voice_adapter_queue_rejected', { guild_id: this.guildId, queue_size: this.queue.length });
          return false;
        }
        if (payload.op !== 4) {
          logger.warn('voice_adapter_opcode_rejected', { guild_id: this.guildId, opcode: payload.op });
          return false;
        }
        this.queue.push(payload);
        this.kickDispatcher();
        return true;
      },
      destroy: () => this.stop(),
    };
    this.kickDispatcher();
  }

  asCreatorResult(): DiscordGatewayAdapterImplementerMethods {
    return this.implementer;
  }

  onVoiceStateUpdate(data: VoiceStatePayload): void {
    if (data.guild_id === this.guildId) this.methods.onVoiceStateUpdate(data as never);
  }

  onVoiceServerUpdate(data: VoiceServerPayload): void {
    if (data.guild_id === this.guildId) this.methods.onVoiceServerUpdate(data as never);
  }

  destroy(): void {
    this.stop();
    this.methods.destroy();
  }

  private stop(): void {
    this.accepting = false;
    this.queue.length = 0;
  }

  private fail(): void {
    this.stop();
    this.methods.destroy();
  }

  private kickDispatcher(): void {
    if (this.draining || !this.accepting) return;
    this.draining = true;
    void this.dispatchLoop();
  }

  private async dispatchLoop(): Promise<void> {
    try {
      while (this.accepting && this.queue.length > 0) {
        const payload = this.queue[0];
        if (!payload) continue;
        try {
          await this.client.voiceGatewayPayload(payload);
          this.queue.shift();
        } catch (error) {
          logger.error('voice_gateway_payload_failed', {
            guild_id: this.guildId,
            error: error instanceof Error ? error.message : String(error),
          });
          // Do not replay an already ambiguous opcode 4 automatically. Disconnect the adapter instead.
          this.fail();
          break;
        }
      }
    } finally {
      this.draining = false;
      if (this.accepting && this.queue.length > 0) this.kickDispatcher();
    }
  }
}

export const createP1228AdapterCreator = (
  client: GatewayClient,
  guildId: string,
  onCreate: (adapter: P1228VoiceAdapter) => void,
): DiscordGatewayAdapterCreator => (methods) => {
  const adapter = new P1228VoiceAdapter(client, guildId, methods);
  onCreate(adapter);
  return adapter.asCreatorResult();
};
