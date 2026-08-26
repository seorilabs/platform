# Seorilabs 플랫폼 GDScript SDK

Godot 앱이 플랫폼을 쓰기 위한 애드온. 인증·이벤트·설정·결제와 보상 광고
SSV adapter가 한 배포본에 있다. 원본 저장소는
`https://github.com/seorilabs/platform/tree/main/sdk-gdscript`이고 vendored addon의
`SOURCE`, `VERSION`, `CHECKSUM`으로 출처와 내용을 고정한다.

## 가져가기

GDScript에는 패키지 매니저가 없다. **파일을 복사해 간다.**

```bash
# 소비자 저장소에서. <platform>은 이 저장소의 checkout 경로다.
<platform>/scripts/vendor_sdk_gdscript.sh \
  --target "$PWD/game/addons/seorilabs_platform"
```

SDK는 symlink나 런타임 네트워크 의존성으로 연결하지 않는다. 모바일 export와
격리된 CI checkout에서도 같은 코드가 들어가도록 파일을 vendoring하고,
`SOURCE`, `VERSION`, `CHECKSUM`으로 원본을 링크한다.

`tools/`는 가져가지 않아도 된다. 검증 전용이다.

### 드리프트를 막는다

복사본은 조용히 갈라진다. foam-party와 lucid-chess가 같은 262줄 파일을
갖고 있으면서 md5가 다른 상태로 지냈고, 어느 쪽이 최신인지 아무도 몰랐다.

그래서 체크섬을 대조한다.

```bash
scripts/sdk_gdscript_checksum.sh --check
```

SDK를 고쳤으면 `--write`로 갱신하고 **같은 커밋에** 넣는다.

## 쓰기

```gdscript
var platform := SeoriPlatformClient.new()
platform.configure({
    "base_url": "https://platform-api-xxxx.run.app",
    "auth_base_url": "https://platform-iap-xxxx.run.app",
    "ingest_base_url": "https://platform-ingest-xxxx.run.app",
    "app_id": "lizard-tycoon",
    "event_context": func():
        return {
            "platform": "android",
            "appVersion": "1.2.3",
            "locale": "ko-KR",
        },
})
add_child(platform)

# 로그인
platform.create_firebase_custom_token("", "", func(bridge):
    if not bridge["ok"]:
        return
    # bridge.result.firebaseCustomToken은 Firebase에 한 번 사용하고 저장하지 않는다
)

platform.sign_in({"kind": "firebase-id-token", "value": id_token},
    func(res):
        if not res["ok"]:
            push_warning("로그인 실패: %s" % res["code"])
)

# 이벤트. 즉시 보내지 않고 모았다가 보낸다
platform.track("level_complete", {"level": 3, "is_new": true})
platform.flush_events()

# 결제
platform.verify_purchase(
    {"platform": "google_play", "product_id": "gecko_galaxy", "token": purchase_token},
    func(res):
        if res["ok"]:
            _apply_entitlements(res["result"]["entitlements"])
)
```

### Firebase 인증과 AdMob SSV

게임은 Custom Token 교환과 reward claim 상태 전이를 다시 구현하지 않는다.
공통 adapter를 만들고, 네이티브 AdMob 표시 코드에는 `custom_data`와 `user_id`만
전달한다.

```gdscript
const FirebaseIdentity := preload(
    "res://addons/seorilabs_platform/adapters/firebase_identity_adapter.gd")
const RewardedClaims := preload(
    "res://addons/seorilabs_platform/adapters/rewarded_claim_adapter.gd")

var identity := FirebaseIdentity.new()
add_child(identity)
identity.configure({
    "firebase_api_key": firebase_api_key,
    "platform_client": platform,
    "state_path": "user://firebase_auth.json",
})

var rewards := RewardedClaims.new()
add_child(rewards)
rewards.configure({
    "platform_client": platform,
    "identity_adapter": identity,
    "client_platform": "android",
})

var policy: Dictionary = await rewards.policy()
if not policy.get("allowed", false):
    return
var created: Dictionary = await rewards.create_admob_claim({
    "request_id": local_claim_id,
    "placement": "hint_reward",
    "reward_key": "hint",
    "reward_amount": 3,
})
var ssv := rewards.ssv_options(local_claim_id)
# 네이티브 SDK의 ServerSideVerificationOptions에 ssv 값을 설정한다.

# recover_admob_claim이 server_verified일 때 로컬 exactly-once 보상을 정산한다.
# 로컬 정산이 끝난 뒤에만 acknowledge를 호출한다.
await rewards.acknowledge(local_claim_id)

# 광고 미노출·미보상 종료 또는 recover final failed claim을 정리한다.
rewards.discard_unsettled_claim(local_claim_id)
```

