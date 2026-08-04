# QA

## P0 실측 항목

| # | 항목 | 상태 |
|---|---|---|
| 1 | **Apple JWS 검증 Go 방안 결정** ← 최우선 | **✅ 확정 — ADR 0009 Accepted** |
| 2 | Firebase 미등록 GCP 프로젝트에서 Firestore 생성 | **✅ 검증됨** |
| 3 | AIT 웹 프레임워크가 `Storage`/`getAnonymousKey`/`appLogin`을 노출하는가 | 대기 — 실제 `.ait` 빌드 필요 |
| 4 | Godot HTML shell에 추가 script를 넣은 `.ait`가 심사를 통과하는가 | 대기 — 심사 제출 필요 |
| 5 | `appLogin` 토큰의 서버 검증 API 존재 여부 | 대기 — **미확인이라 `KindAITLogin`을 fail-closed로 거부 중** |
| 6 | Cloud Run `allUsers`가 조직 DRS 정책에 막히는가 | **✅ 확인됨 — 막힌다. 우회 방법 확보** |
| 7 | Go 콜드스타트 실측 | **⚠️ 실서버 재측정 798~878ms — 목표 300ms 초과.** warm-up ping 도입 완료 |

### 결과 — 2번: Firestore ✅

`firestore.googleapis.com`만 켠 순수 GCP 프로젝트에서 Native DB가 생성됐다. `freeTier: true`, `locationId: asia-northeast3`, `(default)` 데이터베이스.

**ADR 0002와 0003의 전제가 실측으로 검증됐다.**

### 결과 — 6번: DRS ✅ (막힌다)

org policy `constraints/iam.allowedPolicyMemberDomains`가 seorilabs 디렉토리(`C02f93h8p`)만 허용해 `allUsers` 바인딩이 실패한다. lizard-tycoon이 겪은 것과 동일하다.

**우회 방법**: `gcloud run services update --no-invoker-iam-check`. invoker IAM 검사 자체를 끈다.

> **`platform-admin`에는 절대 쓰지 않는다.** private을 유지해야 Cloud Run 인프라가 앱 코드 진입 전에 거부한다.

### 결과 — 7번: 콜드스타트 ⚠️ 목표 미달

유휴 16분 후 측정. 2026-07-31.

| 구분 | 측정값 | 목표 | 판정 |
|---|---|---|---|
| **콜드** | **425ms** — TTFB 424ms, connect 49ms | 300ms | **초과** |
| warm p50 | 59ms | 50ms | 초과 |
| warm 직후 재측정 | 61~64ms | — | — |

`connect` 49ms는 TLS 핸드셰이크를 포함한 네트워크 왕복이다. 이를 빼면 **순수 서버 몫은 콜드 약 375ms, warm 약 12~15ms**다.

**측정 조건의 한계 — 반드시 재측정한다.**

이건 **표준 라이브러리만 쓴 최소 서버**의 수치다. 실제 서버는 Firestore·BigQuery 클라이언트 초기화, gRPC 연결 수립, ADC 토큰 획득이 붙어 **콜드스타트가 늘어난다.** Go는 컴파일 타임 링크라 Node만큼 폭증하지는 않지만 무시할 수 없다.

→ **P1(Firestore 연결)과 P5(마켓 클라이언트) 이후 각각 재측정한다.**

**결정: warm-up ping을 도입한다.**

계획 단계에서 "P0 실측 후 결정"으로 미뤄둔 항목이다. 콜드 425ms가 목표를 넘었고 실제 서버에서는 더 늘어날 것이므로 도입한다.

- Cloud Scheduler 5분 간격으로 `/health/live` 호출
- Cloud Run은 유휴 인스턴스를 약 15분 후 종료하므로 사실상 상시 warm
- 월 8,640 요청 — 무료 한도 200만의 0.4%. **비용 0원**
- `min-instances=1`은 서비스당 월 5~10달러라 채택하지 않는다

적용 시점은 각 서비스 배포와 함께다. `platform-iap`(결제 경로)에 가장 가치가 크다.

### 재측정 — 2026-08-02, 실서버

위에서 "P1·P5 이후 재측정"으로 미뤄둔 것을 실제 서비스에서 다시 쟀다.

**Cloud Run이 이미 기록한 `run.googleapis.com/container/startup_latencies`를 읽었다.**
리비전을 일부러 비워 콜드를 만들지 않았다 — 실서비스 중이라 유저에게 콜드를
떠넘기게 된다. 지표는 지난 7일간 실제 콜드스타트를 전부 담고 있어 표본이 더 낫다.

현재 트래픽을 받는 리비전 기준이다.

| 서비스 | p50 | p95 | 목표 | P0 대비 |
|---|---|---|---|---|
| `platform-api` | 842ms | 878ms | 300ms | **2.1배** |
| `platform-iap` | 765ms | **798ms** | 300ms | **1.9배** |
| `platform-ingest` | 575ms | 600ms | 300ms | 1.4배 |

**예측대로 늘었다.** P0의 425ms는 표준 라이브러리만 쓴 최소 서버였고, 지금은
Firestore·BigQuery 클라이언트 초기화와 ADC 토큰 획득이 붙는다. `platform-api`가
가장 느린 이유는 JWKS 최초 로드가 겹치기 때문으로 보인다.

warm은 반대로 좋다. `/health/live` 왕복이 api 98ms, iap 111ms이고 `connect`
50ms를 빼면 **서버 몫은 47~60ms**다.

> **결론: warm-up ping을 도입한다.** 재측정 결과가 목표를 2배 가까이 넘겼고,
> 결제 검증은 유저가 화면에서 기다리는 유일한 경로다. 798ms는 구매 버튼을
> 누른 뒤 체감되는 지연이다.
>
> 대상은 `platform-iap`와 `platform-api` 둘. `platform-ingest`는
> fire-and-forget이라 콜드가 유저에게 보이지 않고, `platform-admin`은
> 운영자용이라 660ms를 감수한다.

측정을 재현하려면:

```bash
curl -s -G -H "Authorization: Bearer $(gcloud auth print-access-token)" \
  -H "x-goog-user-project: seorilabs-platform" \
  "https://monitoring.googleapis.com/v3/projects/seorilabs-platform/timeSeries" \
  --data-urlencode 'filter=metric.type="run.googleapis.com/container/startup_latencies"' \
  --data-urlencode "interval.startTime=<7일 전 RFC3339>" \
  --data-urlencode "interval.endTime=<지금 RFC3339>" \
  --data-urlencode "aggregation.alignmentPeriod=604800s" \
  --data-urlencode "aggregation.perSeriesAligner=ALIGN_PERCENTILE_95"
```

`x-goog-user-project` 헤더가 필요하다. 없으면 gcloud 기본 quota 프로젝트로
붙어서 billing 오류가 난다.

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
