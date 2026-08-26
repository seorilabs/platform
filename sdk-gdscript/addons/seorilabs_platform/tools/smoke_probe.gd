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
var _probe_unix_ms := 0


class CaptureTransport:
	extends HttpTransport

	var last_request: Dictionary = {}

	func request(request_data: Dictionary, callback: Callable) -> void:
		last_request = request_data.duplicate(true)
		callback.call({"ok": true, "result": {}})


class ScriptedTransport:
	extends HttpTransport

	var requests: Array[Dictionary] = []
	var callbacks: Array[Callable] = []

	func request(request_data: Dictionary, callback: Callable) -> void:
		requests.append(request_data.duplicate(true))
		callbacks.append(callback)

	func respond(request_index: int, response: Dictionary) -> void:
		if request_index < 0 or request_index >= callbacks.size():
			return
		var callback := callbacks[request_index]
		callbacks[request_index] = Callable()
		if callback.is_valid():
			callback.call(response)


func _initialize() -> void:
	_check_loads()
	_check_client_defaults()
	_check_firebase_custom_token_bridge()
	_check_auth_role_routing()
	_check_event_context()
	_check_event_context_request()
	_check_canonical_event()
	_check_session_epoch_and_margin()
	_check_iap_session_expired_retry()
	_check_iap_refresh_single_flight()
	_check_iap_strict_refresh_failure()
	_check_proactive_refresh_sign_in_fallback()
	_check_sign_in_reentry_sign_out()
	_check_sign_in_reentry_account_switch()
	_check_refresh_cancelled_by_sign_out()
	_check_refresh_cancelled_by_account_switch()
	_check_internal_fallback_cancelled_by_sign_out()
	_check_iap_non_auth_failures()
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


func _check_canonical_event() -> void:
	var transport := CaptureTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client.configure({"base_url": "https://platform.invalid", "app_id": "probe"})
	root.add_child(client)
	client.track_event({
		"event_id": "01ABCDEF89abcdef0123456789ABCDEF",
		"occurred_at_micros": 1723456789123456,
		"name": "purchase",
		"params": {"transaction_id": "order-1"},
	})
	client.flush_events()
	var events: Array = transport.last_request.get("body", {}).get("events", [])
	if events.size() != 1 or events[0] != {
		"eventId": "01ABCDEF89abcdef0123456789ABCDEF",
		"tsUnixMs": 1723456789123,
		"name": "purchase",
		"params": {"transaction_id": "order-1"},
	}:
		_fail("canonical event identity가 보존되지 않았다: %s" % events)
	client.free()


## wall clock 기준 expiresAt과 60초 선제 갱신을 검증한다.
func _check_session_epoch_and_margin() -> void:
	_probe_unix_ms = 1_700_000_000_000
	var transport := ScriptedTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client._unix_time_ms_source = Callable(self, "_probe_time_unix_ms")
	client.configure({"base_url": "https://platform.invalid", "app_id": "probe"})
	root.add_child(client)

	var sign_in_results: Array[Dictionary] = []
	client.sign_in(
		{"kind": "firebase-id-token", "value": "credential"},
		func(response: Dictionary) -> void: sign_in_results.append(response),
	)
	if transport.requests.size() != 1:
		_fail("세션 발급 요청이 만들어지지 않았다")
		client.free()
		return

	transport.respond(0, _session_response("token-old", "refresh-old", 120))
	_expect(sign_in_results.size() == 1, "세션 발급 callback이 정확히 한 번 오지 않았다")
	_expect(
		int(client.current_session().get("expiresAt", 0)) == 1_700_000_120_000,
		"expiresAt이 Unix epoch ms로 저장되지 않았다: %s" % client.current_session(),
	)

	# 기기 sleep처럼 monotonic tick과 무관하게 wall clock만 전진시킨다.
	_probe_unix_ms = 1_700_000_059_999
	var token_results: Array[Dictionary] = []
	client.with_token(func(token: String, error: Dictionary) -> void:
		token_results.append({"token": token, "error": error})
	)
	_expect(
		token_results.size() == 1 and String(token_results[0].get("token", "")) == "token-old",
		"60초 margin 전인데 토큰을 즉시 주지 않았다: %s" % token_results,
	)
	_expect(transport.requests.size() == 1, "60초 margin 전에 refresh를 요청했다")

	_probe_unix_ms = 1_700_000_060_000
	token_results.clear()
	client.with_token(func(token: String, error: Dictionary) -> void:
		token_results.append({"token": token, "error": error})
	)
	if transport.requests.size() != 2:
		_fail("60초 margin에서 refresh 요청이 만들어지지 않았다")
		client.free()
		return
	_expect(
		String(transport.requests[1].get("path", "")) == "/v1/auth/refresh",
		"선제 갱신 경로가 다르다: %s" % transport.requests[1],
	)
	_expect(token_results.is_empty(), "refresh 완료 전에 token callback이 호출됐다")
	transport.respond(1, _session_response("token-new", "refresh-new", 120))
	_expect(
		token_results.size() == 1 and String(token_results[0].get("token", "")) == "token-new",
		"선제 갱신 결과가 callback에 한 번 전달되지 않았다: %s" % token_results,
	)

	client.free()


