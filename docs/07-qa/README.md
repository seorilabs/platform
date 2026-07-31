# QA

## P0 실측 항목 — 답을 얻어야 진행 가능

| # | 항목 | 실패 시 |
|---|---|---|
| 1 | **Apple JWS 검증 Go 방안 결정** ← 최우선 | Apple만 기존 Cloud Functions 유지하는 하이브리드 |
| 2 | Firebase 미등록 GCP 프로젝트에서 Firestore 생성·콘솔 조회 | 등록하되 앱 0개로 두거나 저장소 재검토 |
| 3 | AIT 웹 프레임워크가 `Storage`/`getAnonymousKey`/`appLogin`을 노출하는가 | Godot Web은 신원 없는 이벤트 전용으로 축소 |
| 4 | Godot HTML shell에 추가 script를 넣은 `.ait`가 심사를 통과하는가 | React shell 방식으로 전환. 비용 증가 |
| 5 | `appLogin` 토큰의 서버 검증 API 존재 여부 | AIT 결제만 후속으로 분리 |
| 6 | Cloud Run `allUsers` 바인딩이 조직 DRS 정책에 막히는가 | 배포 후 스크립트로 해제 — lizard-tycoon 선례 |
| 7 | Go 콜드스타트 실측 | 목표 초과 시 warm-up ping 도입 |

1번 판정 기준: ① x5c 체인 + Apple Root CA 검증 ② **OCSP online check** ③ `RETRYABLE_VERIFICATION_FAILURE` 구분 가능성 ④ App Store Server API 지원. 후보는 커뮤니티 라이브러리 3종과 **자체 구현**(`crypto/x509` + `golang.org/x/crypto/ocsp`).

## 검증 체크리스트

| # | 항목 | 통과 기준 | 단계 |
|---|---|---|---|
| 1 | JWT 검증 | **골든 6종 전부 거부** — 만료/aud/iss/alg 변조/kid 미존재/서명 변조 | P1 |
| 2 | identity 멱등 | 100회 동시 호출 → `platform_user_id` 1개 | P1 |
| 3 | 정규화 동등성 | **TS와 GDScript가 바이트 동일 JSON** 출력 | P2 |
| 4 | PII blocklist | 금지 키가 이벤트에서 drop | P2 |
| 5 | GA4 회귀 | DebugView 이벤트 diff = 0 | P2 |
| 6 | **IAP 불변식 12개** | **각각 테스트 1개 이상 통과** | P4 |
| 7 | grant 동시성 | `granted`와 `alreadyGranted`가 배타적 | P4 |
| 8 | stale 억제 | 늦은 grant가 환불을 되돌리지 못함 | P4 |
| 9 | 원장 보존 | 문서 삭제 0건 — outbox 제외 | P4 |
| 10 | 환경 격리 | sandbox와 production 경로 교차 0 | P4 |
| 11 | 3마켓 샌드박스 | 구매 → 검증 → 지급 → 복원 전 경로 | P5 |
| 12 | 웹훅 멱등 | 같은 알림 2회 → 1회만 처리 | P6 |
| 13 | 워커 다중 인스턴스 | 중복 완료 0 | P6 |
| 14 | **shadow 대조** | **기존 Functions와 결과 일치** | P8 |
| 15 | RC kill switch | 실제 앱 기능이 차단됨 | P3 |
| 16 | R2 준수 | 백오피스 MySQL에 런타임 유저 테이블 0개 | P7 |
| 17 | 백오피스 다운 내성 | 장애 리허설 통과 | P9 |
| 18 | 기존 화면 무손상 | `/analytics`, `/board`, `/releases` 정상 | P7 |
| 19 | coverage | 플랫폼 DAU / GA4 DAU ≥ 90% | P9 |
| 20 | 콜드스타트 | 콜드 ≤300ms, warm ≤50ms | P0 |

## shadow 대조 — P8의 핵심 안전장치

기존 Cloud Functions를 **삭제하지 않고** 두 경로에 같은 proof를 보내 결과를 대조한다. 미론칭이라 샌드박스에서 자유롭게 반복할 수 있다.

대조 항목: `granted`/`alreadyGranted`, entitlement 목록, `completion.action`, 에러 코드.

## 장애 리허설 — P9 필수

```bash
kubectl -n platform scale deploy/backoffice --replicas=0
```

이 상태에서 게임의 결제·RemoteConfig·이벤트가 **전부 정상**이어야 한다. 그다음 BREAK-GLASS 절차로 점검 모드를 켜고, 백오피스 복구 후 조작이 가능한지 확인한다.

**통과하지 못하면 백오피스를 확장한다는 전제가 무너진다.** 그때는 별도 운영 콘솔 분리를 재검토해야 한다.

## 테스트 전략

- **테이블 드리븐 + 표준 `testing`.** assert 라이브러리를 도입하지 않는다
- Firestore 에뮬레이터가 필요한 테스트는 **별도 태그로 분리**하고 기본 게이트에 넣지 않는다. ARC 러너에 Java가 있는지 미확인
- provider 테스트는 fake HTTP로. 실제 마켓 API를 CI에서 호출하지 않는다
- **에이전트가 테스트를 먼저 작성하고 사용자가 통과시킨다.** → `../../AGENTS.md`
