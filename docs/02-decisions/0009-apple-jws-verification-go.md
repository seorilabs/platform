# ADR 0009: Apple JWS 검증 Go 방안

## Status

**Accepted** — 2026-07-31. P0에서 `richzw/appstore` v1.41.0 소스를 직접 읽어 확정했다.

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

| | JWS 검증 | Root CA 체인 | **OCSP** | `getTransactionInfo` | `finishTransaction` |
|---|---|---|---|---|---|
| **`richzw/appstore` v1.41.0** | O | **O — 실측 확인** | **X** | **O** | **O** |
| `awa/go-iap/appstore` | O | 공개키 추출 위주 | X | **X** | **X** |
| 자체 구현 | 직접 | `crypto/x509` | `x/crypto/ocsp` | 직접 | 직접 |

**`awa/go-iap`는 부적합하다.** App Store Server API가 없어 `getTransactionInfo`와 `finishTransaction`을 쓸 수 없다.

### 실측 결과 — `richzw/appstore` v1.41.0 소스 확인

`cert.go` 104줄을 직접 읽었다.

| 확인 사항 | 결과 |
|---|---|
| x509 체인 검증 | **실제로 수행한다.** `leafCert.Verify(opts)` — 표준 라이브러리 |
| Apple Root CA 앵커 | **G3를 PEM으로 하드코딩** + `newCert(pool)`로 커스텀 주입 가능 |
| 중간 인증서 | x5c[1:]을 `opts.Intermediates`로 구성 |
| 인증서 유효기간 | `Certificate.Verify()`가 자동 검증 |
| 적용 범위 | `extractPublicKeyFromToken` 호출 지점 4곳 — **모든 JWS 파싱 경로가 같은 검증을 거친다** |
| **OCSP** | **코드 없음** |

저장소 상태: 스타 188, MIT, 최근 push 2026-05-07, 아카이브 아님, 미해결 이슈 4건.

**체인 검증이 제대로 되므로 자체 구현할 이유가 사라졌다.** 남은 갭은 OCSP 하나뿐이고, 그것만 독립적으로 덧붙이면 된다.

## Decision

**A안을 채택한다 — `richzw/appstore`를 쓰고 OCSP만 자체 추가한다.**

소스를 직접 읽어 **체인 검증 우려가 해소**됐기 때문이다. 보안 민감 코드를 직접 쓰지 않아도 되면 그게 낫다.

```go
// AppleTransactionVerifier 구현 = 라이브러리 검증 + OCSP 추가 확인
// AppleAppStoreApiClient 구현 = 라이브러리 StoreClient 위임
```

아래는 검토한 네 안이며 A를 고른 근거는 실측 결과 절에 있다.

### A. `richzw/appstore` 전체 사용 + OCSP 별도 추가 ← **채택**

API 클라이언트와 JWS 검증을 라이브러리에 맡기고, x5c를 따로 파싱해 OCSP만 덧붙인다.

- **장점: 보안 민감 코드를 직접 쓰지 않는다.** 체인 검증이 표준 라이브러리 기반으로 이미 올바르다
- 구현량 최소. OCSP 추가분이 30~50줄
- 단점: x5c를 두 번 파싱한다. 라이브러리가 내부에서 한 번, 우리가 OCSP용으로 한 번. **낭비지만 검증 로직을 재작성하는 것보다 훨씬 싸다**

### B. API 클라이언트는 `richzw/appstore`, **JWS 검증은 자체 구현**

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

## OCSP 추가 설계 — P5에서 구현

라이브러리가 채워주지 않는 유일한 갭이다.

```
1. 라이브러리로 JWS 검증  → 체인·서명·유효기간 확인 완료
2. 같은 토큰의 x5c 를 파싱 → leaf 와 issuer 인증서 획득
3. x/crypto/ocsp 로 폐기 확인
   - ocsp.CreateRequest(leaf, issuer, nil)
   - leaf.OCSPServer[0] 로 POST
   - ocsp.ParseResponse 로 검증
4. Good 이 아니면 거부. 네트워크 실패는 환경별로 다르게 처리
```

**환경별 실패 처리가 원본 정책의 핵심이다.**

| 환경 | OCSP 네트워크 실패 시 |
|---|---|
| production | **거부.** `provider_unavailable` 503으로 매핑한다. 폐기 확인을 우회하지 않는다 |
| sandbox | **통과.** 체인·서명·유효기간은 이미 검증됐으므로 OCSP만 건너뛴다 |

이게 원본의 `RETRYABLE_VERIFICATION_FAILURE` 오프라인 fallback에 대응한다. 원본은 라이브러리가 그 상태를 알려줬지만, 우리는 **OCSP를 직접 부르므로 네트워크 실패를 그대로 구분할 수 있다.** 오히려 더 명확하다.

### P5 확인 항목

| # | 확인할 것 |
|---|---|
| 1 | Apple 서명 인증서에 `OCSPServer` 필드가 실제로 있는가 |
| 2 | `x/crypto/ocsp`로 Apple 응답을 파싱·검증할 수 있는가 — 실제 인증서로 시험 |
| 3 | OCSP 응답 캐싱 — `NextUpdate`까지 캐시해 매 웹훅마다 왕복하지 않게 한다 |
| 4 | 라이브러리 `VerifyOptions`의 `KeyUsages` 기본값이 Apple EKU와 맞는가 |

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