## 첫 401 session_expired만 refresh 후 한 번 replay하는지 검증한다.
func _check_iap_session_expired_retry() -> void:
	_probe_unix_ms = 1_700_100_000_000
	var transport := ScriptedTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client._unix_time_ms_source = Callable(self, "_probe_time_unix_ms")
	client.configure({
		"base_url": "https://api.platform.invalid",
		"auth_base_url": "https://auth.platform.invalid",
		"iap_base_url": "https://iap.platform.invalid",
		"app_id": "probe",
	})
	root.add_child(client)
	client._store_session(_session_result("token-old", "refresh-old", 3600))

	var results: Array[Dictionary] = []
	client.list_entitlements(func(response: Dictionary) -> void: results.append(response))
	if transport.requests.size() != 1:
		_fail("entitlement 최초 요청이 만들어지지 않았다")
		client.free()
		return
	_expect_iap_request(
		transport.requests[0],
		"GET",
		"/v1/iap/entitlements",
		"token-old",
	)

	transport.respond(0, _failure_response(401, "session_expired"))
	if transport.requests.size() != 2:
		_fail("session_expired 뒤 refresh가 정확히 한 번 시작되지 않았다")
		client.free()
		return
	_expect(
		String(transport.requests[1].get("path", "")) == "/v1/auth/refresh"
			and bool(transport.requests[1].get("no_retry", false)),
		"session_expired 뒤 refresh 경로가 다르다: %s" % transport.requests[1],
	)
	_expect(results.is_empty(), "refresh 전에 IAP callback이 호출됐다")

	transport.respond(1, _session_response("token-new", "refresh-new", 3600))
	if transport.requests.size() != 3:
		_fail("refresh 성공 뒤 IAP 요청이 한 번 replay되지 않았다")
		client.free()
		return
	_expect_iap_request(
		transport.requests[2],
		"GET",
		"/v1/iap/entitlements",
		"token-new",
	)
	transport.respond(2, _success_response({"entitlements": []}))
	_expect(
		results.size() == 1 and bool(results[0].get("ok", false)),
		"IAP replay 성공 callback이 정확히 한 번 오지 않았다: %s" % results,
	)

	# replay 응답도 401이면 refresh나 세 번째 요청 없이 그대로 끝낸다.
	results.clear()
	client.list_entitlements(func(response: Dictionary) -> void: results.append(response))
	var first_index := transport.requests.size() - 1
	transport.respond(first_index, _failure_response(401, "session_expired"))
	var refresh_index := transport.requests.size() - 1
	transport.respond(refresh_index, _session_response("token-third", "refresh-third", 3600))
	var replay_index := transport.requests.size() - 1
	var count_before_second_401 := transport.requests.size()
	transport.respond(replay_index, _failure_response(401, "session_expired"))
	_expect(
		transport.requests.size() == count_before_second_401,
		"두 번째 session_expired 뒤 요청을 다시 보냈다",
	)
	_expect(
		results.size() == 1 and String(results[0].get("code", "")) == "session_expired",
		"두 번째 session_expired가 한 번 반환되지 않았다: %s" % results,
	)

	client.free()


