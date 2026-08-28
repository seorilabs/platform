# seorilabs/platform

Seorilabs의 모든 앱·게임을 **가로질러 밑에 깔리는** 런타임 공통 백엔드.

## 무엇인가

각 제품이 따로 만들던 공통 역량을 한 곳으로 모은다.

- **identity** — 앱별 Firebase Auth를 유지한 채 `(app_id, firebase_uid) → platform_user_id`로 매핑
- **IAP** — Google Play / App Store / AppsInToss 3마켓 결제 검증과 entitlement 원장
- **지표** — 단일 진입점 SDK가 GA4와 플랫폼 양쪽으로 팬아웃
- **RemoteConfig** — kill switch, 강제 업데이트, 점검 모드. AIT·Godot에서도 동작

## 무엇이 아닌가

**`seorilabs-backoffice`와 다른 층이다.**

| | 이 플랫폼 | seorilabs-backoffice |
|---|---|---|
| 다루는 것 | 최종 사용자 **런타임 데이터** | **개발·릴리스 운영** |
| 데이터 SoT | 플랫폼 Firestore | GitHub |
| 배포 | GCP Cloud Run | vzyx-cluster RPI k8s |

백오피스가 이 플랫폼의 **운영 UI**를 담당한다. 기존 `commerce` 탭이 플랫폼 Admin API에 연결된다.

## 아키텍처 규칙

- **R1** 백오피스는 런타임 경로에 없다. 백오피스가 죽어도 결제·검증은 정상 동작한다.
- **R2** 백오피스 MySQL은 런타임 유저 데이터를 0바이트도 저장하지 않는다. 플랫폼이 SoT.
- **R3** `platform-iap`은 별도 서비스다. 마켓 자격증명은 이 서비스에만 마운트한다.
- **R4** `/v1`은 영구히 깨지 않는다. 마켓 배포된 구버전 SDK가 2~3년 산다.
- **R5** IAP 불변식 12개는 언어와 무관하게 보존한다. → Obsidian `프로젝트/platform/03-architecture/iap.md`

## 구조

```text
spec/           API 계약 SoT. openapi.yaml에서 Go·TS 코드를 생성한다
server/         Go. PLATFORM_ROLE로 api|iap|ingest|admin|worker 스위치
packages/       TS SDK
sdk-gdscript/   Godot addon. 게임 repo에 vendoring
examples/       레퍼런스 앱 - RN, Godot
registry/apps/  앱 레지스트리. git이 SoT
scripts/        릴리스·검증 스크립트
```

## 문서

실행 원장은 이 저장소가 아니라 Obsidian vault의 `프로젝트/platform/`이다.
저장소에는 코드와 계약만 남긴다.

- `프로젝트/platform/02-decisions/` — ADR. **되돌리기 어려운 결정은 전부 여기 있다**
- `프로젝트/platform/03-architecture/` — 아키텍처, identity, 이벤트, IAP, RemoteConfig, Firestore 인덱스
- `프로젝트/platform/08-ops/BREAK-GLASS.md` — 백오피스 다운 시 긴급 조작 런북
- `프로젝트/platform/09-knowledge/go/` — Go 관용구·함정 학습 기록

저장소에 남는 원장은 두 개다. API 계약은 `spec/openapi.yaml`, 앱 레지스트리는
`registry/apps/*.json`이다.

## 상태

**D0 문서화 단계.** 코드는 아직 없다. 진행 상황은 Obsidian `프로젝트/platform/04-work/`를 본다.
