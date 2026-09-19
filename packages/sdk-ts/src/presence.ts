/**
 * RPI Edge 최근 활성 heartbeat.
 *
 * 이 경로는 제품 기능이 아니라 선택적 관측이다. 실패를 던지거나 outbox에
 * 저장하지 않고, 같은 heartbeat를 재시도하지 않는다.
 */

import type { RequestOptions } from "./transport.ts";

export type PresencePlatform = "android" | "ios" | "web" | "ait";

export interface PresenceContext {
  platform?: PresencePlatform;
  appVersion?: string;
}

export type PresenceContextProvider = PresenceContext | (() => PresenceContext);

interface TokenTransport {
  request<T>(options: RequestOptions): Promise<T>;
}

interface Bootstrap {
  enabled: boolean;
  token?: string;
  expiresAt?: string;
  expiresIn?: number;
  edgeUrl?: string;
  heartbeatIntervalSeconds: number;
}

export interface PresenceOptions {
  enabled: boolean;
  tokenTransport: TokenTransport;
  context: PresenceContextProvider;
  fetchImpl?: typeof fetch;
  now?: () => number;
  random?: () => number;
  timeoutMs?: number;
  setTimer?: (callback: () => void, delayMs: number) => ReturnType<typeof setTimeout>;
  clearTimer?: (timer: ReturnType<typeof setTimeout>) => void;
}

const DEFAULT_INTERVAL_MS = 60_000;
const MAX_BACKOFF_MS = 300_000;
const DEFAULT_TIMEOUT_MS = 2_000;
const JITTER_RATIO = 0.2;

export class Presence {
  private readonly enabled: boolean;
  private readonly tokenTransport: TokenTransport;
  private readonly context: PresenceContextProvider;
  private readonly fetchImpl: typeof fetch;
  private readonly now: () => number;
  private readonly random: () => number;
  private readonly timeoutMs: number;
  private readonly setTimer: NonNullable<PresenceOptions["setTimer"]>;
  private readonly clearTimer: NonNullable<PresenceOptions["clearTimer"]>;
  private readonly sessionId = newSessionID();

  private timer: ReturnType<typeof setTimeout> | null = null;
  private running = false;
  private inFlight = false;
  private failures = 0;
  private sequence = 0;
  private lastAttemptAt = 0;
  private token = "";
  private tokenExpiresAt = 0;
  private edgeUrl = "";
  private intervalMs = DEFAULT_INTERVAL_MS;
  private visibilityListener: (() => void) | null = null;

  constructor(options: PresenceOptions) {
    this.enabled = options.enabled;
    this.tokenTransport = options.tokenTransport;
    this.context = options.context;
    this.fetchImpl = options.fetchImpl ?? globalThis.fetch;
    this.now = options.now ?? Date.now;
    this.random = options.random ?? Math.random;
    this.timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    this.setTimer = options.setTimer ?? ((callback, delayMs) => setTimeout(callback, delayMs));
    this.clearTimer = options.clearTimer ?? ((timer) => clearTimeout(timer));
  }

  /** 즉시 반환한다. 첫 네트워크 요청도 호출 흐름에서 await하지 않는다. */
  start(): void {
    if (!this.enabled || this.running) return;
    this.running = true;
    this.attachVisibilityListener();
    void this.cycle();
  }

  stop(): void {
    this.running = false;
    if (this.timer) this.clearTimer(this.timer);
    this.timer = null;
    if (this.visibilityListener && typeof document !== "undefined") {
      document.removeEventListener("visibilitychange", this.visibilityListener);
    }
    this.visibilityListener = null;
  }

  /** 앱 foreground 복귀 시 호출할 수 있다. 네트워크 완료를 기다리지 않는다. */
  resume(): void {
    if (!this.running || this.inFlight || this.now() - this.lastAttemptAt < DEFAULT_INTERVAL_MS) {
      return;
    }
    if (this.timer) this.clearTimer(this.timer);
    this.timer = null;
    void this.cycle();
  }

  private async cycle(): Promise<void> {
    if (!this.running || this.inFlight || this.isHidden()) return;
    this.inFlight = true;
    this.lastAttemptAt = this.now();
    let retryAfterMs: number | undefined;
    try {
      const context = this.resolveContext();
      if (!context.platform) {
        this.failures++;
        return;
      }
      const bootstrap = await this.ensureBootstrap(context);
      if (!bootstrap.enabled) {
        this.failures++;
        retryAfterMs = bootstrap.retryAfterMs;
        return;
      }
      retryAfterMs = await this.sendHeartbeat(context);
      if (retryAfterMs === undefined) this.failures = 0;
    } catch {
      // Presence 오류는 호출자와 제품 흐름으로 전파하지 않는다.
      this.failures++;
    } finally {
      this.inFlight = false;
      if (this.running && !this.isHidden()) {
        this.schedule(retryAfterMs ?? this.nextDelayMs());
      }
    }
  }