## 동시에 만료된 요청이 refresh 하나를 공유하는지 검증한다.
func _check_iap_refresh_single_flight() -> void:
	_probe_unix_ms = 1_700_200_000_000
	var transport := ScriptedTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client._unix_time_ms_source = Callable(self, "_probe_time_unix_ms")
	client.configure({
		"base_url": "https://platform.invalid",
		"iap_base_url": "https://iap.platform.invalid",
		"app_id": "probe",
	})
	root.add_child(client)
	client._store_session(_session_result("token-shared", "refresh-shared", 3600))

	var first_results: Array[Dictionary] = []
	var second_results: Array[Dictionary] = []
	client.list_entitlements(func(response: Dictionary) -> void: first_results.append(response))
	client.list_entitlements(func(response: Dictionary) -> void: second_results.append(response))
	if transport.requests.size() != 2:
		_fail("동시 entitlement 최초 요청 두 개가 만들어지지 않았다")
		client.free()
		return

	transport.respond(0, _failure_response(401, "session_expired"))
	if transport.requests.size() != 3:
		_fail("첫 만료 응답이 refresh 하나를 만들지 않았다")
		client.free()
		return
	_expect(
		bool(transport.requests[2].get("no_retry", false)),
		"single-flight strict refresh 요청의 일반 재시도가 열려 있다",
	)
	transport.respond(1, _failure_response(401, "session_expired"))
	_expect(
		transport.requests.size() == 3,
		"동시 session_expired가 refresh를 중복 생성했다",
	)
	transport.respond(2, _session_response("token-shared-new", "refresh-shared-new", 3600))
	if transport.requests.size() != 5:
		_fail("single-flight refresh 뒤 원 요청 두 개가 각각 replay되지 않았다")
		client.free()
		return
	_expect_iap_request(
		transport.requests[3],
		"GET",
		"/v1/iap/entitlements",
		"token-shared-new",
	)
	_expect_iap_request(
		transport.requests[4],
		"GET",
		"/v1/iap/entitlements",
		"token-shared-new",
	)
	transport.respond(3, _success_response({"entitlements": []}))
	transport.respond(4, _success_response({"entitlements": []}))
	_expect(
		first_results.size() == 1 and second_results.size() == 1,
		"single-flight 요청 callback이 각각 정확히 한 번 오지 않았다",
	)

	client.free()


## strict IAP refresh 실패가 재로그인이나 원 요청 replay를 만들지 않는지 검증한다.
func _check_iap_strict_refresh_failure() -> void:
	_probe_unix_ms = 1_700_300_000_000
	var failures: Array[Dictionary] = [
		_failure_response(401, "refresh_token_invalid"),
		_failure_response(403, "refresh_forbidden"),
		_failure_response(503, "refresh_unavailable"),
	]
	for failure in failures:
		var transport := ScriptedTransport.new()
		var client := PlatformClient.new()
		client.add_child(transport)
		client._transport = transport
		client._unix_time_ms_source = Callable(self, "_probe_time_unix_ms")
		client.configure({
			"base_url": "https://api.platform.invalid",
			"auth_base_url": "https://auth.platform.invalid",
			"iap_base_url": "https://iap.platform.invalid",
			"app_id": "probe",
		})
		root.add_child(client)
		client._credential = {"kind": "firebase-id-token", "value": "credential"}
		client._store_session(_session_result("token-old", "refresh-revoked", 3600))

		var results: Array[Dictionary] = []
		client.list_entitlements(func(response: Dictionary) -> void: results.append(response))
		transport.respond(0, _failure_response(401, "session_expired"))
		if transport.requests.size() != 2:
			_fail("strict 실패 probe에서 refresh 요청이 만들어지지 않았다")
			client.free()
			continue
		_expect(
			bool(transport.requests[1].get("no_retry", false)),
			"strict IAP refresh 요청의 일반 재시도가 열려 있다",
		)
		transport.respond(1, failure)
		_expect(
			transport.requests.size() == 2,
			"strict IAP refresh %s 뒤 재로그인 또는 replay가 발생했다"
				% failure.get("http_status", 0),
		)
		_expect(
			results.size() == 1 and results[0] == failure,
			"strict refresh 오류 envelope가 보존되지 않았다: got=%s want=%s"
				% [results, failure],
		)

		client.free()


