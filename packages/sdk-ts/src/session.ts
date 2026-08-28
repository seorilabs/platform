/**
 * 세션 관리.
 *
 * 플랫폼 토큰은 1시간짜리라 만료 전에 갱신해야 한다.
 * 갱신을 클라이언트 코드가 신경 쓰지 않도록 여기서 감춘다.
 */

import { PlatformError, type Transport } from "./transport.ts";

/** 자격증명 종류. */
export type CredentialKind = "firebase-id-token" | "ait-login" | "anonymous";

export interface Credential {
  kind: CredentialKind;
  value: string;
  /** ait-login에서 appLogin이 반환한 DEFAULT 또는 SANDBOX. */
  referrer?: "DEFAULT" | "SANDBOX";
}

export interface Session {
  platformToken: string;
  refreshToken: string;
  platformUserId: string;
  supportCode: string;
  appUserId: string;
  isAnonymous: boolean;
  /** 검증된 외부 계정 또는 AppsInToss 계정과 연결됐는지. */
  isLinkedAccount: boolean;
  /** 절대 만료 시각(epoch ms). */
  expiresAt: number;
}

/** 세션을 앱 재시작 후에도 유지하기 위한 저장소. */
export interface SessionStore {
  load(): Promise<Session | null>;
  save(session: Session): Promise<void>;
  clear(): Promise<void>;
}

/** 아무것도 저장하지 않는 저장소. 재시작하면 다시 로그인한다. */
export class MemorySessionStore implements SessionStore {
  private session: Session | null = null;

  async load(): Promise<Session | null> {
    return this.session;
  }
  async save(session: Session): Promise<void> {
    this.session = session;
  }
  async clear(): Promise<void> {
    this.session = null;
  }
}

/**
 * 만료 몇 초 전부터 미리 갱신할지.
 *
 * 여유가 없으면 요청이 날아가는 중에 토큰이 죽는다.
 */
const REFRESH_MARGIN_MS = 60_000;

export interface SessionResponse {
  platformToken: string;
  refreshToken: string;
  platformUserId: string;
  supportCode: string;
  appUserId: string;
  isAnonymous: boolean;
  /** 구버전 서버와의 순차 배포 동안 없을 수 있어 false로 해석한다. */
  isLinkedAccount?: boolean;
  expiresIn: number;
}

export class SessionManager {
  private readonly transport: Transport;
  private readonly store: SessionStore;
  private readonly now: () => number;

  /** 갱신이 겹치지 않게 하나로 묶는다. */
  private inflight: Promise<Session> | null = null;

  private credential: Credential | null = null;

  constructor(transport: Transport, store: SessionStore = new MemorySessionStore(), now: () => number = Date.now) {
    this.transport = transport;
    this.store = store;
    this.now = now;
  }

  /**
   * 자격증명으로 세션을 연다.
   *
   * 자격증명을 보관하는 이유는 refresh가 실패했을 때 다시 로그인하기
   * 위해서다. 앱이 매번 Firebase 토큰을 다시 받아오게 하면 Godot에서
   * 호출 앞에 왕복이 두 번씩 붙는다.
   */
  async signIn(credential: Credential): Promise<Session> {
    this.credential = credential;

    const res = await this.transport.request<SessionResponse>({
      method: "POST",
      path: "/v1/auth/session",
      body: { credential },
    });

    const session = this.toSession(res);
    await this.store.save(session);
    return session;
  }

  /**
   * 유효한 토큰을 돌려준다. 필요하면 갱신한다.
   *
   * 동시에 여러 요청이 만료된 토큰을 만나도 갱신은 한 번만 한다.
   * 갱신을 중복하면 서버가 refresh token을 회전시키는 순간
   * 나머지 요청이 무효 토큰을 들고 실패한다.
   */
  async token(): Promise<string> {
    const session = await this.store.load();

    if (session && !this.needsRefresh(session)) {
      return session.platformToken;
    }
    if (!session) {
      throw new PlatformError("auth_required", "로그인이 필요해요", 401, true);
    }

    if (!this.inflight) {
      this.inflight = this.refresh(session).finally(() => {
        this.inflight = null;
      });
    }
    const refreshed = await this.inflight;
    return refreshed.platformToken;
  }

  /** 현재 세션. 없으면 null이다. */
  async current(): Promise<Session | null> {
    return this.store.load();
  }

  /** 계정 연결 응답의 새 세션을 현재 세션으로 원자적으로 교체한다. */
  async adopt(res: SessionResponse): Promise<Session> {
    const session = this.toSession(res);
    // 이전 guest credential로 자동 재로그인하면 연결 계정이 조용히
    // guest로 강등될 수 있다. refresh 실패 시 provider 로그인을 다시 받는다.
    this.credential = null;
    await this.store.save(session);
    return session;
  }

  async signOut(): Promise<void> {
    this.credential = null;
    await this.store.clear();
  }

  private needsRefresh(session: Session): boolean {
    return this.now() + REFRESH_MARGIN_MS >= session.expiresAt;
  }

  private async refresh(session: Session): Promise<Session> {
    try {
      const res = await this.transport.request<SessionResponse>({
        method: "POST",
        path: "/v1/auth/refresh",
        body: { refreshToken: session.refreshToken },
      });
      const next = this.toSession(res);
      await this.store.save(next);
      return next;
    } catch (err) {
      // refresh가 만료됐거나 폐기됐다. 자격증명이 있으면 다시 로그인한다.
      if (this.credential && isAuthFailure(err)) {
        await this.store.clear();
        return this.signIn(this.credential);
      }
      throw err;
    }
  }

  private toSession(res: SessionResponse): Session {
    return {
      platformToken: res.platformToken,
      refreshToken: res.refreshToken,
      platformUserId: res.platformUserId,
      supportCode: res.supportCode,
      appUserId: res.appUserId,
      isAnonymous: res.isAnonymous,
      isLinkedAccount: res.isLinkedAccount ?? false,
      expiresAt: this.now() + res.expiresIn * 1000,
    };
  }
}

function isAuthFailure(err: unknown): boolean {
  return err instanceof PlatformError && (err.status === 401 || err.status === 403);
}