  private async ensureBootstrap(
    context: PresenceContext,
  ): Promise<{ enabled: boolean; retryAfterMs?: number }> {
    if (this.token && this.tokenExpiresAt > this.now() + DEFAULT_INTERVAL_MS) {
      return { enabled: true };
    }
    this.token = "";
    this.edgeUrl = "";
    const bootstrap = await this.tokenTransport.request<Bootstrap>({
      method: "POST",
      path: "/v1/presence/token",
      noRetry: true,
      body: {
        sessionId: this.sessionId,
        platform: context.platform,
        ...(context.appVersion ? { appVersion: context.appVersion.slice(0, 32) } : {}),
      },
    });
    const intervalSeconds = Number(bootstrap.heartbeatIntervalSeconds);
    this.intervalMs = Number.isFinite(intervalSeconds)
      ? clamp(intervalSeconds * 1_000, DEFAULT_INTERVAL_MS, MAX_BACKOFF_MS)
      : DEFAULT_INTERVAL_MS;
    if (!bootstrap.enabled || !bootstrap.token || !bootstrap.edgeUrl) {
      return { enabled: false, retryAfterMs: MAX_BACKOFF_MS };
    }
    const expiresAt = bootstrap.expiresIn && bootstrap.expiresIn > 0
      ? this.now() + bootstrap.expiresIn * 1_000
      : Date.parse(bootstrap.expiresAt ?? "");
    if (!Number.isFinite(expiresAt)) return { enabled: false };
    this.token = bootstrap.token;
    this.tokenExpiresAt = expiresAt;
    this.edgeUrl = bootstrap.edgeUrl.replace(/\/+$/, "");
    return { enabled: true };
  }

  /** undefined는 성공, 숫자는 Retry-After다. 다른 실패는 throw해 backoff한다. */
  private async sendHeartbeat(context: PresenceContext): Promise<number | undefined> {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);
    try {
      const response = await this.fetchImpl(`${this.edgeUrl}/v1/presence/heartbeat`, {
        method: "POST",
        headers: {
          Authorization: `Bearer ${this.token}`,
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          version: 1,
          sequence: this.sequence++,
          platform: context.platform,
          ...(context.appVersion ? { appVersion: context.appVersion.slice(0, 32) } : {}),
        }),
        signal: controller.signal,
        credentials: "omit",
      });
      if (response.ok) return undefined;
      if (response.status === 401) {
        this.token = "";
        this.tokenExpiresAt = 0;
      }
      if (response.status === 429 || response.status === 503) {
        const seconds = Number(response.headers.get("Retry-After"));
        if (Number.isFinite(seconds) && seconds > 0) {
          this.failures++;
          return clamp(seconds * 1_000, DEFAULT_INTERVAL_MS, MAX_BACKOFF_MS);
        }
      }
      throw new Error(`presence heartbeat failed: ${response.status}`);
    } finally {
      clearTimeout(timer);
    }
  }

  private resolveContext(): PresenceContext {
    const value = typeof this.context === "function" ? this.context() : this.context;
    return { ...value };
  }

  private nextDelayMs(): number {
    const base = this.failures === 0
      ? this.intervalMs
      : this.failures === 1
        ? this.intervalMs
        : this.failures === 2
          ? Math.min(this.intervalMs * 2, MAX_BACKOFF_MS)
          : MAX_BACKOFF_MS;
    const jitter = base * JITTER_RATIO * (this.random() * 2 - 1);
    return clamp(Math.round(base + jitter), DEFAULT_INTERVAL_MS, MAX_BACKOFF_MS);
  }

  private schedule(delayMs: number): void {
    if (this.timer) this.clearTimer(this.timer);
    this.timer = this.setTimer(() => {
      this.timer = null;
      void this.cycle();
    }, delayMs);
    this.timer.unref?.();
  }

  private attachVisibilityListener(): void {
    if (typeof document === "undefined" || this.visibilityListener) return;
    this.visibilityListener = () => {
      if (this.isHidden()) {
        if (this.timer) this.clearTimer(this.timer);
        this.timer = null;
        return;
      }
      this.resume();
    };
    document.addEventListener("visibilitychange", this.visibilityListener);
  }

  private isHidden(): boolean {
    return typeof document !== "undefined" && document.visibilityState === "hidden";
  }
}

function newSessionID(): string {
  const cryptoApi = globalThis.crypto;
  if (cryptoApi?.randomUUID) return cryptoApi.randomUUID().replace(/-/g, "_");
  if (cryptoApi?.getRandomValues) {
    const bytes = cryptoApi.getRandomValues(new Uint8Array(24));
    return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
  }
  return `${Date.now().toString(36)}_${Math.random().toString(36).slice(2)}_presence`;
}

function clamp(value: number, minimum: number, maximum: number): number {
  return Math.min(maximum, Math.max(minimum, value));
}