## 일반 선제 갱신은 기존 보관 자격증명 재로그인 정책을 유지한다.
func _check_proactive_refresh_sign_in_fallback() -> void:
	_probe_unix_ms = 1_700_350_000_000
	var transport := ScriptedTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client._unix_time_ms_source = Callable(self, "_probe_time_unix_ms")
	client.configure({
		"base_url": "https://api.platform.invalid",
		"auth_base_url": "https://auth.platform.invalid",
		"app_id": "probe",
	})
	root.add_child(client)
	client._credential = {"kind": "firebase-id-token", "value": "credential"}
	client._store_session(_session_result("token-old", "refresh-revoked", 30))

	var token_results: Array[Dictionary] = []
	client.with_token(func(token: String, error: Dictionary) -> void:
		token_results.append({"token": token, "error": error})
	)
	if transport.requests.size() != 1:
		_fail("선제 refresh 요청이 만들어지지 않았다")
		client.free()
		return
	_expect(
		bool(transport.requests[0].get("no_retry", false)),
		"선제 refresh 전송의 일반 재시도가 열려 있다",
	)

	# 이미 시작된 proactive flight에 strict IAP waiter가 합류해도 같은
	# no-retry 요청을 공유하고, refresh 실패 뒤 재로그인에는 참여하지 않는다.
	var strict_results: Array[Dictionary] = []
	client._refresh_after_session_expired(
		"token-old",
		func(token: String, error: Dictionary) -> void:
			strict_results.append({"token": token, "error": error})
	)
	_expect(
		transport.requests.size() == 1,
		"혼합 waiter가 refresh 요청을 중복 생성했다",
	)
	transport.respond(0, _failure_response(401, "refresh_token_invalid"))
	if transport.requests.size() != 2:
		_fail("선제 refresh 401 뒤 세션 재발급 요청이 만들어지지 않았다")
		client.free()
		return
	_expect(
		strict_results.size() == 1
			and String(strict_results[0].get("token", "")).is_empty()
			and String(strict_results[0].get("error", {}).get("code", ""))
				== "refresh_token_invalid",
		"혼합 flight의 strict waiter가 정확히 한 번 실패하지 않았다: %s" % strict_results,
	)
	_expect(
		String(transport.requests[1].get("path", "")) == "/v1/auth/session"
			and String(transport.requests[1].get("base_url", "")) == "https://auth.platform.invalid",
		"선제 refresh 실패 뒤 재로그인 요청이 다르다: %s" % transport.requests[1],
	)
	transport.respond(1, _session_response("token-signed-in", "refresh-signed-in", 3600))
	_expect(
		token_results.size() == 1
			and String(token_results[0].get("token", "")) == "token-signed-in",
		"선제 refresh 재로그인 callback이 정확히 한 번 오지 않았다: %s" % token_results,
	)
	_expect(strict_results.size() == 1, "strict waiter가 재로그인 결과를 추가로 받았다")

	client.free()


## sign_in의 세션 초기화 signal에서 sign_out이 재진입해도 외부 요청을 막는지 검증한다.
func _check_sign_in_reentry_sign_out() -> void:
	_probe_unix_ms = 1_700_350_000_000
	var transport := ScriptedTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client._unix_time_ms_source = Callable(self, "_probe_time_unix_ms")
	client.configure({
		"base_url": "https://api.platform.invalid",
		"auth_base_url": "https://auth.platform.invalid",
		"app_id": "probe",
	})
	root.add_child(client)
	client._store_session(_session_result("token-old", "refresh-old", 3600))

	var reentered := [false]
	client.session_changed.connect(func(session: Dictionary) -> void:
		if not session.is_empty() or bool(reentered[0]):
			return
		reentered[0] = true
		client.sign_out()
	)
	var results: Array[Dictionary] = []
	client.sign_in(
		{"kind": "firebase-id-token", "value": "outer-credential"},
		func(response: Dictionary) -> void: results.append(response),
	)

	_expect(bool(reentered[0]), "sign_in session_changed에서 sign_out이 재진입하지 않았다")
	_expect(transport.requests.is_empty(), "sign_out 재진입 뒤 외부 sign_in 요청이 시작됐다")
	_expect(
		results.size() == 1
			and String(results[0].get("code", "")) == "auth_state_changed",
		"sign_out 재진입이 외부 callback을 정확히 한 번 종료하지 않았다: %s" % results,
	)
	_expect(client.current_session().is_empty(), "sign_out 재진입 뒤 세션이 남았다")

	client.free()


