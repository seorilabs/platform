# ADR 0001: 백오피스를 확장하고 분리하지 않는다

## Status

Accepted

## Context

플랫폼에는 운영 UI가 필요하다. 공지·메시지·entitlement 조회·지급·회수를 사람이 조작해야 한다.

선택지는 둘이었다.

**분리 논거**: 기존 `seorilabs-backoffice`는 RPI k8s에 있고 플랫폼은 GCP에 있다. 백오피스가 다운돼도 앱은 살아야 하고, 반대로 백오피스가 GCP의 플랫폼 데이터에 접근하려면 네트워크·인증 경로가 새로 필요하다. 또 백오피스의 "GitHub = SoT, 단방향 미러" 원칙은 런타임 유저 데이터에 적용할 수 없다.

**확장 논거**: 앱 레지스트리 자동스캔, 인증과 allowlist, `AuditLog`, 텔레그램 ChatOps 17파일 3,191줄, 앱 워크스페이스 IA, manifest 계약 300줄, 8개 언어 로케일, Gemini 클라이언트, 배포 파이프라인이 **이미 전부 있다.** 그리고 **`commerce`(결제·IAP) 탭이 이미 정의되어 있다.**

## Decision

**기존 `seorilabs-backoffice`를 확장한다.**

분리의 진짜 논거였던 "가용성"은 배포 위치가 아니라 **백오피스가 런타임 경로에 있는가**의 문제다. 이건 아키텍처 규칙으로 푼다.

- **R1** 백오피스는 플랫폼 Admin API의 클라이언트일 뿐이며 클라이언트 요청 경로에 들어가지 않는다
- **R2** 백오피스 MySQL은 런타임 유저 데이터를 0바이트도 저장하지 않는다. 미러조차 만들지 않는다

"GitHub = SoT" 원칙의 본질은 "GitHub이 최고다"가 아니라 **"이 앱 DB는 원본이 아니다"**이며, 이건 그대로 일반화된다. 플랫폼이 런타임 데이터의 SoT이고 백오피스는 API로 읽고 쓴다. GitHub은 webhook이 있어 미러를 만든 것이고, 플랫폼은 Admin API 직접 조회가 항상 더 신선하다.

인증은 `x-admin-token`을 확장하지 않고 **Cloud Run private + Google OIDC ID token**을 쓴다. `x-admin-token`은 클러스터 내부 루프백 전용 패턴이고, 이를 공개 인터넷 너머로 확장하면 무기한 유효한 정적 bearer 하나가 전 유저 권한을 갖게 된다.

## Consequences

- 백오피스에 새 탭을 만들지 않는다. **기존 `commerce` 탭을 플랫폼 Admin API에 연결한다**
- 백오피스 어댑터가 Firestore를 직접 조작하던 1,124줄이 API 호출로 바뀌면서 **오히려 단순해진다** — SA 키 관리, firebase-admin 초기화, 앱별 하드코딩이 사라진다
- 백오피스 OIDC SA에는 `run.invoker` **외 어떤 권한도 주지 않는다.** RPI가 침해돼도 얻는 건 "API 호출 가능"이고, 그 위에 read/write allowlist·서버 typed confirmation·durable rate limit·하드 상한이 있다. 세부 경계는 [ADR 0011](0011-admin-management-boundary.md)에서 확정한다
- **장애 대응 중 RPI까지 죽어 있으면 긴급 조작을 못 한다.** 이건 두 번째 백오피스를 짓는다고 해결되지 않으므로 [BREAK-GLASS 런북](../08-ops/BREAK-GLASS.md)으로 푼다
- **확장의 전제 조건 3가지를 지키지 못하면 이 결정을 재검토한다**
  1. 백오피스 MySQL에 런타임 유저 데이터를 저장하지 않는다
  2. 대량 팬아웃을 RPI에서 실행하지 않는다
  3. break-glass 경로가 문서화되고 **실제로 실행해 검증된다**

3번은 P9 장애 리허설에서 확인한다. **통과하지 못하면 분리 논거가 되살아난다.**

## Alternatives Considered

- **신규 백오피스 분리** — 두 번째 Next.js 앱, 두 번째 GitHub OAuth 앱, 두 번째 텔레그램 봇, 두 번째 배포 파이프라인의 유지비가 1인 운영에서 얻는 것보다 크다. 운영자의 머릿속 모델도 깨진다 — 앱 워크스페이스를 열면 개발·릴리스·지표·IAP가 다 있는데 결제만 다른 URL·다른 로그인이면 사고가 난다
- **WIF로 인증** — GCP STS가 RPI 클러스터의 OIDC discovery를 공개 인터넷에서 가져갈 수 있어야 하는데, 이 클러스터는 그렇게 노출돼 있지 않고 노출시킬 이유도 없다
