# ADR 0009: Apple JWS 검증 Go 방안

## Status

**Proposed** — P0에서 라이브러리 실측 후 Accepted로 전환한다.

## Context

Apple은 App Store Server Library를 Java·Python·Node.js·Swift로 제공하고 **Go는 제공하지 않는다.**

ADR 0006(서버 언어 Go)의 최대 리스크가 이것이다.

### 대체해야 할 것 — 원본 코드 실측

`lizard-tycoon/firebase/functions/src/providers/apple-app-store.ts` 456줄을 읽어 확인했다.

**인터페이스가 이미 두 개로 분리되어 있다.**

```ts
interface AppleAppStoreApiClient {
  getTransactionInfo(transactionId: string): Promise<TransactionInfoResponse>;
  finishTransaction(transactionId: string): Promise<void>;
}

interface AppleTransactionVerifier {
  verifyAndDecodeTransaction(signedTransactionInfo: string): Promise<JWSTransactionDecodedPayload>;
  verifyAndDecodeNotification?(signedPayload: string): Promise<ResponseBodyV2DecodedPayload>;
}
```

**이 둘만 Go로 구현하면 나머지 약 300줄은 거의 그대로 이식된다.** 검증 규칙, revocation 판정, 에러 매핑, canonicalId 결정이 전부 라이브러리 바깥에 있다.

### OCSP는 의도적 보안 결정이다

원본 코드의 주석이 명시적이다.

> TestFlight/Sandbox에서만 OCSP 네트워크 장애를 견딘다. 오프라인 검증도 Apple Root CA, JWS 서명, 인증서 유효 시각, bundle/environment/product를 계속 검증한다.
> **Production은 인증서 폐기 확인을 우회하지 않도록 이 verifier를 만들지 않는다.**

`enableOnlineChecks` 기본값이 `true`이고, `RETRYABLE_VERIFICATION_FAILURE` 발생 시 오프라인 fallback은 **sandbox에서만** 동작한다. production에서는 그대로 `provider_unavailable` 503으로 매핑된다.

**따라서 "OCSP를 빼고 간다"는 선택지는 기존 보안 수준을 낮추는 것이며, 그렇게 하려면 별도 결정이 필요하다.**

### 라이브러리 평가 — 1차

| | JWS 검증 | Root CA 체인 | **OCSP** | 재시도 구분 | `getTransactionInfo` | `finishTransaction` |
|---|---|---|---|---|---|---|
| `richzw/appstore` | O | `CertPool` 있음 — **체인 검증 실제 수행 여부 확인 필요** | **X** | O — 단 API 에러용이지 JWS 검증용 아님 | **O** | **O** |
| `awa/go-iap/appstore` | O | **`ExtractPublicKeyFromToken` — x5c[0]에서 공개키만 추출하는 것으로 보임** | X | — | **X** | **X** |
| 자체 구현 | 직접 | `crypto/x509` | `x/crypto/ocsp` | 직접 | 직접 | 직접 |

**`awa/go-iap`는 부적합하다.** App Store Server API가 없어 `getTransactionInfo`와 `finishTransaction`을 쓸 수 없다. 게다가 x5c[0]에서 공개키만 뽑는다면 **공격자가 자기 인증서를 x5c에 넣고 서명한 위조 알림이 통과**한다. 체인 검증 없는 JWS 검증은 검증이 아니다.

## Decision

`확정 필요` — 아래 선택지 중 P0 실측 후 확정한다. **현재 유력안은 B다.**

### A. `richzw/appstore` 전체 사용 + OCSP 별도 추가

API 클라이언트와 JWS 검증을 라이브러리에 맡기고, 검증 후 x5c 체인을 따로 파싱해 OCSP를 덧붙인다.

- 장점: 구현량 최소
- 단점: 라이브러리가 체인 검증을 어떻게 하는지 **신뢰해야 한다.** 그리고 OCSP를 사후에 붙이면 검증 순서가 원본과 달라진다

### B. API 클라이언트는 `richzw/appstore`, **JWS 검증은 자체 구현** ← 유력

