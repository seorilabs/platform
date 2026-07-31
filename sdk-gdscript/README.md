# Seorilabs 플랫폼 GDScript SDK

Godot 앱이 플랫폼을 쓰기 위한 애드온. 인증·이벤트·설정·결제가 한 노드에 있다.

## 가져가기

GDScript에는 패키지 매니저가 없다. **파일을 복사해 간다.**

```bash
# 소비자 저장소에서
cp -r <platform>/sdk-gdscript/addons/seorilabs_platform game/addons/
cp <platform>/sdk-gdscript/VERSION game/addons/seorilabs_platform/VERSION
cp <platform>/sdk-gdscript/CHECKSUM game/addons/seorilabs_platform/CHECKSUM
```

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
    "app_id": "lizard-tycoon",
})
add_child(platform)

# 로그인
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

콜백은 항상 `Dictionary` 하나를 받는다.

| 키 | 뜻 |
|---|---|
| `ok` | 성공 여부 |
| `result` | 성공 결과 |
| `code` | 오류 코드. **분기는 이 값으로만 한다** |
| `message` | 사람이 읽는 메시지. 분기에 쓰지 않는다 |
| `local` | 로컬 판정인가. 서버에 닿지 못한 경우 |

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
- **결제 검증은 자동 재시도하지 않는다.** 서버가 멱등이어도 응답을
  기다리는 사이 사용자에게는 두 번 결제한 것처럼 보인다.
- HTTP 요청은 직렬로 흐른다. Godot의 `HTTPRequest`가 한 번에 하나만
  처리하기 때문이다. 플랫폼 호출은 빈도가 낮아 충분하다.
- `Retry-After`는 초 단위 숫자만 읽는다. Godot에 HTTP-date 파서가 없다.
