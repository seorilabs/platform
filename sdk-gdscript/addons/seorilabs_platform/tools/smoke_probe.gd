## SDK 전체가 로드되고 기본 동작이 성립하는지 확인한다.
##
## GDScript는 컴파일 단계가 없어서 문법 오류와 잘못된 참조가
## 런타임에야 드러난다. 헤드리스로 한 번 돌려 그것을 앞당긴다.
##
##   godot --headless --script addons/seorilabs_platform/tools/smoke_probe.gd
extends SceneTree

const PlatformClient := preload("res://addons/seorilabs_platform/platform_client.gd")
const HttpTransport := preload("res://addons/seorilabs_platform/core/http_transport.gd")
const Normalizer := preload("res://addons/seorilabs_platform/core/param_normalizer.gd")
const AtomicJsonStore := preload("res://addons/seorilabs_platform/core/atomic_json_store.gd")
const FirebaseIdentityAdapter := preload("res://addons/seorilabs_platform/adapters/firebase_identity_adapter.gd")
const RewardedClaimAdapter := preload("res://addons/seorilabs_platform/adapters/rewarded_claim_adapter.gd")

var _failures: Array[String] = []
var _probe_locale := "ko-KR"


class CaptureTransport:
	extends HttpTransport

	var last_request: Dictionary = {}

	func request(request_data: Dictionary, callback: Callable) -> void:
		last_request = request_data.duplicate(true)
		callback.call({"ok": true, "result": {}})


func _initialize() -> void:
	_check_loads()
	_check_client_defaults()
	_check_firebase_custom_token_bridge()
	_check_auth_role_routing()
	_check_event_context()
	_check_event_context_request()
	_check_standard_adapters()
	_check_guards()

	if _failures.is_empty():
		print("[smoke] 전부 통과")
		quit(0)
		return

	for failure in _failures:
		printerr("[smoke] 실패: %s" % failure)
	quit(1)


## 모든 스크립트가 로드되는지 본다.
func _check_loads() -> void:
	var scripts := [
		"res://addons/seorilabs_platform/platform_client.gd",
		"res://addons/seorilabs_platform/core/http_transport.gd",
		"res://addons/seorilabs_platform/core/param_normalizer.gd",
		"res://addons/seorilabs_platform/core/backoff.gd",
		"res://addons/seorilabs_platform/core/envelope.gd",
		"res://addons/seorilabs_platform/core/atomic_json_store.gd",
		"res://addons/seorilabs_platform/adapters/firebase_identity_adapter.gd",
		"res://addons/seorilabs_platform/adapters/rewarded_claim_adapter.gd",
	]

	for path in scripts:
		var script: Variant = load(path)
		if script == null:
			_fail("스크립트를 로드하지 못했다: %s" % path)


func _check_client_defaults() -> void:
	var client := PlatformClient.new()
	root.add_child(client)

	# 설정 전에도 안전한 기본값을 준다
	if client.is_signed_in():
		_fail("로그인하지 않았는데 세션이 있다")

	var config := client.current_config()
	if config.is_empty():
		_fail("기본 설정이 비었다")

	# 열린 상태가 기본이다. 서버에 닿기 전에 앱을 막으면 안 된다
	if client.is_under_maintenance():
		_fail("기본값이 점검 중으로 잡혔다")
	if client.is_sdk_blocked():
		_fail("기본값이 차단으로 잡혔다")

	if client.pending_event_count() != 0:
		_fail("초기 이벤트 큐가 비어 있지 않다")

	# 이벤트는 설정 없이 기록해도 터지지 않아야 한다
	client.track("smoke_event", {"level": 3, "is_new": true, "email": "a@b.c"})
	if client.pending_event_count() != 1:
		_fail("이벤트가 큐에 쌓이지 않았다")

	# 이름 없는 이벤트는 무시한다
	client.track("", {})
	if client.pending_event_count() != 1:
		_fail("이름 없는 이벤트가 큐에 들어갔다")

	client.queue_free()


func _check_firebase_custom_token_bridge() -> void:
	var transport := CaptureTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client.configure({
		"base_url": "https://api.platform.invalid",
		"ads_base_url": "https://ads.platform.invalid",
		"app_id": "probe",
	})
	root.add_child(client)

	client.create_firebase_custom_token("existing-id-token", "app-check-token", func(_res: Dictionary) -> void: pass)

	var request := transport.last_request
	if String(request.get("base_url", "")) != "https://api.platform.invalid":
		_fail("custom token 요청이 platform-api 경계를 사용하지 않는다: %s" % request)
	if String(request.get("path", "")) != "/v1/auth/firebase-custom-token":
		_fail("custom token 요청 경로가 다르다: %s" % request)
	if request.get("body", {}) != {"appId": "probe", "existingFirebaseIdToken": "existing-id-token"}:
		_fail("custom token 요청 본문이 다르다: %s" % request)
	if String(request.get("app_check_token", "")) != "app-check-token":
		_fail("App Check token이 전송 계층에 전달되지 않았다")
	if not bool(request.get("no_retry", false)):
		_fail("신규 uid bootstrap 요청의 자동 재시도가 열려 있다")

	client.delete_firebase_account("firebase-id-token", "app-check-token", func(_res: Dictionary) -> void: pass)
	request = transport.last_request
	if String(request.get("method", "")) != "DELETE" or String(request.get("path", "")) != "/v1/auth/firebase-account":
		_fail("Firebase account 삭제 요청이 다르다: %s" % request)
	if request.get("body", {}) != {"appId": "probe", "firebaseIdToken": "firebase-id-token"}:
		_fail("Firebase account 삭제 본문이 다르다: %s" % request)

	client.free()


