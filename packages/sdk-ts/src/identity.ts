import { Transport } from "./transport.ts";

export interface FirebaseCustomTokenResult {
  firebaseCustomToken: string;
  appUserId: string;
}

/** Firebase custom-token bridge의 공개 bootstrap 호출. */
export class Identity {
  constructor(
    private readonly transport: Transport,
    private readonly appId: string,
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
}
