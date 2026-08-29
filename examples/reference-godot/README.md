# Godot 레퍼런스 — 플랫폼 SDK 붙이기

Godot 4.3 앱이 플랫폼을 쓰는 최소 형태. 복사해서 시작점으로 쓴다. 전체 가이드는
[Wiki: GDScript SDK](https://github.com/seorilabs/platform/wiki/GDScript-SDK)를 본다.

## 1. SDK 가져오기

GDScript에는 패키지 매니저가 없다. GitHub Release의 tarball을 받아 sha256을
검증한 뒤 `addons/` 아래에 푼다.

```bash
V=0.6.7
B=https://github.com/seorilabs/platform/releases/download/v$V
curl -fsSLO "$B/seorilabs-platform-gdscript-$V.tar.gz"
curl -fsSLO "$B/seorilabs-platform-gdscript-$V.tar.gz.sha256"
sha256sum -c "seorilabs-platform-gdscript-$V.tar.gz.sha256"   # macOS: shasum -a 256 -c
tar -xzf "seorilabs-platform-gdscript-$V.tar.gz" -C game/addons/
```

archive 안의 `SOURCE`, `VERSION`, `CHECKSUM`을 addon과 같은 커밋에 넣는다.
`SOURCE`는 다운로드한 Release asset URL을 가리키므로 나중에 어느 배포본인지
바로 알 수 있다. addon 안의 `addons/seorilabs_platform/tools/`는 SDK 자체
검증용이라 지워도 된다. 프로젝트 루트의 `tools/`와는 다른 디렉토리다.

복사본은 조용히 갈라진다 — foam-party와 lucid-chess가 같은 파일을 md5가 다른
채로 갖고 있었다. 파일을 직접 고치지 않고, 올릴 때는 새 tarball로 통째로 바꾼다.

## 2. 전환 스위치

`firebase/seori-platform.config.json`:

```json
{
  "enabled": false,
  "baseUrl": "",
  "appId": "my-app"
}
```

**기본값은 꺼짐이다.** 설정을 못 읽었을 때 켜진 것으로 보면 배포 사고로
파일이 빠진 순간 결제가 통째로 새 경로를 탄다.

롤백이 이 값 하나와 재배포로 끝나야 한다. 코드를 되돌려야 하는 롤백은
장애 중에 할 수 없다.

## 3. composition root

```gdscript
const PLATFORM_CLIENT_SCRIPT := preload("res://addons/seorilabs_platform/platform_client.gd")
const PLATFORM_CONFIG_SCRIPT := preload("res://game/infra/platform/seori_platform_config.gd")

var _platform: Node = null


func _ready() -> void:
    _platform = _create_platform_client()
    _analytics.set_platform(_platform)


func _create_platform_client() -> Node:
    var config: Dictionary = PLATFORM_CONFIG_SCRIPT.load_config()
    if not PLATFORM_CONFIG_SCRIPT.is_enabled(config):
        return null

    var client: Node = PLATFORM_CLIENT_SCRIPT.new()
    client.configure({
        "base_url": String(config.get("baseUrl", "")),
        "app_id": String(config.get("appId", "")),
        "event_context": func():
            return {
                "platform": "android",
                "appVersion": ProjectSettings.get_setting("application/config/version"),
                "locale": TranslationServer.get_locale(),
            },
        "presence_enabled": false,
    })
    # HTTPRequest가 동작하려면 트리에 있어야 한다.
    add_child(client)
    return client
```

꺼져 있으면 `null`이다. 이후 코드는 전부 `is_instance_valid()`로 감싼다 —
fail-closed 관용구다.

## 4. 로그인

```gdscript
platform.sign_in({"kind": "firebase-id-token", "value": id_token},
    func(res: Dictionary) -> void:
        if not res["ok"]:
            push_warning("로그인 실패: %s" % res["code"])
            return
        _on_signed_in(res["result"])
)
```

자격증명 종류는 셋이다.

| kind | 언제 |
|---|---|
| `firebase-id-token` | 앱별 Firebase Auth. 기본 경로 |
| `ait-login` | AppsInToss `appLogin()` |
| `anonymous` | `getAnonymousKey()` 해시 |

**익명은 결제할 수 없다.** 해시는 bearer 자격증명이 아니라 타인 사칭이
가능하다. 조회와 이벤트는 익명도 된다.

Firebase 계정이 아직 없는 사용자는 `create_firebase_custom_token`으로 플랫폼이
발급한 custom token을 받아 Firebase에 로그인한 뒤 ID 토큰으로 `sign_in`한다.
이 흐름은 `adapters/firebase_identity_adapter.gd`가 표준으로 구현한다.

## 5. 이벤트

GA4를 대체하지 않고 병행한다. 역할이 다르다.

```gdscript
func log_event(event_name: String, params: Dictionary) -> void:
    if is_instance_valid(_ga4) and _ga4.has_method("send"):
        _ga4.send(event_name, params)
    if is_instance_valid(_platform) and _platform.has_method("track"):
        _platform.track(event_name, params)
```

- GA4는 마케팅·잔존율. MP가 요청 IP로 geo를 판정하므로 서버로
  릴레이하면 국가·기기 분해가 망가진다
- 플랫폼은 운영·CS·결제 근거. MP는 인증이 없어 위조 가능하다

**이벤트명을 바꾸지 않는다.** 한쪽만 바꾸면 GA4 시계열이 끊긴다.

## 6. 결제

```gdscript
platform.verify_purchase(
    {
        "platform": "google_play",
        "product_id": "gecko_galaxy",
        "token": purchase_token,   # 마켓 SDK가 준 값
    },
    func(res: Dictionary) -> void:
        if not res["ok"]:
            _show_purchase_error(res["code"])
            return

        var result: Dictionary = res["result"]
        _apply_entitlements(result["entitlements"])

        # 마켓에 완료를 알려야 하는 경우가 있다
        var completion: Dictionary = result.get("completion", {})
        if String(completion.get("action", "none")) == "apps_in_toss_complete_product_grant":
            _ait_complete_product_grant(String(completion.get("orderId", "")))
)
```

`token`은 마켓마다 다르다.

| 마켓 | token |
|---|---|
| Google Play | `purchaseToken` |
| App Store | `transactionId` |
| AppsInToss | `orderId` |

**검증은 자동 재시도하지 않는다.** 서버가 멱등이어도 응답을 기다리는
사이 사용자에게는 두 번 결제한 것처럼 보인다. 재시도는 사용자가 정한다.

## 7. 오류 처리

`code`로만 분기한다. `message`는 사람이 읽는 문장이라 언제든 바뀐다. 전체 목록은
[Wiki: Error Codes](https://github.com/seorilabs/platform/wiki/Error-Codes).

```gdscript
match String(res["code"]):
    "anonymous_not_allowed":
        _prompt_login()
    "purchase_owned_by_another_user":
        _show_other_account_notice()
    "product_not_allowed":
        _show_unavailable_product()
    _:
        _show_generic_error()
```

`local`이 `true`면 서버에 닿지 못한 것이다. 재시도 안내가 맞다.

## 8. 검증

```bash
sha256sum -c seorilabs-platform-gdscript-0.6.7.tar.gz.sha256   # 가져올 때
godot --headless --script tools/platform_switch_probe.gd         # 프로젝트 루트 tools/ — 앱이 직접 만드는 스위치 probe
```

Godot은 `SCRIPT ERROR`가 나도 실행을 계속한다. exit code만 보면
통과로 보이므로 로그도 함께 봐야 한다.

## 관련

- 전체 가이드: [Wiki: GDScript SDK](https://github.com/seorilabs/platform/wiki/GDScript-SDK)
- SDK 문서: `sdk-gdscript/README.md`
- 계약: `spec/conformance/*.json`