## sign_in의 세션 초기화 signal에서 다른 sign_in이 재진입하면 새 요청만 남는지 검증한다.
func _check_sign_in_reentry_account_switch() -> void:
	_probe_unix_ms = 1_700_355_000_000
	var transport := ScriptedTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client._unix_time_ms_source = Callable(self, "_probe_time_unix_ms")
	client.configure({
		"base_url": "https://api.platform.invalid",
		"auth_base_url": "https://auth.platform.invalid",
		"app_id": "probe",
	})
	root.add_child(client)
	client._store_session(_session_result("token-old", "refresh-old", 3600))

	var reentered := [false]
	var inner_results: Array[Dictionary] = []
	client.session_changed.connect(func(session: Dictionary) -> void:
		if not session.is_empty() or bool(reentered[0]):
			return
		reentered[0] = true
		client.sign_in(
			{"kind": "firebase-id-token", "value": "inner-credential"},
			func(response: Dictionary) -> void: inner_results.append(response),
		)
	)
	var outer_results: Array[Dictionary] = []
	client.sign_in(
		{"kind": "firebase-id-token", "value": "outer-credential"},
		func(response: Dictionary) -> void: outer_results.append(response),
	)

	_expect(bool(reentered[0]), "sign_in session_changed에서 다른 sign_in이 재진입하지 않았다")
	_expect(
		outer_results.size() == 1
			and String(outer_results[0].get("code", "")) == "auth_state_changed",
		"계정 전환 재진입이 외부 callback을 정확히 한 번 종료하지 않았다: %s"
			% outer_results,
	)
	_expect(inner_results.is_empty(), "응답 전 내부 sign_in callback이 호출됐다")
	if transport.requests.size() != 1:
		_fail("계정 전환 재진입에서 새 sign_in 요청만 남지 않았다: %s" % transport.requests)
		client.free()
		return
	var request: Dictionary = transport.requests[0]
	var body: Dictionary = request.get("body", {})
	var requested_credential: Dictionary = body.get("credential", {})
	_expect(
		String(request.get("path", "")) == "/v1/auth/session"
			and String(requested_credential.get("value", "")) == "inner-credential",
		"계정 전환 재진입 요청이 내부 자격증명에 귀속되지 않았다: %s" % request,
	)

	transport.respond(0, _session_response("token-inner", "refresh-inner", 3600))
	_expect(
		inner_results.size() == 1 and bool(inner_results[0].get("ok", false)),
		"내부 sign_in callback이 정확히 한 번 성공하지 않았다: %s" % inner_results,
	)
	_expect(outer_results.size() == 1, "내부 sign_in 응답이 외부 callback을 다시 호출했다")
	_expect(
		String(client.current_session().get("platformToken", "")) == "token-inner",
		"내부 sign_in 세션이 저장되지 않았다: %s" % client.current_session(),
	)

	client.free()


## refresh 중 sign_out이 늦은 응답의 세션 복원과 IAP replay를 막는지 검증한다.
func _check_refresh_cancelled_by_sign_out() -> void:
	_probe_unix_ms = 1_700_360_000_000
	var transport := ScriptedTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client._unix_time_ms_source = Callable(self, "_probe_time_unix_ms")
	client.configure({
		"base_url": "https://api.platform.invalid",
		"auth_base_url": "https://auth.platform.invalid",
		"iap_base_url": "https://iap.platform.invalid",
		"app_id": "probe",
	})
	root.add_child(client)
	client._store_session(_session_result("token-old", "refresh-old", 3600))

	var results: Array[Dictionary] = []
	client.list_entitlements(func(response: Dictionary) -> void: results.append(response))
	transport.respond(0, _failure_response(401, "session_expired"))
	if transport.requests.size() != 2:
		_fail("sign_out 취소 probe에서 refresh 요청이 만들어지지 않았다")
		client.free()
		return

	client.sign_out()
	_expect(
		results.size() == 1 and String(results[0].get("code", "")) == "auth_state_changed",
		"sign_out이 refresh waiter를 정확히 한 번 취소하지 않았다: %s" % results,
	)
	_expect(client.current_session().is_empty(), "sign_out 뒤 세션이 남았다")
	transport.respond(1, _session_response("token-stale", "refresh-stale", 3600))
	_expect(client.current_session().is_empty(), "늦은 refresh 응답이 sign_out 세션을 복원했다")
	_expect(results.size() == 1, "늦은 refresh 응답이 callback을 다시 호출했다")
	_expect(transport.requests.size() == 2, "늦은 refresh 응답이 IAP 요청을 replay했다")

	client.free()


