# ADR 0006: 서버 언어를 Go로 한다

## Status

Accepted

## Context

조직의 모든 저장소가 TypeScript다. 백오피스도, 앱들도, PR 봇도. 그런데 플랫폼 서버는 성격이 다르다.

**결제 검증은 유저가 대기하는 경로**라 콜드스타트가 직접 체감된다. Cloud Run `min-instances=0`이 비용 전제이므로 콜드스타트를 피할 수 없다.

측정된 사실: Node 콜드스타트의 대부분은 `@google-cloud/*` 클라이언트가 **런타임에 gRPC와 protobuf 정의를 파싱**하는 비용이다.

사용자가 **Go 학습을 명시적 목표로 제시**했다.

## Decision

**서버 전체를 Go로 작성한다.**

SDK는 여전히 TypeScript와 GDScript다. Go는 서버만.

| 이득 | 내용 |
|---|---|
| 콜드스타트 | 100~300ms. **컴파일 타임 링크라 SDK 파싱 비용이 0** |
| 이미지 | 정적 바이너리 + distroless → 수 MB. pull 시간도 사라진다 |
| CI | **크로스컴파일이 네이티브.** ARC arm64 러너에서 amd64 바이너리를 QEMU 없이 수 초에 빌드. Cloud Build 위임 불필요 |
| GCP 지원 | Firestore·BigQuery·Cloud Tasks·androidpublisher 전부 1급 공식 |
| 비용 | 메모리·CPU 사용량이 낮아 무료 한도에 여유가 크다 |
| 격리성 | 플랫폼은 다른 저장소와 **코드를 공유하지 않고 경계가 API뿐**이라 언어가 달라도 침범이 없다 |
| 학습 | k8s·ARC·kubectl 도구가 전부 Go라 **이미 운영 중인 vzyx-cluster를 다룰 때 계속 쓰인다** |

### Rust를 배제한 이유

**Firestore 데이터 플레인 공식 클라이언트가 없다.** `googleapis/google-cloud-rust`에는 Admin 크레이트만 있고 문서 CRUD·쿼리·**트랜잭션**은 커뮤니티 crate에 의존해야 한다.

**IAP 원장의 멱등성이 Firestore 트랜잭션에 걸려 있는데** 이를 커뮤니티 crate에 맡길 수 없다. 학습 곡선은 Go보다 훨씬 가파른데 콜드스타트 이득은 거의 없다.

### "zod 계약 공유를 잃는다"는 반론에 대해

계약을 zod에 두면 **TypeScript만 특권적 위치**가 되고, GDScript SDK는 어차피 손으로 맞춰야 한다.

`spec/openapi.yaml`을 진짜 SoT로 올리면 셋이 **동등하게 스펙을 참조**한다. 이게 더 정직한 구조이므로 손실이 아니다. → ADR 0007

## Consequences

- **Prisma도 ORM도 없다.** repository 포트 뒤에 Firestore 클라이언트를 둔다
- 외부 의존을 최소로 한다. 라우팅은 **표준 `net/http`** — Go 1.22+ `ServeMux`가 패턴 라우팅을 지원한다. 테스트는 **표준 `testing` + 테이블 드리븐**. 절약이자 관용구 학습을 위한 선택이다
- **Apple App Store Server Library에 Go 대응이 없다.** JWS ES256 + x5c 체인 + OCSP를 커뮤니티 라이브러리나 자체 구현으로 해결해야 한다. **이 결정의 최대 리스크**이며 P0에서 방안을 확정한다 → ADR 0009
- Firebase Admin SDK for Go를 쓰지 않는다. 프로젝트별 App 인스턴스가 필요해 16개 앱에 부적합하다. `golang-jwt/jwt/v5` + JWKS 캐시로 직접 검증한다
- **Firebase Functions의 함수별 Secret 격리가 사라진다.** `PLATFORM_ROLE` 분리로 복원한다 → R3
- **6개월 뒤 유지보수가 가능해야 한다.** 학습이 중단되면 부채가 된다. `docs/09-knowledge/go/`에 관용구를 기록하고, 에이전트는 **설명 위주 + 사용자 직접 구현** 방식으로 작업한다

## Alternatives Considered

- **Node/TypeScript 유지** — 계약 공유와 조직 일관성을 얻지만 콜드스타트가 1~3초다. lazy import와 warm-up ping으로 완화할 수 있으나 근본 해결은 아니다
- **Bun/Deno** — Node 호환에 시작이 빠르지만 `@google-cloud/*`의 gRPC 네이티브 모듈 호환성이 미검증이다. 이득이 불확실한데 리스크는 확실하다
- **Java/Kotlin** — 콜드스타트가 최악이다
