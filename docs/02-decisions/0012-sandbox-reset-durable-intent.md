# ADR 0012 — sandbox reset은 영구 intent로 시작 순서를 확정한다

## 상태

채택. 2026-08-02.

ADR 0011의 "sandbox reset 요청과 모든 효과를 한 트랜잭션에서 쓴다"는
결정을 이 ADR이 대체한다. grant·revoke의 단일 트랜잭션 결정은 유지한다.

## 맥락

App Store sandbox 구매내역을 지운 뒤 플랫폼 원장을 초기화하는 동안 같은
거래의 복원 grant가 들어올 수 있다. reset과 grant를 각각 원자적으로 만드는
것만으로는 먼저 시작한 reset이 이긴다는 보장이 없다. reset이 주문을 읽은 뒤
grant가 다른 사용자에게 소유권을 옮기면 다음 중 하나가 생길 수 있다.

- reset보다 오래된 거래가 새 사용자에게 다시 활성화된다.
- 이전 사용자의 reset barrier만 확인해 cross-user 이동을 놓친다.
- reset 효과 반영 전에 응답이 유실되어 새 requestId로 다시 실행된다.

함수 진입 시각은 durable하지 않으므로 순서의 기준으로 사용할 수 없다.

## 결정

### 선형화 지점

"reset 요청 시작"은 HTTP handler 진입이 아니라 **첫 Firestore 트랜잭션이
immutable intent와 사용자 barrier를 함께 commit한 시점**으로 정의한다.
commit 전 실패한 요청은 순서를 획득하지 않는다.

reset은 두 단계로 처리한다.

1. **prepare 트랜잭션**
   - `sandbox_reset_requests/{requestId}`에 고정 payload, `resetAt`, 최초 barrier
     revision을 create-only로 기록한다.
   - 같은 트랜잭션에서 `sandbox_reset_barriers/{puid}`의 active request와
     cutoff를 설정한다.
   - 같은 requestId·같은 payload는 기존 intent를 재개하고, 다른 payload는
     `operator_replay_mismatch`로 거부한다.
   - 같은 사용자에 다른 active reset이 있으면 `sandbox_reset_busy`로 거부한다.
2. **apply 트랜잭션**
   - intent, completion, barrier를 다시 읽고 active request와 cutoff가 정확히
     일치하는지 확인한다. grant가 barrier revision을 올릴 수 있으므로 현재
     revision은 최초 revision보다 크거나 같아야 한다.
   - `purchasedAt <= resetAt`인 App Store sandbox source만 revoke하고 projection을
     다시 계산한다.
   - `sandbox_reset_completions/{requestId}`를 create-only로 기록하는 것과 active
     barrier를 지우고 마지막 완료 cutoff를 보존하는 것을 같은 트랜잭션에서 한다.

intent와 completion은 immutable이며 삭제하지 않는다. 아직 intent가 없는 unknown 실행을
종결하는 `sandbox_reset_closures/{requestId}`도 immutable이며 삭제하지 않는다. barrier는
갱신할 수 있지만 문서 자체는 삭제하지 않는다. apply가 실패하면 intent와 active
barrier를 남겨 fail-closed하고 `sandbox_reset_pending` 503을 반환한다. 운영자는 같은
requestId의 원 요청을 재호출하거나 전용 resume API로만 계속한다. cancel과 새 requestId
재실행은 허용하지 않는다.

closure와 intent는 서로의 경로를 읽고 create하는 한 Firestore 트랜잭션으로
직렬화한다. 첫 commit이 이긴다.

- closure가 먼저 commit되면 뒤늦은 reset은 `sandbox_reset_closed`로 거부한다.
- intent 또는 completion이 먼저 commit되면 closure는
  `sandbox_reset_already_started`로 거부한다.
- 같은 requestId·appId·actor의 closure 재호출은 `applied=false`로 멱등 반환한다.
- 같은 requestId의 다른 appId·actor 또는 다른 운영 조작은
  `operator_replay_mismatch`로 거부한다.

트랜잭션 크기를 Firestore 한도 안에 고정하기 위해 한 사용자 reset에서 읽는
entitlement 문서는 최대 200개, 대상 주문은 최대 20개로 제한한다. 한도를 넘으면
부분 적용하지 않고 pending 상태를 유지한다.

### grant와 소유권 이전

App Store sandbox grant는 주문의 기존 소유자와 새 소유자가 다르면 **두 사용자의
barrier를 모두 읽고 성공 트랜잭션에서 모두 touch**한다.

- active 또는 마지막 완료 cutoff보다 오래된 거래는 grant와 소유권 이전을 막는다.
- `purchasedAt > cutoff`인 진짜 신규 거래는 허용한다.
- 막힌 이전에는 `iap_ownership_transfers` 증거를 만들지 않는다.

이 규칙으로 prepare가 먼저 commit되면 pre-cutoff grant가 이길 수 없고, grant가
먼저 commit되면 barrier revision 충돌로 prepare/apply가 최신 소유권을 다시 읽는다.

### 상태 조회와 복구

- `GET /v1/admin/iap/sandbox-resets/{requestId}`는 `prepared`, `completed`,
  `closed_not_started` 중 하나를 반환한다. intent와 closure가 모두 없을 때만
  `sandbox_reset_not_found` 404다.
- HTTP 상태 응답에는 PUID와 order key를 노출하지 않는다.
- `POST /v1/admin/iap/sandbox-resets/{requestId}/resume`은
  `RESUME RESET {appId} {requestId}` typed confirmation과 write allowlist를
  검증한 뒤 저장된 intent를 재개한다. completed 요청은 저장된 결과를 멱등 반환하고,
  `closed_not_started` 요청은 `sandbox_reset_closed`로 거부한다.
- 404 관찰만으로는 `not_applied`를 확정할 수 없다. 상태 조회 뒤 늦게 도착한 prepare와
  로컬 unlock이 경합할 수 있기 때문이다. 운영자는
  `POST /v1/admin/iap/sandbox-resets/{requestId}/close-not-started`에
  `CLOSE RESET {appId} {requestId}` typed confirmation을 보내 immutable closure를 먼저
  commit해야 한다.
- 백오피스의 command TTL이 끝나도 `prepared`를 `not_applied`로 종결하거나 새
  requestId를 만들 수 없다. `completed`만 `applied`, `closed_not_started`만
  `not_applied`로 수동 확정할 수 있다.

## 배포와 롤백

배포 직전에 sandbox reset 쓰기를 중단하고 live 원장의 legacy reset, active barrier,
미완료 intent가 0건인지 다시 확인한다. 확인 뒤 durable-intent 인식 코드 전체를
배포하고 reset 쓰기를 다시 연다.

첫 prepare가 commit된 뒤에는 intent를 모르는 이전 바이너리로 롤백하지 않는다.
미완료 intent를 보존한 채 수정 버전을 배포하는 **roll-forward만 허용**한다.

## 결과

- reset과 grant의 순서는 durable commit으로 설명하고 재현할 수 있다.
- 응답 유실이나 apply 실패가 새 요청으로 중복 초기화되는 일을 막는다.
- phase 사이 장애는 자동으로 사라지지 않고 `prepared`라는 명시적 운영 상태로 남는다.
- intent 부재 관찰은 immutable closure가 commit되어야만 안전한 `not_applied`가 된다.
- reset이 두 트랜잭션이 되므로 상태 조회, resume, 배포 freeze와 roll-forward 절차가
  필수 운영 계약이 된다.
