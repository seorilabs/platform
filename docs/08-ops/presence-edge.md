# RPI Presence Edge 운영 런북

`edge.vzyx.xyz`는 최근 활성 세션만 받는 유실 허용 관측 경로다. 이 절차에서
Edge가 준비되지 않으면 앱의 presence를 켜지 않는다. 기존 게임·인증·결제에는
영향이 없다.

## 보안 경계

| 위치 | 값 | 권한 |
|---|---|---|
| GCP Secret Manager | `platform-presence-private-key` | Ed25519 서명 비공개키. `platform-ingest`만 읽기 |
| RPI `presence-edge-secrets` | `PRESENCE_PUBLIC_KEY` | 검증용 raw 공개키 base64 |
| RPI `presence-edge-secrets` | `PRESENCE_DATABASE_URL` | `platform_presence_session`의 SELECT·INSERT·UPDATE·DELETE만 |

키 쌍과 DB 암호는 `~/.config/seorilabs` credential catalog를 원본으로 관리한다.
로그·PR·문서·GitHub Secret에 키 원문을 남기지 않는다. RPI에는 비공개키를 넣지
않는다.

## 최초 배포 순서

1. Backoffice의 `32_platform_presence_session` migration을 먼저 적용한다.
2. MySQL에 `platform_presence` 전용 사용자를 만들고
   `backoffice.platform_presence_session` 한 테이블의 SELECT·INSERT·UPDATE·DELETE만
   부여한다.
3. catalog에 Ed25519 키 쌍과 DB 접속정보를 등록한 뒤 RPI
   `presence-edge-secrets`를 만든다. 예시 파일은 값의 형식만 참고한다.
4. Platform 저장소 GitHub Actions에 기존 registry push 자격증명과 최소권한
   `KUBECONFIG_B64`를 연결한다.
5. `RPI Presence Edge` workflow를 `deploy=false`로 실행해 ARM64 이미지만 만든다.
6. `deploy=true`로 실행하고 rollout과 Ingress를 확인한다.
7. 아래 공개 health가 신뢰된 TLS로 200을 반환하는지 확인한다.
8. GCP `platform-ingest`에 URL과 비공개키를 함께 반영한다.
9. 한 앱의 registry `features.presence`와 SDK opt-in만 먼저 켜서 10분 관찰한 뒤
   앱 단위로 넓힌다.

```bash
curl --fail --silent --show-error https://edge.vzyx.xyz/health/live
curl --fail --silent --show-error https://edge.vzyx.xyz/health/ready
```

`/health/live`만 200이면 프로세스는 살았지만 MySQL이 준비되지 않은 상태다.
Backoffice는 `/health/ready`가 실패하면 숫자를 0으로 바꾸지 않고 `알 수 없음`을
표시한다.

## Platform ingest 활성화

두 설정은 같은 revision에 함께 들어가야 한다. 하나만 있으면 ingest가 시작을
거부한다.

```text
PLATFORM_PRESENCE_EDGE_URL=https://edge.vzyx.xyz
PLATFORM_PRESENCE_PRIVATE_KEY=Secret Manager platform-presence-private-key
```

설정 뒤 registry 파일만 고쳐서는 충분하지 않다. `cmd/regsync`로 Firestore까지
반영한 readback이 있어야 token 발급이 켜진다. Edge와 Backoffice가 정상이라는
근거를 확보하기 전에는 SDK의 `presenceEnabled` 또는 `presence_enabled`를 켜지
않는다.

## 장애 판정과 복구

| 증상 | 의미 | 조치 |
|---|---|---|
| DNS/TLS 실패 | 공개 진입점 장애 | DNS, cert-manager Certificate, Ingress 순서로 확인 |
| live 200, ready 503 | MySQL 경로 장애 | Secret 참조, DNS, 사용자 권한, MySQL 상태 확인 |
| ready 200, 동접 없음 | token/앱 opt-in 경로 | registry readback과 SDK 설정 확인 |
| 429 증가 | session limiter 포화 또는 비정상 호출 | 앱별 배포·요청 패턴 확인. 제한을 즉시 올리지 않음 |

복구 뒤 오래된 heartbeat를 재생하지 않는다. 클라이언트가 보내는 다음 새
heartbeat만 받아 최대 5분 안에 현재 상태를 다시 채운다. Cloud Run이나
BigQuery fallback을 만들지 않는다.
