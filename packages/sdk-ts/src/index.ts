/**
 * Seorilabs 공통 플랫폼 SDK.
 *
 * 앱은 이 패키지 하나만 알면 된다. 인증·이벤트·설정·결제가
 * 같은 전송 계층과 세션을 공유한다.
 *
 *   const platform = createPlatform({
 *     baseUrl: "https://platform-api-....run.app",
 *     appId: "lizard-tycoon",
 *   })
 *   await platform.signIn({ kind: "firebase-id-token", value: idToken })
 *   platform.events.track({ name: "level_complete", params: { level: 3 } })
 */

import { Config, type ConfigOptions } from "./config.ts";
import {
  Events,
  type EventContextProvider,
  type EventOutbox,
} from "./events.ts";
import { Iap } from "./iap.ts";
import {
  MemorySessionStore,
  SessionManager,
  type Credential,
  type Session,
  type SessionStore,
} from "./session.ts";
import { Transport, type TransportOptions } from "./transport.ts";
import { SDK_VERSION } from "./version.ts";

export { PlatformError } from "./transport.ts";
export { parseEnvelope, LOCAL_RESPONSE_INVALID } from "./envelope.ts";
export { normalizeParams, PII_KEYS, MAX_PARAMS } from "./normalize.ts";
export {
  backoffDelayMs,
  backoffWithJitter,
  isRetryableStatus,
  parseRetryAfterMs,
} from "./backoff.ts";
export { MemorySessionStore, SessionManager } from "./session.ts";
export { MemoryEventOutbox, Events } from "./events.ts";
export { Iap } from "./iap.ts";
export { Config } from "./config.ts";
export { SDK_VERSION } from "./version.ts";

export type { Credential, CredentialKind, Session, SessionStore } from "./session.ts";
export type {
  EventContext,
  EventContextProvider,
  EventInput,
  EventOutbox,
  EventPlatform,
} from "./events.ts";
export type {
  Market,
  PurchaseProof,
  VerifyOutcome,
  CompletionAction,
  AccountReferences,
} from "./iap.ts";
export type { RemoteConfig, ConfigTarget, SdkStatus, Maintenance } from "./config.ts";
export type { TransportOptions, RequestOptions } from "./transport.ts";
export type { ParamValue } from "./normalize.ts";

export interface PlatformOptions extends TransportOptions {
  /** 이벤트 수집 호스트. 생략하면 baseUrl을 쓴다. */
  ingestBaseUrl?: string;
  /** IAP 호스트. 생략하면 baseUrl을 쓴다. */
  iapBaseUrl?: string;
  /** 플랫폼으로 보낼 저빈도 이벤트 이름. 생략하면 모든 이벤트를 허용한다. */
  eventAllowlist?: readonly string[];
  /** 이벤트 배치에 붙일 공통 실행 환경 정보. */
  eventContext?: EventContextProvider;
  sessionStore?: SessionStore;
  eventOutbox?: EventOutbox;
  /** 이벤트 자동 전송 주기. 0이면 수동으로만 보낸다. */
  eventFlushIntervalMs?: number;
  configTtlMs?: ConfigOptions["ttlMs"];
}

/** SDK 진입점. */
export class Platform {
  readonly transport: Transport;
  readonly session: SessionManager;
  readonly events: Events;
  readonly iap: Iap;
  readonly config: Config;

  constructor(opts: PlatformOptions) {
    this.transport = new Transport(opts);
    const ingestTransport = new Transport({
      ...opts,
      baseUrl: opts.ingestBaseUrl ?? opts.baseUrl,
    });
    const iapTransport = new Transport({
      ...opts,
      baseUrl: opts.iapBaseUrl ?? opts.baseUrl,
    });

    this.session = new SessionManager(
      this.transport,
      opts.sessionStore ?? new MemorySessionStore(),
      opts.now ?? Date.now,
    );

    this.events = new Events({
      transport: ingestTransport,
      // 세션이 없어도 익명 수집이 동작해야 한다.
      getToken: async () => {
        try {
          return await this.session.token();
        } catch {
          return undefined;
        }
      },
      ...(opts.eventOutbox ? { outbox: opts.eventOutbox } : {}),
      ...(opts.eventFlushIntervalMs !== undefined
        ? { flushIntervalMs: opts.eventFlushIntervalMs }
        : {}),
      ...(opts.now ? { now: opts.now } : {}),
      ...(opts.eventAllowlist ? { allowlist: opts.eventAllowlist } : {}),
      context: () => {
        const value = typeof opts.eventContext === "function"
          ? opts.eventContext()
          : (opts.eventContext ?? {});
        return { ...value, sdkVersion: value.sdkVersion ?? SDK_VERSION };
      },
    });

    this.iap = new Iap({
      transport: iapTransport,
      // 결제는 인증이 필수다. 실패하면 그대로 던진다.
      getToken: () => this.session.token(),
    });

    this.config = new Config({
      transport: this.transport,
      ...(opts.configTtlMs !== undefined ? { ttlMs: opts.configTtlMs } : {}),
      ...(opts.now ? { now: opts.now } : {}),
    });
  }

  /** 자격증명으로 세션을 연다. */
  async signIn(credential: Credential): Promise<Session> {
    return this.session.signIn(credential);
  }

  async signOut(): Promise<void> {
    this.events.stop();
    await this.session.signOut();
  }

  /** 이벤트 자동 전송을 시작한다. */
  start(): void {
    this.events.start();
  }

  /** 종료 전에 남은 이벤트를 보낸다. */
  async shutdown(): Promise<void> {
    this.events.stop();
    await this.events.flush();
  }
}

export function createPlatform(opts: PlatformOptions): Platform {
  return new Platform(opts);
}
