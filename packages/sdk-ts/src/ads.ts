import { PlatformError, type Transport } from "./transport.ts";

export type AdProvider = "admob" | "apps_in_toss";
export type AdClaimState = "accepted" | "confirmed" | "delivered" | "expired";
export type AdAssurance = "pending" | "server_verified" | "client_confirmed";
export interface AdsPolicy {
  appUsesAds: boolean;
  adsEnabled: boolean;
  disabledBy: Array<"operator" | "ad_free">;
  checkedAt: string;
}
export interface AdReward { key: string; amount: number }
export interface AdRewardClaim {
  claimId: string;
  appId: string;
  placement: string;
  provider: AdProvider;
  clientPlatform: "android" | "ios" | "apps_in_toss";
  reward: AdReward;
  state: AdClaimState;
  assurance: AdAssurance;
  createdAt: string;
  confirmedAt?: string;
  acknowledgedAt?: string;
  expiresAt: string;
  admobSsv?: { customData: string; userId: string };
}
export interface CreateAdRewardClaim {
  requestId: string;
  placement: string;
  provider: AdProvider;
  clientPlatform: "android" | "ios" | "apps_in_toss";
  reward: AdReward;
}

export class Ads {
  constructor(
    private readonly transport: Transport,
    private readonly getToken: () => Promise<string>,
  ) {}

  /** 조회 실패를 허용으로 바꾸지 않는다. 호출자는 error에서 load/show를 중단한다. */
  async policy(): Promise<AdsPolicy> {
    return this.transport.request<AdsPolicy>({
      method: "GET", path: "/v1/ads/policy", token: await this.getToken(),
    });
  }

  async createClaim(input: CreateAdRewardClaim): Promise<AdRewardClaim> {
    if (!input.requestId || !input.placement || !input.provider ||
        !input.reward?.key || input.reward.amount <= 0) {
      throw new PlatformError("request_invalid", "광고 claim 정보가 올바르지 않아요", 400, true);
    }
    return this.transport.request<AdRewardClaim>({
      method: "POST", path: "/v1/ads/reward-claims", token: await this.getToken(),
      body: input, noRetry: true,
    });
  }

  async claim(claimId: string): Promise<AdRewardClaim> {
    return this.transport.request<AdRewardClaim>({
      method: "GET", path: `/v1/ads/reward-claims/${encodeURIComponent(claimId)}`,
      token: await this.getToken(),
    });
  }

  async confirm(claimId: string, transactionId: string): Promise<AdRewardClaim> {
    if (!transactionId) {
      throw new PlatformError("request_invalid", "광고 transaction ID가 필요해요", 400, true);
    }
    return this.transport.request<AdRewardClaim>({
      method: "POST", path: `/v1/ads/reward-claims/${encodeURIComponent(claimId)}/confirm`,
      token: await this.getToken(), body: { transactionId }, noRetry: true,
    });
  }

  async ack(claimId: string): Promise<AdRewardClaim> {
    return this.transport.request<AdRewardClaim>({
      method: "POST", path: `/v1/ads/reward-claims/${encodeURIComponent(claimId)}/ack`,
      token: await this.getToken(), noRetry: true,
    });
  }
}
