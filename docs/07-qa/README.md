# QA

## P0 실측 항목

| # | 항목 | 상태 |
|---|---|---|
| 1 | **Apple JWS 검증 Go 방안 결정** ← 최우선 | **✅ 확정 — ADR 0009 Accepted** |
| 2 | Firebase 미등록 GCP 프로젝트에서 Firestore 생성 | **✅ 검증됨** |
| 3 | AIT 웹 프레임워크가 `Storage`/`getAnonymousKey`/`appLogin`을 노출하는가 | 대기 |
| 4 | Godot HTML shell에 추가 script를 넣은 `.ait`가 심사를 통과하는가 | 대기 |
| 5 | `appLogin` 토큰의 서버 검증 API 존재 여부 | 대기 |
| 6 | Cloud Run `allUsers`가 조직 DRS 정책에 막히는가 | **✅ 확인됨 — 막힌다. 우회 방법 확보** |
| 7 | Go 콜드스타트 실측 | **진행 중** — warm 확보, 콜드 측정 중 |

### 결과 — 2번: Firestore ✅

`firestore.googleapis.com`만 켠 순수 GCP 프로젝트에서 Native DB가 생성됐다. `freeTier: true`, `locationId: asia-northeast3`, `(default)` 데이터베이스.

**ADR 0002와 0003의 전제가 실측으로 검증됐다.**

### 결과 — 6번: DRS ✅ (막힌다)

org policy `constraints/iam.allowedPolicyMemberDomains`가 seorilabs 디렉토리(`C02f93h8p`)만 허용해 `allUsers` 바인딩이 실패한다. lizard-tycoon이 겪은 것과 동일하다.

**우회 방법**: `gcloud run services update --no-invoker-iam-check`. invoker IAM 검사 자체를 끈다.

> **`platform-admin`에는 절대 쓰지 않는다.** private을 유지해야 Cloud Run 인프라가 앱 코드 진입 전에 거부한다.

### 결과 — 7번: 콜드스타트 (진행 중)

| 구분 | 측정값 |
|---|---|
| warm p50 | **59ms** |
| warm p90 | 62ms |
| warm max | 64ms |
| 콜드 | 측정 중 — 유휴 16분 후 |

서울 리전이고 네트워크 왕복을 포함한 수치다. 목표는 콜드 300ms 이하, warm 50ms 이하였다.

**부수 검증**: arm64 Mac에서 `GOOS=linux GOARCH=amd64`로 정적 바이너리가 QEMU 없이 빌드됐다. ADR 0006의 CI 근거가 확인됐다.

### 결과 — 1번: Apple JWS ✅

**`richzw/appstore` v1.41.0 채택 + OCSP 자체 추가.**

`cert.go` 104줄을 직접 읽어 확인했다.

- **x509 체인 검증을 실제로 수행한다** — `leafCert.Verify(opts)` 표준 라이브러리
- Apple Root CA G3를 하드코딩하고 커스텀 pool 주입도 가능
- **JWS 파싱 경로 4곳 전부가 같은 검증을 거친다**
- `getTransactionInfo`·`finishTransaction` 제공
- **OCSP만 없다** → 30~50줄로 자체 추가

체인 검증이 올바르므로 자체 구현할 이유가 사라졌다. **보안 민감 코드를 직접 쓰지 않는 쪽을 골랐다.**

원본 코드 확인 결과 **production의 OCSP는 의도적 보안 결정**이며 생략하면 기존 보안 수준을 낮춘다. 환경별 실패 처리(production 거부 / sandbox 통과)를 P5에서 구현한다.

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
