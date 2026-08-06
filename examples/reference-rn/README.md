# React Native 레퍼런스 — 플랫폼 SDK 붙이기

RN 앱이 플랫폼을 쓰는 최소 형태.

## 1. 설치

```jsonc
// package.json
{
  "dependencies": {
    "@seorilabs/platform-sdk": "0.1.0"
  }
}
```

private GitHub Packages이므로 소비 저장소와 CI 모두 `read:packages` 인증이
필요하다. 저장소에는 `@seorilabs:registry=https://npm.pkg.github.com`만 두고
토큰은 파일이나 번들에 넣지 않는다.

## 2. 조립

```ts
import { createPlatform } from "@seorilabs/platform-sdk";

export const platform = createPlatform({
  baseUrl: "https://platform-api-….run.app",
  ingestBaseUrl: "https://platform-ingest-….run.app",
  iapBaseUrl: "https://platform-iap-….run.app",
  appId: "my-app",
  eventAllowlist: ["seori_session_start", "onboarding_complete"],
  eventContext: {
    platform: "android",
    appVersion: "1.0.0",
    locale: "ko-KR",
  },
  sessionStore: new AsyncStorageSessionStore(),
});

platform.start();  // 이벤트 자동 전송 시작
```

세 URL은 Secret이 아니다. 별도 이벤트·IAP URL을 생략하면 하위 호환을 위해
`baseUrl`을 사용한다.

### 세션 저장소

기본은 메모리라 앱을 껐다 켜면 다시 로그인한다. 유지하려면
`SessionStore`를 구현한다.

```ts
import AsyncStorage from "@react-native-async-storage/async-storage";
import type { Session, SessionStore } from "@seorilabs/platform-sdk";

const KEY = "seori.platform.session";

class AsyncStorageSessionStore implements SessionStore {
  async load(): Promise<Session | null> {
    const raw = await AsyncStorage.getItem(KEY);
    return raw ? (JSON.parse(raw) as Session) : null;
  }
  async save(session: Session): Promise<void> {
    await AsyncStorage.setItem(KEY, JSON.stringify(session));
  }
  async clear(): Promise<void> {
    await AsyncStorage.removeItem(KEY);
  }
}
```

> **주의**: `refreshToken`이 평문으로 저장된다. 탈취되면 세션을 계속
> 갱신할 수 있다. 민감한 앱이라면 Keychain·Keystore를 쓴다.

## 3. 로그인

```ts
import auth from "@react-native-firebase/auth";

const idToken = await auth().currentUser?.getIdToken();
if (!idToken) throw new Error("Firebase 로그인이 먼저다");

await platform.signIn({ kind: "firebase-id-token", value: idToken });
```

AppsInToss에서는 Firebase ID 토큰이 없다. `appLogin()` 결과를 쓴다.

```ts
await platform.signIn({ kind: "ait-login", value: aitLoginToken });
```

**익명은 결제할 수 없다.** 조회와 이벤트만 된다.

## 4. 이벤트

```ts
platform.events.track({
  name: "level_complete",
  params: { level: 3, is_new: true },
});
```

즉시 보내지 않는다. 20건이 쌓이거나 주기가 되면 배치로 나간다.
실패한 배치는 outbox에 남아 다음 주기에 다시 시도한다.

**던지지 않는다.** 계측 때문에 화면이 멈추면 안 된다.

앱이 백그라운드로 갈 때 남은 것을 보낸다.

```ts
import { AppState } from "react-native";

AppState.addEventListener("change", (state) => {
  if (state === "background") void platform.events.flush();
});
```

### 정규화 규칙

SDK가 알아서 하지만 알고 있으면 좋다.

| 입력 | 출력 |
|---|---|
| `true` / `false` | `1` / `0` |
| `NaN`, `Infinity` | `0` |
| `null`, `undefined` | 파라미터 자체를 버림 |
| 객체, 배열 | **버림** (문자열로 바꾸지 않는다) |
| 100자 초과 문자열 | 잘림 |
| PII 키 (`email`, `phone`, ...) | 버림 |

객체를 조용히 `JSON.stringify`하면 의도치 않은 정보가 통째로 실려 나간다.

## 5. 결제

```ts
import { PlatformError } from "@seorilabs/platform-sdk";

try {
  const outcome = await platform.iap.verifyPurchase({
    platform: "google_play",
    productId: "gecko_galaxy",
    token: purchase.purchaseToken,   // react-native-iap이 준 값
  });

  applyEntitlements(outcome.entitlements);
} catch (err) {
  if (err instanceof PlatformError) {
    handlePurchaseError(err.code);
  }
}
```

신규 구매 전에 계정 참조를 마켓 결제 화면에 넣는다. 그래야 다른
사용자가 시작한 구매를 가로채지 못한다.

```ts
const refs = await platform.iap.accountReferences();
await RNIap.requestPurchase({
  sku,
  obfuscatedAccountIdAndroid: refs.googlePlay,
  appAccountToken: refs.appStore,
});
```

## 6. 오류 처리

`code`로만 분기한다.

```ts
function handlePurchaseError(code: string): void {
  switch (code) {
    case "anonymous_not_allowed":
      return promptLogin();
    case "purchase_owned_by_another_user":
      return showOtherAccountNotice();
    case "product_not_allowed":
      return showUnavailableProduct();
    case "network_error":
      return showRetryPrompt();
    default:
      return showGenericError();
  }
}
```

`err.local`이 `true`면 서버에 닿지 못한 것이다.

## 7. RemoteConfig

```ts
const config = await platform.config.fetch({
  appVersion: DeviceInfo.getVersion(),
  platform: Platform.OS === "ios" ? "ios" : "android",
});

if (config.maintenance.active) return <MaintenanceScreen />;
if (config.sdk.status === "blocked") return <ForceUpdateScreen />;
```

**실패하면 열린 기본값을 준다.** 설정을 못 읽었다고 앱을 막으면
서버 장애가 전체 중단으로 번진다. 차단은 서버가 명시할 때만 한다.

## 8. 종료

```ts
await platform.shutdown();   // 남은 이벤트를 보내고 타이머를 멈춘다
```

## 관련

- 계약: `spec/conformance/*.json`
- 서버 API: `spec/openapi.yaml`