func _check_standard_adapters() -> void:
	var identity := FirebaseIdentityAdapter.new()
	root.add_child(identity)
	identity.configure({"firebase_api_key": "", "platform_client": null})
	if not identity.has_method("ensure_identity") or not identity.has_method("set_app_check_token"):
		_fail("Firebase identity 표준 adapter 계약이 없다")

	var rewards := RewardedClaimAdapter.new()
	root.add_child(rewards)
	rewards.configure({
		"platform_client": null,
		"identity_adapter": identity,
		"client_platform": "android",
		"claim_map_path": "user://sdk_smoke_claims.json",
		"ack_queue_path": "user://sdk_smoke_acks.json",
	})
	for method in [
		"policy", "create_admob_claim", "ssv_options", "recover_admob_claim",
		"acknowledge", "discard_unsettled_claim",
	]:
		if not rewards.has_method(method):
			_fail("Rewarded claim 표준 adapter 메서드가 없다: %s" % method)

	rewards.free()
	identity.free()


func _check_auth_role_routing() -> void:
	var transport := CaptureTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client.configure({
		"base_url": "https://api.platform.invalid",
		"auth_base_url": "https://iap.platform.invalid",
		"app_id": "probe",
	})
	root.add_child(client)

	client.sign_in({"kind": "ait-login", "value": "code", "referrer": "SANDBOX"}, func(_res: Dictionary) -> void: pass)
	if String(transport.last_request.get("base_url", "")) != "https://iap.platform.invalid":
		_fail("세션 요청이 auth role 경계를 사용하지 않는다: %s" % transport.last_request)

	client.free()


func _check_event_context() -> void:
	_probe_locale = "ko-KR"
	var client := PlatformClient.new()
	client.configure({
		"base_url": "https://platform.invalid",
		"app_id": "probe",
		"event_context": Callable(self, "_probe_event_context"),
	})
	root.add_child(client)

	var first := client._resolved_event_context()
	var want_first := {
		"platform": "android",
		"appVersion": "1.2.3",
		"locale": "ko-KR",
		"sdkVersion": PlatformClient.SDK_VERSION,
	}
	if first != want_first:
		_fail("이벤트 context 정규화 결과가 다르다: %s" % first)

	_probe_locale = "en-US"
	var second := client._resolved_event_context()
	if String(second.get("locale", "")) != "en-US":
		_fail("동적 locale이 flush 시점에 갱신되지 않았다: %s" % second)

	client.configure({
		"base_url": "https://platform.invalid",
		"app_id": "probe",
		"event_context": {"platform": "desktop"},
	})
	var invalid := client._resolved_event_context()
	if invalid.has("platform") or invalid != {"sdkVersion": PlatformClient.SDK_VERSION}:
		_fail("허용되지 않은 이벤트 context가 남았다: %s" % invalid)

	client.queue_free()


func _probe_event_context() -> Dictionary:
	return {
		"platform": "Android",
		"appVersion": " 1.2.3 ",
		"locale": _probe_locale,
		"unknown": "drop",
	}


func _check_event_context_request() -> void:
	var transport := CaptureTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client.configure({
		"base_url": "https://platform.invalid",
		"app_id": "probe",
		"event_context": {"platform": "web", "locale": "en-US"},
	})
	root.add_child(client)
	client.track("smoke_event", {})
	client.flush_events()

	var body: Dictionary = transport.last_request.get("body", {})
	var context: Dictionary = body.get("context", {})
	if context != {
		"platform": "web",
		"locale": "en-US",
		"sdkVersion": PlatformClient.SDK_VERSION,
	}:
		_fail("flush 요청에 이벤트 context가 붙지 않았다: %s" % transport.last_request)

	client.free()


## 잘못된 입력이 네트워크를 타지 않고 즉시 거부되는지 본다.
func _check_guards() -> void:
	var client := PlatformClient.new()
	client.configure({"base_url": "https://platform.invalid", "app_id": "probe"})
	root.add_child(client)

	var got: Array[Dictionary] = []

	# 증명이 비면 즉시 거부한다
	client.verify_purchase({}, func(res: Dictionary) -> void: got.append(res))

	if got.size() != 1:
		_fail("빈 증명이 즉시 거부되지 않았다")
	elif String(got[0].get("code", "")) != "purchase_proof_invalid":
		_fail("빈 증명 코드가 다르다: %s" % got[0].get("code", ""))

	# 로그인 없이 entitlement를 조회하면 즉시 거부한다
	got.clear()
	client.list_entitlements(func(res: Dictionary) -> void: got.append(res))

	if got.size() != 1:
		_fail("미로그인 조회가 즉시 거부되지 않았다")
	elif String(got[0].get("code", "")) != "auth_required":
		_fail("미로그인 코드가 다르다: %s" % got[0].get("code", ""))

	# 정규화가 클라이언트를 거쳐도 같은 결과를 낸다
	var normalized := Normalizer.normalize({"is_new": true, "email": "x@y.z", "level": 3})
	if normalized != {"is_new": 1, "level": 3}:
		_fail("정규화 결과가 다르다: %s" % normalized)

	client.queue_free()


func _fail(message: String) -> void:
	_failures.append(message)