## 다른 sign_in이 시작되면 이전 refresh가 새 신원을 덮지 않는지 검증한다.
func _check_refresh_cancelled_by_account_switch() -> void:
	_probe_unix_ms = 1_700_370_000_000
	var transport := ScriptedTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client._unix_time_ms_source = Callable(self, "_probe_time_unix_ms")
	client.configure({
		"base_url": "https://api.platform.invalid",
		"auth_base_url": "https://auth.platform.invalid",
		"iap_base_url": "https://iap.platform.invalid",
		"app_id": "probe",
	})
	root.add_child(client)
	client._store_session(_session_result("token-old", "refresh-old", 3600))

	var iap_results: Array[Dictionary] = []
	var sign_in_results: Array[Dictionary] = []
	client.list_entitlements(func(response: Dictionary) -> void: iap_results.append(response))
	transport.respond(0, _failure_response(401, "session_expired"))
	if transport.requests.size() != 2:
		_fail("계정 전환 probe에서 refresh 요청이 만들어지지 않았다")
		client.free()
		return

	client.sign_in(
		{"kind": "firebase-id-token", "value": "new-credential"},
		func(response: Dictionary) -> void: sign_in_results.append(response),
	)
	if transport.requests.size() != 3:
		_fail("계정 전환 세션 요청이 만들어지지 않았다")
		client.free()
		return
	_expect(
		iap_results.size() == 1
			and String(iap_results[0].get("code", "")) == "auth_state_changed",
		"계정 전환이 이전 IAP waiter를 정확히 한 번 취소하지 않았다: %s" % iap_results,
	)
	_expect(client.current_session().is_empty(), "계정 전환 시작 뒤 이전 세션이 남았다")

	# 새 sign_in보다 이전 refresh 응답을 먼저 반환해도 저장되면 안 된다.
	transport.respond(1, _session_response("token-stale", "refresh-stale", 3600))
	_expect(client.current_session().is_empty(), "이전 refresh가 전환 중 세션을 복원했다")
	_expect(iap_results.size() == 1, "이전 refresh가 IAP callback을 다시 호출했다")
	_expect(transport.requests.size() == 3, "이전 refresh가 IAP 요청을 replay했다")

	transport.respond(2, _session_response("token-new", "refresh-new", 3600))
	_expect(
		String(client.current_session().get("platformToken", "")) == "token-new",
		"새 sign_in 세션이 저장되지 않았다: %s" % client.current_session(),
	)
	_expect(
		sign_in_results.size() == 1 and bool(sign_in_results[0].get("ok", false)),
		"새 sign_in callback이 정확히 한 번 오지 않았다: %s" % sign_in_results,
	)

	client.free()