`firebase_identity_adapter.gd`는 직접 Firebase 익명 가입으로 우회하지 않는다.
기존 UID 이전이 실패하면 fail-closed한다. UID와 refresh token만 원자적으로 저장하고,
Custom Token과 ID token은 메모리에서만 사용한다. 0.6.1 저장본에 ID token이 있으면
로드할 때 제거한다. `rewarded_claim_adapter.gd`는 정책 조회
실패를 광고 허용으로 바꾸지 않고, pending claim 참조와 ack 재시도를 로컬에
보존한다. 잘못된 SDK 객체나 버전 불일치로 필수 메서드가 없을 때도 호출 전에
fail-closed한다. `discard_unsettled_claim`은 광고를 보여 주지 못한 경우,
보상 없이 닫힌 경우, 또는 `recover_admob_claim`이 final failed로 종결된 후에만
호출하며, 이미 로컬 정산 후 ack 대기열에 든 claim은 폐기하지 않는다.
광고 unit, placement, reward 범위는 계속 앱 registry가 원장이다.

`event_context`는 `Dictionary` 또는 `Callable`을 받는다. Callable은 배치를
보내는 시점에 평가하므로 앱 안에서 언어가 바뀌어도 다음 flush부터 최신 locale이
들어간다. SDK는 OpenAPI에 선언된 `platform`, `appVersion`, `locale`,
`ga4ClientId`만 보내고 `sdkVersion`은 배포본 버전으로 고정한다.

`auth_base_url`은 Toss Login mTLS 자격증명이 격리된 `platform-iap`처럼
세션 발급 role이 기본 API와 다를 때만 지정한다. 생략하면 `base_url`을 쓴다.

콜백은 항상 `Dictionary` 하나를 받는다.

| 키 | 뜻 |
|---|---|
| `ok` | 성공 여부 |
| `result` | 성공 결과 |
| `code` | 오류 코드. **분기는 이 값으로만 한다** |
| `message` | 사람이 읽는 메시지. 분기에 쓰지 않는다 |
| `local` | 로컬 판정인가. 서버에 닿지 못한 경우 |

### 세션 만료와 IAP 인증 복구

`current_session()["expiresAt"]`은 Unix epoch millisecond다. SDK는 기기
sleep 중 멈출 수 있는 monotonic tick을 세션 만료 기준으로 쓰지 않으며,
만료 60초 전부터 선제 refresh한다. session refresh 전송 자체는 proactive와
strict IAP 경로 모두 일반 재시도 없이 한 번만 보낸다.

`verify_purchase`, `list_entitlements`, `account_references`는 전송 계층의 일반
재시도를 모두 끈다. 첫 응답이 정확히 `401 session_expired`일 때만 refresh를
한 번 요청하고 새 토큰으로 원 요청을 한 번 replay한다. 그 refresh 요청도
일반 재시도를 하지 않으며 refresh 실패, replay의 두 번째 401, 403, 5xx,
timeout은 그대로 한 번 반환한다. 이 strict IAP 복구 경로는 refresh 401/403
뒤 보관 자격증명으로 다시 로그인하지 않는다. 일반 `with_token`의 선제
refresh는 기존 재로그인 정책을 유지한다.

refresh 중 public `sign_in` 또는 `sign_out`이 호출되면 이전 인증 세대의
waiter를 `auth_state_changed`로 한 번 끝내고, 늦게 도착한 refresh·내부
재로그인 응답은 세션에 저장하거나 IAP 요청에 재사용하지 않는다. refresh
실패 응답은 `http_status`, `local`, `valid`를 포함한 원래 envelope를 보존한다.

## 계약

응답 해석·정규화·백오프는 `spec/conformance/*.json`이 정본이고
TypeScript SDK와 **같은 출력**을 내야 한다.

```bash
scripts/check_sdk_gdscript.sh
```

이 스크립트는 `SCRIPT ERROR`도 함께 잡는다. Godot은 스크립트 런타임
오류가 나도 실행을 계속해서 exit code만 보면 통과로 보인다.

### 미지 필드를 무시한다

응답에 모르는 필드가 있어도 정상으로 본다. 키 개수를 세지 않는다.

lizard-tycoon의 기존 `iap_functions_client.gd`는 `_exact_keys()`로
키 개수까지 일치를 요구했다. 서버가 필드를 하나 추가하면 마켓에 배포된
구버전이 깨진다. 앱은 2~3년 살아남으므로 그 방식으로는 `/v1`을
영구히 유지할 수 없다.

## 제약

- **익명 신원은 결제할 수 없다.** `getAnonymousKey` 해시는 bearer
  자격증명이 아니라 타인 사칭이 가능하다. 조회와 이벤트는 익명도 된다.
- **결제 검증은 일반 오류에서 자동 재시도하지 않는다.** 인증 미들웨어의
  첫 `401 session_expired`만 위 규칙으로 한 번 복구한다. 서버가 멱등이어도
  다른 응답을 기다리는 사이 사용자에게는 두 번 결제한 것처럼 보인다.
- HTTP 요청은 직렬로 흐른다. Godot의 `HTTPRequest`가 한 번에 하나만
  처리하기 때문이다. 플랫폼 호출은 빈도가 낮아 충분하다.
- `Retry-After`는 초 단위 숫자만 읽는다. Godot에 HTTP-date 파서가 없다.
