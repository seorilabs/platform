# ADR 0002: seorilabs-platform은 인프라 프로젝트다

## Status

Accepted

## Context

조직에는 **"게임 1개 = 프로젝트 1개"** 규칙이 있다. 근거는 GA4 속성·Remote Config·Auth·과금이 전부 프로젝트 단위라서, 여러 제품을 한 프로젝트에 섞으면 **분석이 엉킨다**는 것이다.

또 `seorilabs-gws`는 Workspace/공용 프로젝트이며 **앱 워크로드에 쓰지 말라**고 명시되어 있다.

플랫폼은 GCP 프로젝트가 하나 필요하다. 이게 규칙 위반인가?

## Decision

**`seorilabs-platform` 프로젝트를 새로 만들되, Firebase 프로젝트로 등록하지 않는다.**

그리고 **인프라 프로젝트라는 범주를 명시적으로 추가한다.**

| 범주 | 개수 | 예 |
|---|---|---|
| 제품 프로젝트 | 앱당 1개 | `lizard-tycoon`, `happy-farm-tycoon` |
| 조직 관리 프로젝트 | 1개 | `seorilabs-gws` |
| **인프라 프로젝트 — 신규** | 조직당 소수 | `seorilabs-platform` |

### 규칙과 충돌하지 않는 이유

기존 규칙의 대상은 **제품의 Firebase 자산**이다. 플랫폼은 제품이 아니다.

- 배포되는 클라이언트가 없다
- **Firebase 프로젝트를 등록하지 않는다.** Android/iOS/Web 앱 등록도 없고, `google-services.json`을 발급하지 않는다
- GA4 속성이 없다. Remote Config도 Firebase Auth도 쓰지 않는다
- 필요한 건 **Firestore라는 GCP 제품**이지 Firebase가 아니다. `firestore.googleapis.com`만 켜면 된다
- 클라이언트는 Firestore에 직접 붙지 않고 Cloud Run REST API만 호출한다. 서버가 `cloud.google.com/go/firestore`로 ADC 접근한다

**각 앱의 Firebase 프로젝트는 그대로 유지되고 플랫폼이 흡수하지 않는다.** 오히려 앱별 Firebase Auth를 그대로 두고 토큰만 검증하는 설계를 고른 이유가 이 규칙과 충돌하지 않기 위해서다.

`seorilabs-gws`에 얹지 않는 것도 지킨다.

## Consequences

- **`seorilabs-game-provisioning` 스킬 문서에 이 예외를 한 줄 추가한다.** 안 그러면 6개월 뒤 같은 질문이 반복되고 그때는 이유를 기억하지 못한다
- Firebase 콘솔에 이 프로젝트가 나타나지 않는다. 관리는 GCP 콘솔에서 한다

### P0 실측 결과 — **검증됨** (2026-07-31)

`firestore.googleapis.com`만 활성화한 순수 GCP 프로젝트에서 Firestore Native 데이터베이스가 **정상 생성됐다.** Firebase 프로젝트 등록도, 앱 등록도 하지 않았다.

```
name:       projects/seorilabs-platform/databases/(default)
type:       FIRESTORE_NATIVE
locationId: asia-northeast3
freeTier:   true
```

`firestore.googleapis.com`을 켜면 `firebaserules.googleapis.com`과 `datastore.googleapis.com`이 의존성으로 함께 활성화된다. **이는 API 의존성이지 Firebase 프로젝트 등록이 아니다.**

절차와 함정은 `../06-release/gcp-bootstrap.md`에 있다.
- 앞으로 인프라성 GCP 프로젝트가 더 필요해지면 같은 범주로 추가한다. 다만 **조직당 소수**로 유지한다

## Alternatives Considered

- **`seorilabs-gws`에 얹기** — Workspace/공용 프로젝트를 워크로드에 쓰지 말라는 규칙과 충돌한다
- **앱 프로젝트 중 하나에 얹기** — 어느 앱을 고르든 자의적이고, 그 앱의 과금·IAM과 플랫폼이 엉킨다. "게임 1개 = 프로젝트 1개"의 취지와 정면 충돌
- **Firebase 프로젝트로 등록하고 Firestore를 쓰기** — 등록 자체가 규칙과의 충돌을 만들고, 실익이 없다. Firebase Auth도 GA4도 RC도 쓰지 않는다
