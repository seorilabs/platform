# Release

## 배포 파이프라인

```
PR / push→main  → static-checks    golangci-lint · go vet · go test · conformance
push→main       → deploy-staging   자동 → 스모크
운영자 dispatch  → deploy-production  Environment 승인 게이트
```

org 표준(`main = 정적 게이트만`)보다 한 단계 보수적이다. 이 플랫폼은 20개 앱 클라이언트가 물린 공개 API이므로 프로덕션 배포에 명시적 승인을 요구한다.

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
2. **warm-up ping — 도입 확정**
   - Cloud Scheduler 5분 간격 `/health/live`
   - Cloud Run이 유휴 인스턴스를 약 15분 후 종료하므로 사실상 상시 warm
   - 월 8,640 요청으로 무료 한도의 0.4%. **비용 0원**
   - `platform-iap`(결제 경로)에 가장 가치가 크다
3. **구조적 warm-up** — 부팅 시 `/v1/auth/session`을 즉시 발사하면 결제 시점엔 이미 warm이다. **유저 트래픽 자체가 warm-up**이 된다
4. **클라이언트** — 구매 버튼에 즉시 로딩 인디케이터. Godot `HTTPRequest` 타임아웃 15초

**P1과 P5 이후 재측정한다.** 실제 의존성이 붙은 뒤의 수치가 진짜다.

## 아티팩트

`retention-days: 3`. Go 이미지는 수 MB라 Artifact Registry 0.5GB 무료 한도에 여유가 크다.
