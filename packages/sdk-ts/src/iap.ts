/**
 * 결제.
 *
 * 마켓 SDK는 이 패키지에 없다. 구매 증명을 만드는 것은 앱의 몫이고
 * SDK는 그 증명을 플랫폼에 넘겨 검증받는 역할만 한다.
 * 마켓 SDK를 품으면 RN과 Godot에서 각각 다른 의존성이 필요해진다.
 */

import { PlatformError, type Transport } from "./transport.ts";

export type Market = "google_play" | "app_store" | "apps_in_toss";

/** 구매 증명. 마켓 SDK가 준 값을 그대로 넘긴다. */
export interface PurchaseProof {
  platform: Market;
  productId: string;
  /**
   * 마켓별 증명값.
   *   Play:      purchaseToken
   *   App Store: transactionId
   *   AIT:       orderId
   */
  token: string;
}

/** 검증 후 클라이언트가 해야 할 후속 조치. */
export type CompletionAction =
  | "none"
  | "retry_server_completion"
  | "apps_in_toss_complete_product_grant"
  | "app_store_sync_after_sandbox_reset";

export interface VerifyOutcome {
  status: "verified" | "pending" | "revoked";
  entitlementId: string;
  /** 이번 호출로 지급됐으면 true. alreadyGranted와 배타적이다. */
  granted?: boolean;
  /** 이미 갖고 있었으면 true. */
  alreadyGranted?: boolean;
  entitlements: string[];
  completion?: { action: CompletionAction; orderId?: string };
}

/**
 * 신규 구매 전에 마켓 결제 화면에 넣을 계정 참조.
 *
 * 마켓이 검증 응답에 그대로 돌려주고, 서버가 그 값을 대조해
 * 다른 사용자가 시작한 구매를 가로채지 못하게 한다.
 */
export interface AccountReferences {
  googlePlay: string;
  appStore: string;
}

export interface IapOptions {
  transport: Transport;
  /** 세션 토큰을 가져온다. 결제는 인증이 필수다. */
  getToken: () => Promise<string>;
}

export class Iap {
  private readonly transport: Transport;
  private readonly getToken: () => Promise<string>;

  constructor(opts: IapOptions) {
    this.transport = opts.transport;
    this.getToken = opts.getToken;
  }

  /**
   * 구매를 검증하고 지급받는다.
   *
   * 재시도하지 않는다. 결제 경로에서 같은 요청을 자동으로 반복하면
   * 서버가 멱등이라 해도 응답을 기다리는 사이 사용자가 두 번
   * 결제한 것처럼 보이는 상황을 만든다. 재시도는 사용자가 정한다.
   */
  async verifyPurchase(proof: PurchaseProof): Promise<VerifyOutcome> {
    if (!proof.platform || !proof.productId || !proof.token) {
      throw new PlatformError(
        "purchase_proof_invalid",
        "구매 정보가 비어 있어요",
        400,
        true,
      );
    }

    return this.transport.request<VerifyOutcome>({
      method: "POST",
      path: "/v1/iap/verify",
      token: await this.getToken(),
      body: {
        platform: proof.platform,
        productId: proof.productId,
        token: proof.token,
      },
      noRetry: true,
    });
  }

  /**
   * 활성 entitlement 목록.
   *
   * 마켓 SDK 없이도 환불 반영을 확인할 수 있는 경로다.
   * 앱 시작 시 한 번 부르면 서버가 아는 최종 상태를 얻는다.
   */
  async listEntitlements(): Promise<string[]> {
    const res = await this.transport.request<{ entitlements: string[] }>({
      method: "GET",
      path: "/v1/iap/entitlements",
      token: await this.getToken(),
    });
    return res.entitlements ?? [];
  }

  /** 신규 구매 전에 받아갈 계정 참조. */
  async accountReferences(): Promise<AccountReferences> {
    return this.transport.request<AccountReferences>({
      method: "POST",
      path: "/v1/iap/account-references",
      token: await this.getToken(),
    });
  }
}