## 내부 fallback sign_in 중 sign_out도 늦은 세션 응답을 폐기하는지 검증한다.
func _check_internal_fallback_cancelled_by_sign_out() -> void:
	_probe_unix_ms = 1_700_380_000_000
	var transport := ScriptedTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client._unix_time_ms_source = Callable(self, "_probe_time_unix_ms")
	client.configure({
		"base_url": "https://api.platform.invalid",
		"auth_base_url": "https://auth.platform.invalid",
		"app_id": "probe",
	})
	root.add_child(client)
	client._credential = {"kind": "firebase-id-token", "value": "credential"}
	client._store_session(_session_result("token-old", "refresh-revoked", 30))

	var token_results: Array[Dictionary] = []
	client.with_token(func(token: String, error: Dictionary) -> void:
		token_results.append({"token": token, "error": error})
	)
	transport.respond(0, _failure_response(401, "refresh_token_invalid"))
	if transport.requests.size() != 2:
		_fail("내부 fallback sign_in 요청이 만들어지지 않았다")
		client.free()
		return

	client.sign_out()
	_expect(
		token_results.size() == 1
			and String(token_results[0].get("error", {}).get("code", ""))
				== "auth_state_changed",
		"sign_out이 내부 fallback waiter를 정확히 한 번 취소하지 않았다: %s"
			% token_results,
	)
	transport.respond(1, _session_response("token-stale", "refresh-stale", 3600))
	_expect(client.current_session().is_empty(), "늦은 내부 fallback 응답이 세션을 복원했다")
	_expect(token_results.size() == 1, "늦은 내부 fallback 응답이 callback을 다시 호출했다")

	client.free()


## 403, 5xx, timeout과 refresh 실패가 일반 재시도를 만들지 않는지 검증한다.
func _check_iap_non_auth_failures() -> void:
	_probe_unix_ms = 1_700_400_000_000
	var transport := ScriptedTransport.new()
	var client := PlatformClient.new()
	client.add_child(transport)
	client._transport = transport
	client._unix_time_ms_source = Callable(self, "_probe_time_unix_ms")
	client.configure({
		"base_url": "https://platform.invalid",
		"iap_base_url": "https://iap.platform.invalid",
		"app_id": "probe",
	})
	root.add_child(client)
	client._store_session(_session_result("token-no-retry", "refresh-no-retry", 3600))

	var failures: Array[Dictionary] = [
		_failure_response(403, "forbidden"),
		_failure_response(503, "service_unavailable"),
		_network_failure_response(),
	]
	for failure in failures:
		var results: Array[Dictionary] = []
		var request_index := transport.requests.size()
		client.account_references(func(response: Dictionary) -> void: results.append(response))
		if transport.requests.size() != request_index + 1:
			_fail("account reference 요청이 정확히 하나 만들어지지 않았다")
			continue
		_expect_iap_request(
			transport.requests[request_index],
			"POST",
			"/v1/iap/account-references",
			"token-no-retry",
		)
		transport.respond(request_index, failure)
		_expect(
			transport.requests.size() == request_index + 1,
			"IAP 실패 %s 뒤 일반 재시도가 발생했다" % failure.get("code", ""),
		)
		_expect(
			results.size() == 1 and String(results[0].get("code", "")) == String(failure.get("code", "")),
			"IAP 실패 callback이 정확히 한 번 오지 않았다: %s" % results,
		)

	# 구매 검증도 5xx를 일반 재시도하지 않는다.
	var verify_results: Array[Dictionary] = []
	var verify_index := transport.requests.size()
	client.verify_purchase(
		{"platform": "app_store", "product_id": "premium", "token": "proof"},
		func(response: Dictionary) -> void: verify_results.append(response),
	)
	if transport.requests.size() == verify_index + 1:
		_expect_iap_request(
			transport.requests[verify_index],
			"POST",
			"/v1/iap/verify",
			"token-no-retry",
		)
		transport.respond(verify_index, _failure_response(500, "verify_failed"))
		_expect(transport.requests.size() == verify_index + 1, "구매 검증 5xx를 재시도했다")
		_expect(
			verify_results.size() == 1
				and String(verify_results[0].get("code", "")) == "verify_failed",
			"구매 검증 실패 callback이 정확히 한 번 오지 않았다: %s" % verify_results,
		)
	else:
		_fail("구매 검증 요청이 정확히 하나 만들어지지 않았다")

	# 인증 replay의 응답은 401/403/5xx/timeout 모두 terminal이다.
	var replay_failures: Array[Dictionary] = [
		_failure_response(401, "session_expired"),
		_failure_response(403, "forbidden"),
		_failure_response(503, "service_unavailable"),
		_network_failure_response(),
	]
	for failure in replay_failures:
		var replay_results: Array[Dictionary] = []
		var replay_index := transport.requests.size()
		client._send_iap_request(
			{"method": "GET", "path": "/v1/iap/entitlements"},
			"token-no-retry",
			func(response: Dictionary) -> void: replay_results.append(response),
			true,
			client._auth_generation,
		)
		if transport.requests.size() != replay_index + 1:
			_fail("인증 replay 요청이 정확히 하나 만들어지지 않았다")
			continue
		transport.respond(replay_index, failure)
		_expect(
			transport.requests.size() == replay_index + 1,
			"인증 replay 실패 %s 뒤 요청을 다시 보냈다" % failure.get("code", ""),
		)
		_expect(
			replay_results.size() == 1
				and String(replay_results[0].get("code", "")) == String(failure.get("code", "")),
			"인증 replay 실패 callback이 정확히 한 번 오지 않았다: %s" % replay_results,
		)

	# 원 요청의 401 뒤 refresh 자체가 실패하면 IAP 요청을 replay하지 않는다.
	var refresh_failure_results: Array[Dictionary] = []
	var initial_index := transport.requests.size()
	client.list_entitlements(
		func(response: Dictionary) -> void: refresh_failure_results.append(response)
	)
	if transport.requests.size() == initial_index + 1:
		transport.respond(initial_index, _failure_response(401, "session_expired"))
		var refresh_index := transport.requests.size() - 1
		transport.respond(refresh_index, _failure_response(503, "refresh_unavailable"))
		_expect(
			transport.requests.size() == refresh_index + 1,
			"refresh 실패 뒤 IAP 요청을 replay했다",
		)
		_expect(
			refresh_failure_results.size() == 1
				and String(refresh_failure_results[0].get("code", "")) == "refresh_unavailable",
			"refresh 실패 callback이 정확히 한 번 오지 않았다: %s" % refresh_failure_results,
		)
	else:
		_fail("refresh 실패 probe의 최초 IAP 요청이 만들어지지 않았다")

	client.free()


