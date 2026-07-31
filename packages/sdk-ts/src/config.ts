/**
 * RemoteConfig.
 *
 * kill switch, 강제 업데이트, 점검 안내를 담당한다.
 * Firebase RemoteConfig가 AIT와 Godot 런타임에서 동작하지 않아
 * 앱마다 제각각 구현하던 것을 하나로 모았다.
 */

import type { Transport } from "./transport.ts";

export interface SdkStatus {
  status: "ok" | "deprecated" | "blocked";
  updateUrl?: string;
}

export interface Maintenance {
  active: boolean;
  message?: string;
  until?: string;
}

export interface RemoteConfig {
  values: Record<string, unknown>;
  features: Record<string, boolean>;
  sdk: SdkStatus;
  maintenance: Maintenance;
  minSupportedVersion?: string;
}

export interface ConfigTarget {
  appVersion: string;
  platform: "android" | "ios" | "web";
  locale?: string;
}

export interface ConfigOptions {
  transport: Transport;
  /** 캐시 유효 시간. 서버도 max-age 60을 준다. */
  ttlMs?: number;
  now?: () => number;
}

const DEFAULT_TTL_MS = 60_000;

/**
 * 서버에 닿지 못했을 때 쓰는 값.
 *
 * **열린 상태로 둔다.** 설정을 못 읽었다고 앱을 막으면 서버 장애가
 * 전체 서비스 중단으로 번진다. 차단은 서버가 명시적으로 지시할 때만 한다.
 */
const FALLBACK: RemoteConfig = {
  values: {},
  features: {},
  sdk: { status: "ok" },
  maintenance: { active: false },
};

export class Config {
  private readonly transport: Transport;
  private readonly ttlMs: number;
  private readonly now: () => number;

  private cached: RemoteConfig | null = null;
  private cachedAt = 0;
  private etag: string | undefined;
  private inflight: Promise<RemoteConfig> | null = null;

  constructor(opts: ConfigOptions) {
    this.transport = opts.transport;
    this.ttlMs = opts.ttlMs ?? DEFAULT_TTL_MS;
    this.now = opts.now ?? Date.now;
  }

  /**
   * 설정을 가져온다. 캐시가 유효하면 네트워크를 타지 않는다.
   *
   * 실패하면 마지막 캐시를 주고, 그것도 없으면 열린 기본값을 준다.
   * 던지지 않는다 — 설정 조회 실패가 앱 시작을 막으면 안 된다.
   */
  async fetch(target: ConfigTarget): Promise<RemoteConfig> {
    if (this.cached && this.now() - this.cachedAt < this.ttlMs) {
      return this.cached;
    }

    // 동시 호출을 하나로 묶는다.
    if (!this.inflight) {
      this.inflight = this.load(target).finally(() => {
        this.inflight = null;
      });
    }
    return this.inflight;
  }

  /** 마지막으로 받은 설정. 네트워크를 타지 않는다. */
  current(): RemoteConfig {
    return this.cached ?? FALLBACK;
  }

  /**
   * 세션 응답에 함께 온 설정을 캐시에 넣는다.
   *
   * `/v1/auth/session`이 설정을 얹어 주므로 앱 시작 시 왕복이 하나 준다.
   */
  seed(config: RemoteConfig): void {
    this.cached = config;
    this.cachedAt = this.now();
  }

  private async load(target: ConfigTarget): Promise<RemoteConfig> {
    try {
      const res = await this.transport.request<RemoteConfig>({
        method: "GET",
        path: "/v1/config",
        query: {
          appVersion: target.appVersion,
          platform: target.platform,
          locale: target.locale,
        },
        headers: this.etag ? { "If-None-Match": this.etag } : {},
      });

      this.cached = res;
      this.cachedAt = this.now();
      return res;
    } catch {
      // 마지막으로 성공한 값을 유지한다.
      // 없으면 열린 기본값이다.
      return this.cached ?? FALLBACK;
    }
  }
}