- API 클라이언트는 라이브러리가 잘 커버한다. ES256 JWT 인증, 에러 타입, 백오프가 이미 있다
- **JWS 검증은 어차피 OCSP 때문에 손봐야 하므로 자체 구현이 더 명확하다**
- 자체 구현이라 해도 우리가 쓰는 건 조립뿐이다
  - 체인 검증 → `x509.Certificate.Verify()` 표준 라이브러리
  - ES256 서명 → `crypto/ecdsa` 또는 `golang-jwt/jwt/v5`
  - OCSP → `golang.org/x/crypto/ocsp`가 요청 생성·응답 파싱·서명 검증을 전부 해준다
- 예상 규모 150~250줄

### C. 전부 자체 구현

App Store Server API도 직접 만든다. ES256 JWT 인증 + REST 호출이라 어렵지는 않지만 에러 코드 매핑 등 부가 작업이 늘어난다. 라이브러리로 얻을 수 있는 것을 굳이 다시 만들 이유가 없다.

### D. Apple만 기존 Cloud Functions 유지 — **대비책**

Play와 AppsInToss는 Go 플랫폼으로 가고 Apple 검증만 기존 Firebase Functions에 남긴다.

- A·B·C가 모두 막히면 이걸 쓴다
- 단점: 원장이 두 곳으로 갈라진다. **불변식 3(stale 억제)과 5(원장 삭제 금지)를 두 시스템이 함께 지켜야 해서 위험이 크다**
- **최후 수단이며 기본 선택지가 아니다**

## P0 확인 항목

| # | 확인할 것 | 방법 |
|---|---|---|
| 1 | `richzw/appstore`가 **실제로 x509 체인 검증**을 하는가, x5c[0] 공개키만 쓰는가 | `cert.go`, `pool.go` 소스 확인 |
| 2 | Apple Root CA를 신뢰 앵커로 명시 주입할 수 있는가 | `CertPool.Init()` 시그니처 |
| 3 | 라이브러리 활성도 — 최근 커밋, 미해결 이슈, 라이선스 | GitHub |
| 4 | `x/crypto/ocsp`로 Apple 중간 인증서의 OCSP 응답을 검증할 수 있는가 | 실제 인증서로 시험 |
| 5 | sandbox에서 OCSP 실패를 어떤 에러로 구분할 수 있는가 | `RETRYABLE_VERIFICATION_FAILURE` 대응물 설계 |

## Consequences

- **어느 안을 고르든 `AppleTransactionVerifier`와 `AppleAppStoreApiClient`에 대응하는 Go 인터페이스를 먼저 정의한다.** 그래야 구현을 바꿔도 나머지 300줄이 영향받지 않는다. 원본이 이미 그렇게 되어 있어 그대로 따른다
- **테스트를 fake verifier로 짠다.** 원본의 `dependencies` 주입 패턴을 그대로 가져오면 실제 Apple API 없이 검증 규칙을 전부 테스트할 수 있다. `providers.unit.test.ts` 744줄이 참조 원본이다
- **production에서 OCSP를 끄지 않는다.** 끄려면 이 ADR을 supersede하는 별도 결정이 필요하다
- sandbox 오프라인 fallback은 **sandbox에서만** 만든다. production verifier를 두 벌 만들지 않는다
- B를 고르면 **보안 민감 코드를 직접 작성**하게 된다. 완화책은 표준 라이브러리에 판정을 전부 위임하고 직접 파싱하지 않는 것, 그리고 골든 벡터 테스트를 필수 게이트로 두는 것이다
  - 만료된 인증서 / 체인 불일치 / 잘못된 Root CA / alg 변조 / 서명 변조 / 폐기된 인증서

## Alternatives Considered

- **`meszmate/apple-go`, `xsean2020/appstore`** — 1차 조사에서 확인했으나 `richzw`보다 활성도와 API 커버리지가 낮아 보인다. P0에서 필요하면 재검토
- **OCSP 생략** — 기존 production 보안 수준을 낮춘다. 원본이 명시적으로 거부한 선택이므로 채택하지 않는다
