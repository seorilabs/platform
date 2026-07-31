/**
 * 이벤트 수집.
 *
 * 이벤트는 fire-and-forget이다. 전송 실패가 게임 진행을 막으면 안 된다.
 * 그래서 버퍼에 모았다가 배치로 보내고, 실패하면 outbox에 남긴다.
 */

import { normalizeParams, type ParamValue } from "./normalize.ts";
import type { Transport } from "./transport.ts";

export interface EventInput {
  name: string;
  params?: Record<string, unknown>;
}

interface NormalizedEvent {
  name: string;
  params: Record<string, ParamValue>;
  /** 클라이언트가 이벤트를 만든 시각(epoch ms). */
  clientTimestamp: number;
}

/** 실패한 배치를 담아두는 저장소. */
export interface EventOutbox {
  push(events: NormalizedEvent[]): Promise<void>;
  drain(max: number): Promise<NormalizedEvent[]>;
  size(): Promise<number>;
}

/**
 * 메모리 outbox.
 *
 * 앱을 껐다 켜면 사라진다. 이벤트는 정확한 카운트를 요구하지 않으므로
 * 기본값으로 충분하다. 영속이 필요하면 SessionStore처럼 갈아끼운다.
 */
export class MemoryEventOutbox implements EventOutbox {
  private queue: NormalizedEvent[] = [];
  private readonly limit: number;

  constructor(limit = 500) {
    this.limit = limit;
  }

  async push(events: NormalizedEvent[]): Promise<void> {
    this.queue.push(...events);
    // 상한을 넘으면 오래된 것부터 버린다.
    // 무한히 쌓으면 오프라인이 길어질 때 메모리를 다 쓴다.
    if (this.queue.length > this.limit) {
      this.queue = this.queue.slice(this.queue.length - this.limit);
    }
  }

  async drain(max: number): Promise<NormalizedEvent[]> {
    return this.queue.splice(0, max);
  }

  async size(): Promise<number> {
    return this.queue.length;
  }
}

/** 한 번에 보낼 최대 이벤트 수. */
const MAX_BATCH = 20;

/** 자동 전송 주기. */
const DEFAULT_FLUSH_MS = 10_000;

export interface EventsOptions {
  transport: Transport;
  /** 세션 토큰을 가져온다. 익명 수집도 허용하므로 실패해도 된다. */
  getToken?: () => Promise<string | undefined>;
  outbox?: EventOutbox;
  flushIntervalMs?: number;
  now?: () => number;
}

export class Events {
  private readonly transport: Transport;
  private readonly getToken: (() => Promise<string | undefined>) | undefined;
  private readonly outbox: EventOutbox;
  private readonly flushIntervalMs: number;
  private readonly now: () => number;

  private buffer: NormalizedEvent[] = [];
  private timer: ReturnType<typeof setInterval> | null = null;
  private flushing = false;

  constructor(opts: EventsOptions) {
    this.transport = opts.transport;
    this.getToken = opts.getToken;
    this.outbox = opts.outbox ?? new MemoryEventOutbox();
    this.flushIntervalMs = opts.flushIntervalMs ?? DEFAULT_FLUSH_MS;
    this.now = opts.now ?? Date.now;
  }

  /**
   * 이벤트를 기록한다. 즉시 보내지 않는다.
   *
   * 던지지 않는다. 계측 때문에 게임이 멈추면 안 된다.
   */
  track(event: EventInput): void {
    if (!event.name) {
      return;
    }
    this.buffer.push({
      name: event.name,
      params: normalizeParams(event.params ?? {}),
      clientTimestamp: this.now(),
    });

    if (this.buffer.length >= MAX_BATCH) {
      void this.flush();
    }
  }

  /** 주기적 전송을 시작한다. */
  start(): void {
    if (this.timer) {
      return;
    }
    this.timer = setInterval(() => void this.flush(), this.flushIntervalMs);
    // Node에서 타이머가 프로세스를 붙잡지 않게 한다.
    this.timer.unref?.();
  }

  /** 주기적 전송을 멈춘다. 남은 것은 flush로 직접 보낸다. */
  stop(): void {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  /**
   * 버퍼와 outbox를 보낸다.
   *
   * 실패하면 outbox로 되돌린다. 던지지 않는다.
   */
  async flush(): Promise<void> {
    // 동시 전송을 막는다. 겹치면 같은 이벤트가 두 번 나간다.
    if (this.flushing) {
      return;
    }
    this.flushing = true;

    try {
      const pending = this.buffer;
      this.buffer = [];

      const retried = await this.outbox.drain(MAX_BATCH);
      const batch = [...retried, ...pending].slice(0, MAX_BATCH);

      // 상한을 넘긴 나머지는 다시 버퍼에 남긴다.
      const overflow = [...retried, ...pending].slice(MAX_BATCH);
      if (overflow.length > 0) {
        await this.outbox.push(overflow);
      }

      if (batch.length === 0) {
        return;
      }

      try {
        await this.send(batch);
      } catch {
        // 실패한 배치는 outbox로. 다음 주기에 다시 시도한다.
        await this.outbox.push(batch);
      }
    } finally {
      this.flushing = false;
    }
  }

  private async send(batch: NormalizedEvent[]): Promise<void> {
    const token = await this.resolveToken();

    await this.transport.request({
      method: "POST",
      path: "/v1/events",
      token,
      body: { events: batch },
    });
  }

  private async resolveToken(): Promise<string | undefined> {
    if (!this.getToken) {
      return undefined;
    }
    try {
      return await this.getToken();
    } catch {
      // 세션이 없어도 익명 수집이 가능하다.
      return undefined;
    }
  }
}
