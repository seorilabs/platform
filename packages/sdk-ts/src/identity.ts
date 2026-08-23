import type { Session, SessionResponse } from "./session.ts";
import { PlatformError, Transport } from "./transport.ts";

export interface FirebaseCustomTokenResult {
  firebaseCustomToken: string;
  appUserId: string;
}

export type AccountProvider = "kakao" | "apple";

export interface AccountLinkChallenge {
  provider: AccountProvider;
  nonce: string;
  expiresAt: string;
}

export interface AccountLinkResult {
  session: Session;
  firebaseCustomToken: string;
  provider: AccountProvider;
  restored: boolean;
}

interface AccountLinkResponse {
  session: SessionResponse;
  firebaseCustomToken: string;
  provider: AccountProvider;
  restored: boolean;
}

/** Firebase bootstrap과 외부 계정 연결 HTTP 어댑터. */
export class Identity {
  constructor(
    private readonly transport: Transport,
    private readonly appId: string,
    private readonly getToken?: () => Promise<string>,
    private readonly adoptSession?: (session: SessionResponse) => Promise<Session>,
  ) {}

  firebaseCustomToken(existingFirebaseIdToken?: string): Promise<FirebaseCustomTokenResult> {
    return this.transport.request({
      method: "POST",
      path: "/v1/auth/firebase-custom-token",
      body: {
        appId: this.appId,
        ...(existingFirebaseIdToken
          ? { existingFirebaseIdToken }
          : {}),
      },
    });
  }

  async beginAccountLink(provider: AccountProvider): Promise<AccountLinkChallenge> {
    return this.transport.request({
      method: "POST",
      path: "/v1/auth/account-link-challenges",
      token: await this.platformToken(),
      body: { provider },
      noRetry: true,
    });
  }

  async completeAccountLink(
    provider: AccountProvider,
    idToken: string,
    nonce: string,
  ): Promise<AccountLinkResult> {
    const result = await this.transport.request<AccountLinkResponse>({
      method: "POST",
      path: "/v1/auth/account-links",
      token: await this.platformToken(),
      body: { provider, idToken, nonce },
      // challenge 소비와 세션 교체가 멱등이지만 로그인 SDK가 호출 정책을
      // 통제할 수 있도록 전송 계층의 자동 재시도는 하지 않는다.
      noRetry: true,
    });
    if (!this.adoptSession) {
      throw new PlatformError("auth_required", "세션 관리자가 필요해요", 401, true);
    }
    return { ...result, session: await this.adoptSession(result.session) };
  }

  private platformToken(): Promise<string> {
    if (!this.getToken) {
      throw new PlatformError("auth_required", "로그인이 필요해요", 401, true);
    }
    return this.getToken();
  }
}
