# Architecture

## 문서

| 문서 | 내용 |
|---|---|
| [overview.md](overview.md) | 배치도, 서비스 분리 근거, 저장소 선택 |
| [identity.md](identity.md) | 세션 교환, 토큰 검증, anonymous 한계, PII 정책 |
| [events.md](events.md) | GA4와의 역할 분담, sink 구성, 직렬화 규약 |
| [iap.md](iap.md) | **불변식 12개**, 데이터 모델, 3마켓, 웹훅, 재시도 |
| [remote-config.md](remote-config.md) | 타겟팅, kill switch, 캐시 |

## 아키텍처 규칙

되돌리기 어려운 결정은 ADR에 있다. 여기는 **일상적으로 지켜야 하는 규칙**이다.

### R1. 백오피스는 런타임 경로에 없다

백오피스가 죽어도 결제·검증은 정상 동작한다. 잃는 건 "지금 운영 조작을 못 한다"뿐이고, 그건 [BREAK-GLASS](../08-ops/BREAK-GLASS.md)로 푼다.

이 규칙이 백오피스를 **분리하지 않고 확장**할 수 있게 만든 근거다. → ADR 0001

### R2. 백오피스 MySQL은 런타임 유저 데이터를 0바이트도 저장하지 않는다

미러조차 만들지 않는다. 백오피스가 저장하는 건 자기 행위의 감사기록뿐이다.

백오피스는 GitHub을 SoT로 두고 webhook 미러를 만드는 구조인데, **런타임 데이터에는 그 패턴을 쓰지 않는다.** Admin API 직접 조회가 항상 더 신선하고, 미러를 만들면 "어느 쪽이 진짜냐" 문제가 생긴다.

### R3. platform-iap은 별도 서비스다

Firebase Functions에서는 **함수별 Secret 분리가 보안 경계**였다. Apple 키는 Apple 엔드포인트에만 붙었다. 단일 Go 바이너리로 옮기면 이 경계가 사라지므로 **role 분리로 복원**한다.

마켓 자격증명은 `platform-iap`에만 마운트한다. 고QPS 공개 엔드포인트인 `platform-ingest`가 결제 자격증명을 들고 있으면 안 된다.

### R4. `/v1`은 영구히 깨지지 않는다

마켓에 배포된 구버전 SDK가 2~3년 산다. 응답 필드 **추가만** 허용하고, 제거나 의미 변경은 `/v2`를 새로 만든다.

> 현재 lizard-tycoon 클라이언트는 **응답 키 개수까지 정확히 일치**할 것을 요구한다. 즉 필드 추가도 breaking change다. **P8에서 "필수 필드 존재 검증 + 미지 필드 무시"로 완화**해야 이 규칙이 성립한다.

### R5. IAP 불변식 12개를 보존한다

언어와 저장소가 바뀌어도 [iap.md](iap.md)의 불변식은 그대로다. 이를 바꾸는 변경은 ADR 없이 하지 않는다.

## 서비스 분리

단일 바이너리, `PLATFORM_ROLE` 환경변수로 스위치. 코드베이스는 **하나**이고 네트워크 경계가 없다. 마이크로서비스가 아니다.

분리하는 이유는 셋이며, 전부 실질적이다.

1. **IAM 폭발 반경** — `platform-iap`만 마켓 자격증명을 갖는다
2. **비용 격벽** — ingest 폭주가 `max-instances`를 다 먹어 결제를 죽이면 안 된다. 서비스별 상한이 격벽이다
3. **동시성 튜닝이 정반대** — ingest는 I/O 바운드 write-only, api는 캐시 + Firestore 읽기

## Clean Architecture

조직의 다른 저장소와 같은 관용구를 쓴다.

- `internal/iap/domain`에 포트 인터페이스와 도메인 타입만 둔다. Firestore·HTTP·마켓 SDK를 import하지 않는다
- 구현은 `internal/iap/ledger`, `internal/iap/providers/*`에 둔다
- **인터페이스는 소비자 쪽에 정의한다.** 구현 패키지가 인터페이스를 export하지 않는 것이 Go 관용구다
- `cmd/platform/main.go`가 composition root다
