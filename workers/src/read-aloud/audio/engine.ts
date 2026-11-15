import { createHash, randomUUID } from 'node:crypto';
import { mkdir, readFile, readdir, rename, stat, unlink, utimes, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { Readable } from 'node:stream';

// ===== 1. 型定義 =====
export interface SynthesisOptions {
  text: string;
  speakerId: number;
  speed: number;
  volume: number;
  /** Settings and dictionary revisions prevent stale speech from being reused. */
  cacheRevision: string;
}

interface CacheFile {
  path: string;
  size: number;
  modifiedAt: number;
}

// ===== 2. 音声合成とキャッシュ =====
export class AudioEngineClient {
  private readonly baseURL = (process.env.P1228_AUDIO_ENGINE_URL ?? 'http://127.0.0.1:50021').replace(/\/$/, '');
  private readonly cacheDirectory = process.env.P1228_AUDIO_CACHE_DIR ?? './data/audio-cache';
  private readonly maxCacheBytes = parsePositiveInteger(process.env.P1228_AUDIO_CACHE_MAX_MB, 512) * 1024 * 1024;
  private readonly maxCacheAgeMs = parsePositiveInteger(process.env.P1228_AUDIO_CACHE_TTL_HOURS, 24) * 60 * 60 * 1000;

  async synthesize(options: SynthesisOptions): Promise<Readable> {
    if (!options.text.trim()) throw new Error('Audio text is empty');
    const normalized = {
      engine: this.baseURL,
      text: options.text,
      speakerId: options.speakerId,
      speed: clamp(options.speed, 0.5, 2.0),
      volume: clamp(options.volume, 0.0, 2.0),
      revision: options.cacheRevision,
    };
    const cacheKey = createHash('sha256').update(JSON.stringify(normalized)).digest('hex');
    const cachePath = join(this.cacheDirectory, `${cacheKey}.wav`);

    const cached = await this.readCache(cachePath);
    if (cached) return Readable.from(cached);

    const queryURL = new URL(`${this.baseURL}/audio_query`);
    queryURL.searchParams.set('text', options.text);
    queryURL.searchParams.set('speaker', String(options.speakerId));
    const queryResponse = await fetch(queryURL, { method: 'POST', signal: AbortSignal.timeout(20_000) });
    if (!queryResponse.ok) throw new Error(`Audio engine query failed: HTTP ${queryResponse.status}`);
    const query = (await queryResponse.json()) as Record<string, unknown>;
    query['speedScale'] = normalized.speed;
    query['volumeScale'] = normalized.volume;

    const synthesisURL = new URL(`${this.baseURL}/synthesis`);
    synthesisURL.searchParams.set('speaker', String(options.speakerId));
    const synthesisResponse = await fetch(synthesisURL, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(query),
      signal: AbortSignal.timeout(60_000),
    });
    if (!synthesisResponse.ok) throw new Error(`Audio engine synthesis failed: HTTP ${synthesisResponse.status}`);
    const audio = Buffer.from(await synthesisResponse.arrayBuffer());
    if (!isWav(audio)) throw new Error('Audio engine returned an empty or invalid WAV payload');

    // Large one-off files are returned but not cached. The cache is temporary and bounded.
    if (audio.byteLength <= this.maxCacheBytes) {
      await this.writeCache(cachePath, audio).catch(() => undefined);
    }
    return Readable.from(audio);
  }

  // ----- 2.1 キャッシュ管理 -----
  private async readCache(path: string): Promise<Buffer | null> {
    try {
      const info = await stat(path);
      if (!info.isFile() || info.size < 44 || info.size > this.maxCacheBytes || Date.now() - info.mtimeMs > this.maxCacheAgeMs) {
        await unlink(path).catch(() => undefined);
        return null;
      }
      const audio = await readFile(path);
      if (!isWav(audio)) return null;
      const now = new Date();
      await utimes(path, now, now).catch(() => undefined);
      return audio;
    } catch {
      return null;
    }
  }

  private async writeCache(path: string, audio: Buffer): Promise<void> {
    await mkdir(this.cacheDirectory, { recursive: true, mode: 0o770 });
    const temporary = `${path}.${process.pid}.${randomUUID()}.tmp`;
    try {
      await writeFile(temporary, audio, { mode: 0o660, flag: 'wx' });
      await rename(temporary, path);
    } finally {
      await unlink(temporary).catch(() => undefined);
    }
    await this.enforceCacheLimit(path);
  }

  private async enforceCacheLimit(protectedPath: string): Promise<void> {
    let names: string[];
    try {
      names = await readdir(this.cacheDirectory);
    } catch {
      return;
    }
    const files: CacheFile[] = [];
    for (const name of names) {
      if (!name.endsWith('.wav')) continue;
      const path = join(this.cacheDirectory, name);
      try {
        const info = await stat(path);
        if (info.isFile()) files.push({ path, size: info.size, modifiedAt: info.mtimeMs });
      } catch {
        /* Concurrent cleanup or file replacement. */
      }
    }
    const now = Date.now();
    for (const file of files) {
      if (file.path !== protectedPath && now - file.modifiedAt > this.maxCacheAgeMs) {
        await unlink(file.path).catch(() => undefined);
      }
    }
    const retained = files.filter((file) => file.path === protectedPath || now - file.modifiedAt <= this.maxCacheAgeMs).sort((a, b) => a.modifiedAt - b.modifiedAt);
    let total = retained.reduce((sum, file) => sum + file.size, 0);
    for (const file of retained) {
      if (total <= this.maxCacheBytes) break;
      if (file.path === protectedPath) continue;
      await unlink(file.path).catch(() => undefined);
      total -= file.size;
    }
  }
}

// ===== 3. ユーティリティ =====
function isWav(audio: Buffer): boolean {
  return audio.byteLength >= 44 && audio.toString('ascii', 0, 4) === 'RIFF' && audio.toString('ascii', 8, 12) === 'WAVE';
}

function parsePositiveInteger(value: string | undefined, fallback: number): number {
  const parsed = Number.parseInt(value ?? '', 10);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : fallback;
}

function clamp(value: number, minimum: number, maximum: number): number {
  if (!Number.isFinite(value)) return minimum;
  return Math.max(minimum, Math.min(maximum, value));
}