func _probe_time_unix_ms() -> int:
	return _probe_unix_ms


func _session_result(token: String, refresh_token: String, expires_in: int) -> Dictionary:
	return {
		"platformToken": token,
		"refreshToken": refresh_token,
		"platformUserId": "pu_probe",
		"supportCode": "SUPPORT",
		"appUserId": "app-user",
		"isAnonymous": false,
		"expiresIn": expires_in,
	}


func _session_response(token: String, refresh_token: String, expires_in: int) -> Dictionary:
	return _success_response(_session_result(token, refresh_token, expires_in))


func _success_response(result: Dictionary) -> Dictionary:
	return {
		"valid": true,
		"ok": true,
		"result": result,
		"code": "",
		"message": "",
		"local": false,
		"http_status": 200,
	}


func _failure_response(status: int, code: String) -> Dictionary:
	return {
		"valid": true,
		"ok": false,
		"result": {},
		"code": code,
		"message": code,
		"local": false,
		"http_status": status,
	}


func _network_failure_response() -> Dictionary:
	return {
		"valid": false,
		"ok": false,
		"result": {},
		"code": "network_error",
		"message": "network_error",
		"local": true,
		"http_status": 0,
	}


func _expect_iap_request(
	request: Dictionary,
	method: String,
	path: String,
	token: String,
) -> void:
	_expect(
		String(request.get("method", "")) == method
			and String(request.get("path", "")) == path
			and String(request.get("base_url", "")) == "https://iap.platform.invalid"
			and String(request.get("token", "")) == token
			and bool(request.get("no_retry", false)),
		"IAP 요청 계약이 다르다: %s" % request,
	)


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
	elif (
		bool(got[0].get("valid", true))
		or not bool(got[0].get("local", false))
		or int(got[0].get("http_status", -1)) != 0
	):
		_fail("불완전한 로컬 인증 오류가 SDK envelope로 보정되지 않았다: %s" % got[0])

	# 정규화가 클라이언트를 거쳐도 같은 결과를 낸다
	var normalized := Normalizer.normalize({"is_new": true, "email": "x@y.z", "level": 3})
	if normalized != {"is_new": 1, "level": 3}:
		_fail("정규화 결과가 다르다: %s" % normalized)

	client.queue_free()


func _fail(message: String) -> void:
	_failures.append(message)


func _expect(condition: bool, message: String) -> void:
	if not condition:
		_fail(message)
