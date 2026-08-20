# Release

## 배포 파이프라인

```
PR / push→main  → static-checks   golangci-lint · go vet · go test · conformance
push→main       → deploy          Cloud Build 이미지 → 여섯 대상 배포 → readback · 스모크
운영자 dispatch  → deploy          같은 commit 재배포용
운영자 dispatch  → deploy-staging  검증된 main 이미지 → `platform-api-stg` → 경계 readback
```

배포는 `production` 고정이며 병합과 함께 자동으로 돈다. 승인 게이트 대신
배포 후 검증(이미지 태그 일치, secret·환경 경계 readback, `health/ready`)이
실패하면 워크플로가 실패한다.

staging API는 `Deploy staging`을 명시적으로 dispatch할 때만 바뀐다. 입력은 이미
Artifact Registry에 존재하는 40자리 main commit SHA만 허용하고, `stg_` Firestore
prefix·`platform_stg` BigQuery dataset·별도 `platform-stg-session-secret`을 사용한다.
production 서비스나 원장에는 traffic과 설정을 쓰지 않는다.

## 러너

**ARC `seorilabs-rpi-arm64`.**

Cloud Run은 amd64 전용이고 ARC는 arm64지만, **Go는 크로스컴파일이 네이티브**라 문제가 되지 않는다.

```bash
GOOS=linux GOARCH=amd64 go build -o bin/platform ./cmd/platform
```

QEMU 크로스빌드도, Cloud Build 위임도 불필요하다. distroless 이미지에 바이너리만 얹는다.

인증은 WIF — org var `GOOGLE_WORKLOAD_IDENTITY_PROVIDER` 재사용. **SA 키를 새로 만들지 않는다.**

## 환경 분리

프로젝트를 추가하지 않고 같은 `seorilabs-platform` 안에서 나눈다.

| | staging | production |
|---|---|---|
| Cloud Run | `platform-*-stg` | `platform-*` |
| Firestore | **`(default)` DB + 컬렉션 prefix `stg_`** | `(default)` DB |
| BigQuery | `platform_stg` | `platform` |

**Firestore를 named database로 나누지 않는 이유**: 무료 할당량이 `(default)` 데이터베이스에만 적용된다. named DB는 첫 읽기부터 과금된다. → ADR 0003

CI와 로컬 개발은 **Firestore 에뮬레이터**를 쓴다.

## 서비스 배포 단위

단일 바이너리, `PLATFORM_ROLE` 환경변수로 스위치.

| role | 노출 | 특징 |
|---|---|---|
| `api` | public | session, RemoteConfig, entitlements 조회 |
| `iap` | public | 검증, 웹훅. **마켓 자격증명은 여기에만** |
| `ingest` | public | 이벤트 수집. 고QPS |
| `admin` | **`--no-allow-unauthenticated`** | 백오피스 전용 |
| `worker` | Cloud Run Job | 완료 outbox 재시도 |

`admin`을 private으로 두면 **Cloud Run 인프라가 애플리케이션 코드 진입 전에 거부**하므로, 라우팅 버그로 admin 핸들러가 노출되는 사고가 구조적으로 불가능하다.

## 콜드스타트

`min-instances`는 **전부 0**. 1로 두면 서비스당 월 5~10달러가 상시 과금된다.

### P0 실측 — 2026-07-31

| 구분 | 측정값 |
|---|---|
| 콜드 | **425ms** — 순수 서버 몫 약 375ms |
| warm | 59~64ms — 순수 서버 몫 약 12~15ms |

목표(콜드 300ms)를 넘었다. 그리고 이건 **표준 라이브러리만 쓴 최소 서버** 수치라 Firestore·BigQuery 클라이언트가 붙으면 늘어난다.

### 대응

1. **Startup CPU boost** — 켠다. `--cpu-boost`. 거의 무료이고 효과가 크다
2. **warm-up ping — 도입 완료**(2026-08-03, 아래 "적용" 참고)
   - Cloud Scheduler 5분 간격 `/health/live`
   - Cloud Run이 유휴 인스턴스를 약 15분 후 종료하므로 사실상 상시 warm
   - 월 8,640 요청으로 무료 한도의 0.4%. **비용 0원**
   - `platform-iap`(결제 경로)에 가장 가치가 크다
3. **구조적 warm-up** — 부팅 시 `/v1/auth/session`을 즉시 발사하면 결제 시점엔 이미 warm이다. **유저 트래픽 자체가 warm-up**이 된다
4. **클라이언트** — 구매 버튼에 즉시 로딩 인디케이터. Godot `HTTPRequest` 타임아웃 15초

### 재측정 — 2026-08-02, 실서버

의존성이 다 붙은 뒤의 수치다. Cloud Run이 기록한 `startup_latencies`를 읽었다
(리비전을 비워 콜드를 만들면 그 콜드를 유저가 맞는다).

| 서비스 | p50 | p95 | P0 대비 |
|---|---|---|---|
| `platform-api` | 842ms | 878ms | 2.1배 |
| `platform-iap` | 765ms | **798ms** | 1.9배 |
| `platform-ingest` | 575ms | 600ms | 1.4배 |

**예측대로 늘었다.** Firestore·BigQuery 클라이언트 초기화와 ADC 토큰 획득이
붙는다. `platform-api`가 가장 느린 것은 JWKS 최초 로드가 겹치기 때문으로 보인다.

warm은 반대로 좋다 — 서버 몫 47~60ms.

### 적용 — warm-up ping 도입 완료

목표를 2배 가까이 넘겨 도입했다.

```
platform-iap-warmup-5m   */5 * * * *  GET /health/live   → 200
platform-api-warmup-5m   */5 * * * *  GET /health/live   → 200
```

`platform-ingest`는 fire-and-forget이라 콜드가 유저에게 보이지 않고,
`platform-admin`은 운영자용이라 감수한다. 둘은 붙이지 않았다.

되돌리려면 job을 지우면 된다.

```bash
gcloud scheduler jobs delete platform-iap-warmup-5m \
  --project=seorilabs-platform --location=asia-northeast3
```

## 아티팩트

`retention-days: 3`. Go 이미지는 수 MB라 Artifact Registry 0.5GB 무료 한도에 여유가 크다.
