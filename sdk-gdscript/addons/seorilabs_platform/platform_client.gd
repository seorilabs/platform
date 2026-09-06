## Seorilabs 공통 플랫폼 SDK 진입점.
##
## 앱은 이 노드 하나만 알면 된다. 인증·이벤트·설정·결제가
## 같은 전송 계층과 세션을 공유한다.
##
##   var platform := SeoriPlatformClient.new()
##   platform.configure({"base_url": "https://...", "app_id": "lizard-tycoon"})
##   add_child(platform)
##   platform.sign_in({"kind": "firebase-id-token", "value": id_token},
##       func(res): print(res))
##
## GDScript에는 async/await 체인이 없어 콜백을 쓴다.
## 콜백은 항상 Dictionary 하나를 받고, ok 키로 성패를 가른다.
class_name SeoriPlatformClient
extends Node

# 상대 경로로 preload한다. 애드온이 어느 경로에 vendoring되든 동작해야 한다.
# res:// 절대 경로를 쓰면 소비자가 game/addons/ 아래에 두는 순간 깨진다.
const HttpTransport := preload("core/http_transport.gd")
const Normalizer := preload("core/param_normalizer.gd")
const PresenceClient := preload("core/presence_client.gd")
const AtomicJsonStore := preload("core/atomic_json_store.gd")
const UpdateGate := preload("core/update_gate.gd")

## SDK 버전. 이벤트 context와 배포본 VERSION 파일이 같은 값을 사용한다.
const SDK_VERSION := "0.7.0"

## 세션이 갱신되면 발생한다.
signal session_changed(session: Dictionary)

## 설정이 갱신되면 발생한다.
signal config_changed(config: Dictionary)

## 업데이트 게이트 상태가 바뀌면 발생한다.
##
## 자기 UI로 그리는 앱은 config_changed 대신 이걸 구독하면 semver를 볼
## 필요가 없다. 판정은 서버가 이미 끝냈다.
signal update_gate_changed(state: Dictionary)

## 정책이 이겼는데 서버가 문구를 주지 않았을 때 쓴다.
const DEFAULT_REQUIRED_MESSAGE := "업데이트가 필요해요. 스토어에서 최신 버전을 받아 주세요"
const DEFAULT_RECOMMENDED_MESSAGE := "새 버전이 나왔어요. 업데이트하면 더 편하게 쓸 수 있어요"
const DEFAULT_MAINTENANCE_MESSAGE := "지금 점검 중이에요. 잠시 후 다시 시도해 주세요"

## 권장 안내를 다시 띄우기까지의 간격.
##
## 매 실행마다 띄우면 짜증나고, 버전당 1회면 한 번 닫은 유저가 그대로
## 구버전에 남는다. 하루 1회는 잔존하는 유저에게 반복 노출된다.
const RECOMMEND_PROMPT_INTERVAL_MS := 24 * 60 * 60 * 1000

## 권장 안내 노출 이력 파일.
const GATE_PROMPT_PATH := "user://seorilabs_platform_update_gate.json"

## 만료 몇 ms 전부터 미리 갱신할지.
##
## 여유가 없으면 요청이 날아가는 중에 토큰이 죽는다.
const REFRESH_MARGIN_MS := 60_000

## 한 번에 보낼 최대 이벤트 수.
const MAX_EVENT_BATCH := 20

## 이벤트 outbox 상한. 넘으면 오래된 것부터 버린다.
const MAX_EVENT_OUTBOX := 500

const EVENT_CONTEXT_PLATFORMS := ["android", "ios", "web", "ait"]
const MAX_APP_VERSION_LENGTH := 32
const MAX_LOCALE_LENGTH := 16
const MAX_GA4_CLIENT_ID_LENGTH := 64

var _transport: HttpTransport
var _presence: PresenceClient
var _gate: UpdateGate
var _session: Dictionary = {}
var _api_base_url := ""
var _app_id := ""
var _runtime := ""
var _iap_base_url := ""
var _ingest_base_url := ""
var _ads_base_url := ""
var _auth_base_url := ""
var _credential: Dictionary = {}
var _config: Dictionary = _fallback_config()
var _event_context_source: Variant = {}

var _event_buffer: Array = []
var _event_outbox: Array = []
var _flushing := false
var _auth_generation := 0
var _refreshing := false
var _refresh_waiters: Array[Dictionary] = []
var _refresh_reauthenticating := false
var _refresh_failure: Dictionary = {}
var _refresh_flight_sequence := 0
var _active_refresh_flight_id := 0
var _active_refresh_generation := 0
# 테스트는 wall clock만 전진시켜 기기 sleep을 재현한다. 제품에서는
# 항상 Time.get_unix_time_from_system()을 사용한다.
var _unix_time_ms_source: Callable = Callable()


func _ready() -> void:
	if _transport == null:
		_transport = HttpTransport.new()
		add_child(_transport)
	_ensure_presence()


## 설정한다. add_child 전에 불러도 된다.
##
## options 키:
##   base_url        : String (필수) — 세션·설정
##   iap_base_url    : String (선택) — 결제. 없으면 base_url
##   ingest_base_url : String (선택) — 이벤트. 없으면 base_url
##   ads_base_url    : String (선택) — 광고 정책·claim. 있으면 세션도 이 역할에서 발급
##   auth_base_url   : String (선택) — 세션 발급·갱신. 없으면 ads_base_url 또는 base_url
##   app_id          : String (필수)
##   event_context   : Dictionary | Callable (선택) — platform/appVersion/locale/ga4ClientId
##   runtime         : String (선택) — 실행 환경. 예 `godot-native-android`, `godot-web-ait`
##   max_retries     : int (선택, 기본 3)
##   presence_enabled: bool (선택, 기본 false) — fail-open RPI Edge heartbeat
##
## 역할마다 Cloud Run 서비스가 다르다. 마켓 자격증명을 결제 서비스
## 하나에만 마운트하는 것이 경계라서 한 호스트로 합칠 수 없다.
func configure(options: Dictionary) -> void:
	if _transport == null:
		_transport = HttpTransport.new()
		add_child(_transport)

	var base := String(options.get("base_url", ""))
	_api_base_url = base.strip_edges()
	_app_id = String(options.get("app_id", "")).strip_edges()
	_iap_base_url = String(options.get("iap_base_url", "")).strip_edges()
	if _iap_base_url.is_empty():
		_iap_base_url = base
	_ingest_base_url = String(options.get("ingest_base_url", "")).strip_edges()
	if _ingest_base_url.is_empty():
		_ingest_base_url = base
	_ads_base_url = String(options.get("ads_base_url", "")).strip_edges()
	if _ads_base_url.is_empty():
		_ads_base_url = base
	_auth_base_url = String(options.get("auth_base_url", "")).strip_edges()
	if _auth_base_url.is_empty():
		_auth_base_url = _ads_base_url if options.has("ads_base_url") else base
	_event_context_source = options.get("event_context", {})
	if typeof(_event_context_source) == TYPE_DICTIONARY:
		_event_context_source = (_event_context_source as Dictionary).duplicate(true)
	_runtime = String(options.get("runtime", "")).strip_edges()

	_transport.configure(
		base,
		_app_id,
		int(options.get("max_retries", 3)),
	)
	_transport.set_client_context(SDK_VERSION, _client_context)
	_ensure_presence()
	_presence.configure({
		"enabled": bool(options.get("presence_enabled", false)),
		"token_base_url": _ingest_base_url,
		"app_id": _app_id,
		"context": _event_context_source,
	})


func _ensure_presence() -> void:
	if is_instance_valid(_presence):
		return
	_presence = PresenceClient.new()
	add_child(_presence)


## 앱 foreground 복귀를 직접 감지하는 host는 이 메서드로 fresh heartbeat를 요청한다.
func resume_presence() -> void:
	if is_instance_valid(_presence):
		_presence.resume()


# ---------------------------------------------------------------- 인증

## 앱 Firebase 프로젝트용 custom token을 발급받는다.
##
## 신규 사용자 요청은 응답 유실 뒤 재시도하면 서로 다른 uid가 생길 수 있어
## 자동 재시도를 금지한다. 반환 token은 Firebase signInWithCustomToken에 한 번
## 사용하고 저장하지 않는다.
func create_firebase_custom_token(
	existing_firebase_id_token: String,
	app_check_token: String,
	callback: Callable,
) -> void:
	var body := {"appId": _app_id}
	if not existing_firebase_id_token.is_empty():
		body["existingFirebaseIdToken"] = existing_firebase_id_token

	_transport.request(
		{
			"method": "POST",
			"path": "/v1/auth/firebase-custom-token",
			"base_url": _api_base_url,
			"body": body,
			"app_check_token": app_check_token,
			"no_retry": true,
		},
		callback,
	)


## Firebase uid에 연결된 Platform identity mapping을 지운다.
## 앱은 이 요청이 성공한 뒤 자기 Firebase Auth 사용자와 데이터를 지운다.
func delete_firebase_account(
	firebase_id_token: String,
	app_check_token: String,
	callback: Callable,
) -> void:
	if firebase_id_token.is_empty():
		callback.call(_client_error("auth_required", "로그인이 필요해요"))
		return

	_transport.request(
		{
			"method": "DELETE",
			"path": "/v1/auth/firebase-account",
			"base_url": _api_base_url,
			"body": {"appId": _app_id, "firebaseIdToken": firebase_id_token},
			"app_check_token": app_check_token,
		},
		callback,
	)

## 자격증명으로 세션을 연다.
##
## 자격증명을 보관하는 이유는 refresh가 실패했을 때 다시 로그인하기
## 위해서다. 앱이 매번 Firebase 토큰을 다시 받아오게 하면 Godot에서
## 호출 앞에 왕복이 두 번씩 붙는다.
func sign_in(credential: Dictionary, callback: Callable = Callable()) -> void:
	var requested_credential := credential.duplicate(true)
	var had_session := not _session.is_empty()
	_auth_generation += 1
	var requested_generation := _auth_generation
	_credential = requested_credential.duplicate(true)
	_session = {}
	if had_session:
		session_changed.emit({})
	# session_changed subscriber가 동기적으로 sign_out이나 다른 sign_in을
	# 시작했으면 이 요청은 더 이상 현재 인증 세대에 속하지 않는다.
	if requested_generation != _auth_generation:
		_invoke(callback, _auth_state_changed_error())
		return
	_cancel_refresh_flight(_auth_state_changed_error())
	_request_sign_in(requested_credential, requested_generation, callback)


## 같은 인증 세대에서 세션을 발급한다.
##
## public sign_in은 세대를 올리지만 refresh 401 뒤 내부 fallback은 같은 사용자
## flight이므로 이 함수를 직접 사용한다. 그 사이 외부 sign_in/sign_out이 세대를
## 바꾸면 늦은 응답을 저장하지 않는다.
func _request_sign_in(
	credential: Dictionary,
	auth_generation: int,
	callback: Callable,
) -> void:

	_transport.request(
		{
			"method": "POST",
			"path": "/v1/auth/session",
			"base_url": _auth_base_url,
			"body": {"credential": credential},
		},
		func(response: Dictionary) -> void:
			if auth_generation != _auth_generation:
				_invoke(callback, _auth_state_changed_error())
				return
			if response.get("ok", false):
				_store_session(response["result"])
				# session_changed subscriber가 동기적으로 sign_out/account switch를
				# 시작했으면 성공 응답의 후속 효과와 callback도 폐기한다.
				if auth_generation != _auth_generation:
					_invoke(callback, _auth_state_changed_error())
					return
				# 세션 응답에 설정이 얹혀 오면 캐시를 채운다.
				# 앱 시작 시 왕복이 하나 준다.
				_seed_config_from_session(response["result"])
			_invoke(callback, response)
	)


func sign_out() -> void:
	_auth_generation += 1
	_session = {}
	_credential = {}
	_cancel_refresh_flight(_auth_state_changed_error())
	session_changed.emit({})


## 현재 세션. 없으면 빈 Dictionary다.
## expiresAt은 Unix epoch millisecond다.
func current_session() -> Dictionary:
	return _session.duplicate(true)


func is_signed_in() -> bool:
	return not _session.is_empty()


## 익명 신원인지 본다.
##
## 익명은 결제할 수 없다. getAnonymousKey 해시는 bearer 자격증명이
## 아니라 타인 사칭이 가능하기 때문이다.
func is_anonymous() -> bool:
	return bool(_session.get("isAnonymous", false))


## 유효한 토큰을 콜백으로 준다. 필요하면 갱신한다.
##
## 콜백은 (token: String, error: Dictionary)를 받는다.
## 토큰이 비면 error를 본다.
func with_token(callback: Callable) -> void:
	if _session.is_empty():
		callback.call("", {
			"code": "auth_required",
			"message": "로그인이 필요해요",
			"local": true,
		})
		return

	if not _needs_refresh():
		callback.call(String(_session.get("platformToken", "")), {})
		return

	_queue_refresh(callback, true)


## refresh 요청을 single-flight로 묶는다.
##
## 겹치면 서버가 refresh token을 회전시키는 순간 나머지가 무효 토큰을
## 들고 실패하므로 proactive refresh와 401 복구가 같은 큐를 써야 한다.
## 다만 IAP 401 waiter는 refresh 실패 뒤 자격증명 재로그인에 참여하지 않는다.
func _queue_refresh(
	callback: Callable,
	allow_sign_in_fallback: bool,
) -> void:
	# proactive refresh의 재로그인이 이미 진행 중이면 strict IAP waiter는
	# 그 재로그인 토큰으로 원 요청을 replay하지 않는다.
	if _refresh_reauthenticating and not allow_sign_in_fallback:
		callback.call("", _refresh_failure)
		return

	_refresh_waiters.append({
		"callback": callback,
		"allow_sign_in_fallback": allow_sign_in_fallback,
	})
	if _refreshing:
		return

	_refresh_flight_sequence += 1
	_active_refresh_flight_id = _refresh_flight_sequence
	_active_refresh_generation = _auth_generation
	_refreshing = true
	_refresh(
		_active_refresh_flight_id,
		_active_refresh_generation,
		String(_session.get("refreshToken", "")),
	)


func _needs_refresh() -> bool:
	var expires_at := int(_session.get("expiresAt", 0))
	return _now_unix_ms() + REFRESH_MARGIN_MS >= expires_at


func _now_unix_ms() -> int:
	if _unix_time_ms_source.is_valid():
		return int(_unix_time_ms_source.call())
	return int(Time.get_unix_time_from_system() * 1000.0)


## 서버가 방금 거부한 토큰을 강제로 갱신한다.
##
## 다른 요청이 이미 같은 토큰을 갱신했다면 새 토큰을 재사용한다. 그렇지
## 않으면 proactive refresh와 같은 single-flight 큐에 합류한다.
func _refresh_after_session_expired(failed_token: String, callback: Callable) -> void:
	if _refresh_reauthenticating:
		_queue_refresh(callback, false)
		return

	if _session.is_empty():
		callback.call("", _client_error("auth_required", "로그인이 필요해요"))
		return

	var current_token := String(_session.get("platformToken", ""))
	if not current_token.is_empty() and current_token != failed_token:
		callback.call(current_token, {})
		return

	# IAP 401 복구는 refresh 자체의 5xx/timeout도 재시도하지 않으며,
	# refresh 401/403 뒤 보관 자격증명으로 다시 로그인하지 않는다.
	_queue_refresh(callback, false)


func _refresh(flight_id: int, auth_generation: int, refresh_token: String) -> void:
	var request_data := {
		"method": "POST",
		"path": "/v1/auth/refresh",
		"base_url": _auth_base_url,
		"body": {"refreshToken": refresh_token},
		# strict waiter가 이미 진행 중인 proactive flight에 합류해도
		# refresh 5xx/timeout backoff를 기다리지 않도록 항상 한 번만 보낸다.
		"no_retry": true,
	}

	_transport.request(
		request_data,
		func(response: Dictionary) -> void:
			if not _is_current_refresh_flight(flight_id, auth_generation):
				return
			if response.get("ok", false):
				_store_session(response["result"])
				_resolve_refresh(
					flight_id,
					String(_session.get("platformToken", "")),
					{},
				)
				return

			# proactive waiter만 refresh token 폐기 뒤 자격증명으로 다시 로그인한다.
			# strict IAP waiter는 먼저 실패시켜 원 요청 replay를 막는다.
			var status := int(response.get("http_status", 0))
			var fallback_waiters: Array[Dictionary] = []
			var strict_waiters: Array[Dictionary] = []
			for waiter in _refresh_waiters:
				if bool(waiter.get("allow_sign_in_fallback", false)):
					fallback_waiters.append(waiter)
				else:
					strict_waiters.append(waiter)

			if (
				not _credential.is_empty()
				and (status == 401 or status == 403)
				and not fallback_waiters.is_empty()
			):
				var fallback_credential := _credential.duplicate(true)
				_refresh_waiters = fallback_waiters
				_refresh_reauthenticating = true
				_refresh_failure = response.duplicate(true)
				# strict callback이 동기적으로 새 요청을 시작해도 폐기된 토큰을
				# 다시 받지 않도록 세션을 먼저 비운다.
				_session = {}
				for waiter in strict_waiters:
					_invoke_refresh_waiter(waiter, "", response)
				if not _is_current_refresh_flight(flight_id, auth_generation):
					return
				_request_sign_in(
					fallback_credential,
					auth_generation,
					func(retry: Dictionary) -> void:
						if retry.get("ok", false):
							_resolve_refresh(
								flight_id,
								String(_session.get("platformToken", "")),
								{},
							)
						else:
							_resolve_refresh(flight_id, "", retry)
				)
				return

			_resolve_refresh(flight_id, "", response)
	)


func _is_current_refresh_flight(flight_id: int, auth_generation: int) -> bool:
	return (
		_refreshing
		and _active_refresh_flight_id == flight_id
		and _active_refresh_generation == auth_generation
		and _auth_generation == auth_generation
	)


func _resolve_refresh(flight_id: int, token: String, error: Dictionary) -> void:
	if not _refreshing or _active_refresh_flight_id != flight_id:
		return

	var waiters := _refresh_waiters.duplicate()
	_reset_refresh_flight()

	for waiter in waiters:
		_invoke_refresh_waiter(waiter, token, error)


func _cancel_refresh_flight(error: Dictionary) -> void:
	if not _refreshing:
		return

	var waiters := _refresh_waiters.duplicate()
	_reset_refresh_flight()

	for waiter in waiters:
		_invoke_refresh_waiter(waiter, "", error)


func _reset_refresh_flight() -> void:
	_refreshing = false
	_refresh_reauthenticating = false
	_refresh_failure = {}
	_active_refresh_flight_id = 0
	_active_refresh_generation = 0
	_refresh_waiters.clear()


func _invoke_refresh_waiter(waiter: Dictionary, token: String, error: Dictionary) -> void:
	var callback: Callable = waiter.get("callback", Callable())
	if callback.is_valid():
		callback.call(token, error)


func _store_session(result: Dictionary) -> void:
	_session = {
		"platformToken": String(result.get("platformToken", "")),
		"refreshToken": String(result.get("refreshToken", "")),
		"platformUserId": String(result.get("platformUserId", "")),
		# 앱 설정 화면이 보여줄 식별자다. Firebase uid를 보여주면 CS가
		# 그걸로 원장을 찾을 수 없다.
		"supportCode": String(result.get("supportCode", "")),
		"appUserId": String(result.get("appUserId", "")),
		"isAnonymous": bool(result.get("isAnonymous", false)),
		# 공개 current_session의 expiresAt은 기기 sleep과 무관한 Unix epoch ms다.
		"expiresAt": _now_unix_ms() + int(result.get("expiresIn", 3600)) * 1000,
	}
	session_changed.emit(current_session())


# ---------------------------------------------------------------- 이벤트

## 이벤트를 기록한다. 즉시 보내지 않는다.
##
## 실패해도 아무 일도 일어나지 않는다. 계측 때문에 게임이 멈추면 안 된다.
func track(event_name: String, params: Dictionary = {}) -> void:
	if event_name.is_empty():
		return

	# 필드 이름은 서버 계약을 따른다. 서버가 미지 필드를 거부하므로
	# 여기서 다른 이름을 쓰면 배치 전체가 400으로 떨어진다.
	#
	# eventId가 없으면 서버가 배치를 받아들이면서도 그 이벤트를 버린다.
	# 응답이 200이라 여기서는 성공으로 알고 outbox에서 지운다 —
	# 조용히 유실된다.
	_event_buffer.append({
		"eventId": _new_event_id(),
		"name": event_name,
		"params": Normalizer.normalize(params),
		"tsUnixMs": int(Time.get_unix_time_from_system() * 1000.0),
	})

	if _event_buffer.size() >= MAX_EVENT_BATCH:
		flush_events()


## 앱의 canonical analytics envelope를 받아 같은 eventId/발생시각을 보존한다.
## 서버 계약에 없는 envelope 필드는 전송하지 않는다.
func track_event(event: Dictionary) -> void:
	var event_id := String(event.get("event_id", "")).strip_edges()
	var event_name := String(event.get("name", "")).strip_edges()
	var occurred_at_micros := int(event.get("occurred_at_micros", 0))
	var params = event.get("params", null)
	if event_id.length() != 32 or event_name.is_empty() or occurred_at_micros <= 0 \
			or not (params is Dictionary):
		return
	_event_buffer.append({
		"eventId": event_id,
		"name": event_name,
		"params": Normalizer.normalize(params),
		"tsUnixMs": int(occurred_at_micros / 1000),
	})
	if _event_buffer.size() >= MAX_EVENT_BATCH:
		flush_events()


## 버퍼와 outbox를 보낸다.
func flush_events(callback: Callable = Callable()) -> void:
	# 동시 전송을 막는다. 겹치면 같은 이벤트가 두 번 나간다.
	if _flushing:
		_invoke(callback, {"ok": true, "result": {}})
		return

	var combined: Array = _event_outbox + _event_buffer
	_event_outbox = []
	_event_buffer = []

	if combined.is_empty():
		_invoke(callback, {"ok": true, "result": {}})
		return

	var batch := combined.slice(0, MAX_EVENT_BATCH)
	var overflow := combined.slice(MAX_EVENT_BATCH)
	if not overflow.is_empty():
		_push_outbox(overflow)

	_flushing = true

	# 세션이 없어도 익명 수집이 동작해야 한다.
	var send := func(token: String) -> void:
		var body := {"events": batch}
		var context := _resolved_event_context()
		if not context.is_empty():
			body["context"] = context
		_transport.request(
			{
				"method": "POST",
				"path": "/v1/events",
				"base_url": _ingest_base_url,
				"token": token,
				"body": body,
			},
			func(response: Dictionary) -> void:
				_flushing = false
				if not response.get("ok", false):
					# 실패한 배치는 outbox로. 다음 flush에서 다시 시도한다.
					_push_outbox(batch)
				_invoke(callback, response)
		)

	if _session.is_empty():
		send.call("")
	else:
		with_token(func(token: String, _error: Dictionary) -> void:
			send.call(token)
		)


## 이벤트 식별자를 만든다. 적재 행 추적과 소비자 중복 판정에 쓴다.
func _new_event_id() -> String:
	# Godot에는 UUID가 없다. 무작위 16바이트를 hex로 쓴다.
	var bytes := PackedByteArray()
	bytes.resize(16)
	for i in 16:
		bytes[i] = randi() % 256
	return bytes.hex_encode()


func _push_outbox(events: Array) -> void:
	_event_outbox.append_array(events)
	# 무한히 쌓으면 오프라인이 길어질 때 메모리를 다 쓴다.
	if _event_outbox.size() > MAX_EVENT_OUTBOX:
		var excess := _event_outbox.size() - MAX_EVENT_OUTBOX
		_event_outbox = _event_outbox.slice(excess)


func pending_event_count() -> int:
	return _event_buffer.size() + _event_outbox.size()


## OpenAPI EventContext에 선언된 비식별 실행 정보만 반환한다.
## Callable을 flush 시점에 평가해 런타임 언어 변경이 다음 배치부터 반영된다.
func _resolved_event_context() -> Dictionary:
	var raw: Variant = _event_context_source
	if typeof(raw) == TYPE_CALLABLE:
		var provider: Callable = raw
		if provider.is_valid():
			raw = provider.call()

	var source: Dictionary = {}
	if typeof(raw) == TYPE_DICTIONARY:
		source = raw
	var context := {"sdkVersion": SDK_VERSION}

	var platform := String(source.get("platform", "")).strip_edges().to_lower()
	if platform in EVENT_CONTEXT_PLATFORMS:
		context["platform"] = platform

	var app_version := _bounded_context_value(source.get("appVersion", ""), MAX_APP_VERSION_LENGTH)
	if not app_version.is_empty():
		context["appVersion"] = app_version

	var locale := _bounded_context_value(source.get("locale", ""), MAX_LOCALE_LENGTH)
	if not locale.is_empty():
		context["locale"] = locale

	var ga4_client_id := _bounded_context_value(source.get("ga4ClientId", ""), MAX_GA4_CLIENT_ID_LENGTH)
	if not ga4_client_id.is_empty():
		context["ga4ClientId"] = ga4_client_id

	return context


## 관측 헤더에 실을 실행 환경.
##
## 앱이 이미 event_context로 주는 버전을 그대로 쓴다. 같은 사실을 두 군데
## 설정하게 만들지 않는다. runtime은 event_context에 없는 축이라 따로 받는다.
func _client_context() -> Dictionary:
	var context := {}
	if not _runtime.is_empty():
		context["runtime"] = _runtime
	var event_context := _resolved_event_context()
	if event_context.has("appVersion"):
		context["appVersion"] = event_context["appVersion"]
	return context


func _bounded_context_value(value: Variant, max_length: int) -> String:
	var clean := String(value).strip_edges()
	if clean.length() > max_length:
		return clean.substr(0, max_length)
	return clean


# ---------------------------------------------------------------- 설정

## 설정을 가져온다.
##
## 실패하면 마지막 캐시를 주고, 그것도 없으면 열린 기본값을 준다.
## 설정 조회 실패가 앱 시작을 막으면 서버 장애가 전체 중단으로 번진다.
func fetch_config(target: Dictionary, callback: Callable = Callable()) -> void:
	_transport.request(
		{
			"method": "GET",
			"path": "/v1/config",
			"query": {
				"appVersion": String(target.get("app_version", "")),
				"platform": String(target.get("platform", "")),
				"locale": target.get("locale", null),
			},
		},
		func(response: Dictionary) -> void:
			if response.get("ok", false):
				_set_config(response["result"])
			_invoke(callback, {
				"ok": true,
				"result": _config,
				"code": "",
				"message": "",
			})
	)


## 마지막으로 받은 설정. 네트워크를 타지 않는다.
func current_config() -> Dictionary:
	return _config.duplicate(true)


func _set_config(config: Dictionary) -> void:
	_config = config
	config_changed.emit(_config)
	update_gate_changed.emit(update_gate_state())


## 세션 응답에 실려 온 설정을 캐시에 넣는다.
##
## 통째로 덮지 않고 병합한다. 세션 오버레이에는 values가 없어서, 덮으면
## 앞서 /v1/config로 받은 값이 사라진다.
##
## 네 필드가 하나도 없으면 서버가 설정을 싣지 못한 것이다. 그때는 캐시를
## 건드리지 않는다.
func _seed_config_from_session(result: Dictionary) -> void:
	var has_overlay := (
		result.has("sdk") or result.has("maintenance") or result.has("features")
	)
	if not has_overlay:
		return

	var merged := _config.duplicate(true)
	for key in ["features", "sdk", "maintenance"]:
		if result.has(key) and typeof(result[key]) == TYPE_DICTIONARY:
			merged[key] = result[key]
	_set_config(merged)


## 점검 중인지 본다.
##
## vendoring된 0.6.x를 쓰는 앱들이 부르고 있어 남긴다.
func is_under_maintenance() -> bool:
	return String(update_gate_state().get("kind", "ok")) == "maintenance"


## SDK가 차단됐는지 본다.
##
## vendoring된 0.6.x를 쓰는 앱들이 부르고 있어 남긴다. 새 코드는
## update_gate_state()를 쓴다.
func is_sdk_blocked() -> bool:
	var sdk: Dictionary = _config.get("sdk", {})
	return String(sdk.get("status", "ok")) == "blocked"


## 지금 앱이 무엇을 보여줘야 하는지.
##
## 버전을 비교하지 않는다. 서버가 X-Seori-AppVer와 X-Seori-Runtime을 보고
## 이미 판정했다. 같은 비교 구현을 TS와 GDScript에 두 벌 두면 언젠가 갈라진다.
##
## 반환 키: kind(ok|recommended|required|maintenance), message,
## update_url, recommended_version, until
func update_gate_state() -> Dictionary:
	return _gate_state_of(_config)


## 계약이 정본이라 conformance probe가 같은 함수를 직접 부른다.
static func _gate_state_of(config: Dictionary) -> Dictionary:
	var maintenance: Dictionary = config.get("maintenance", {})
	if bool(maintenance.get("active", false)):
		# 점검이 강제보다 우선한다. 시간이 정해져 있고 자동으로 해제되며
		# 안내 문구가 더 행동 가능하다.
		var state := {
			"kind": "maintenance",
			"message": _or_default(maintenance.get("message", ""), DEFAULT_MAINTENANCE_MESSAGE),
		}
		var until := String(maintenance.get("until", ""))
		if not until.is_empty():
			state["until"] = until
		return state

	var sdk: Dictionary = config.get("sdk", {})
	var status := String(sdk.get("status", "ok"))
	# 모르는 status는 ok로 읽는다. 서버가 상태를 추가해도 구버전 SDK가
	# 스스로를 막지 않는다.
	if status != "blocked" and status != "deprecated":
		return {"kind": "ok"}

	var kind := "required" if status == "blocked" else "recommended"
	var fallback := DEFAULT_REQUIRED_MESSAGE if status == "blocked" else DEFAULT_RECOMMENDED_MESSAGE
	var out := {
		"kind": kind,
		"message": _or_default(sdk.get("message", ""), fallback),
	}
	var update_url := String(sdk.get("updateUrl", ""))
	if not update_url.is_empty():
		out["update_url"] = update_url
	var recommended := String(sdk.get("recommendedVersion", ""))
	if not recommended.is_empty():
		out["recommended_version"] = recommended
	return out


## 기본 게이트 오버레이를 띄운다. 이미 떠 있으면 상태만 갱신한다.
##
## 정상이면 아무것도 띄우지 않고 떠 있던 것을 내린다.
##
## 권장 안내는 하루 1회만 뜬다. 강제와 점검은 이력과 무관하게 항상 뜬다.
##
## options 키: labels(Dictionary: update, later), force(bool - 이력 무시)
func show_update_gate(options: Dictionary = {}) -> void:
	var state := update_gate_state()
	if String(state.get("kind", "ok")) == "ok":
		hide_update_gate()
		return

	# 이미 떠 있으면 이력과 무관하게 갱신한다. 노출 이력은 새로 띄울 때만
	# 본다. 닫을 수 없는 강제·점검 화면이 뜬 뒤 상태가 권장으로 바뀌었는데
	# 이력 때문에 갱신을 건너뛰면 유저가 옛 화면에 갇힌다.
	if _gate == null:
		if not bool(options.get("force", false)) and not _should_prompt(state):
			return
		_gate = UpdateGate.new()
		_gate.update_pressed.connect(_on_gate_update_pressed)
		_gate.later_pressed.connect(hide_update_gate)
		add_child(_gate)
	_gate.apply_state(state, options)
	_mark_prompted(state)


## 게이트를 내린다. 떠 있지 않으면 아무 일도 하지 않는다.
func hide_update_gate() -> void:
	if _gate == null:
		return
	_gate.queue_free()
	_gate = null


func _on_gate_update_pressed(url: String) -> void:
	OS.shell_open(url)


## 이 상태를 지금 띄워도 되는지 본다.
##
## 강제와 점검은 언제나 true다. 이력은 권장에만 적용된다.
func _should_prompt(state: Dictionary) -> bool:
	if String(state.get("kind", "ok")) != "recommended":
		return true

	var read: Dictionary = AtomicJsonStore.read_dictionary(GATE_PROMPT_PATH)
	if not bool(read.get("ok", false)) or not bool(read.get("exists", false)):
		return true
	var log: Dictionary = read.get("value", {})
	# 권장 기준이 올라갔으면 이력을 무시하고 다시 띄운다.
	if String(log.get("version", "")) != String(state.get("recommended_version", "")):
		return true
	var prompted_at := int(log.get("promptedAt", 0))
	var elapsed := _now_unix_ms() - prompted_at
	# 기기 시계가 과거로 교정되면 경과가 음수가 된다. 그대로 두면 미래
	# 시각에서 24시간이 더 지날 때까지 안내가 멈춘다.
	if elapsed < 0:
		return true
	return elapsed >= RECOMMEND_PROMPT_INTERVAL_MS


func _mark_prompted(state: Dictionary) -> void:
	if String(state.get("kind", "ok")) != "recommended":
		return
	# 저장 실패가 안내를 막으면 안 된다. 다음에 한 번 더 뜰 뿐이다.
	AtomicJsonStore.write(GATE_PROMPT_PATH, {
		"version": String(state.get("recommended_version", "")),
		"promptedAt": _now_unix_ms(),
	})


static func _or_default(value: Variant, fallback: String) -> String:
	var text := String(value).strip_edges()
	return fallback if text.is_empty() else text


static func _fallback_config() -> Dictionary:
	# 열린 상태로 둔다. 차단은 서버가 명시적으로 지시할 때만 한다.
	return {
		"values": {},
		"features": {},
		"sdk": {"status": "ok"},
		"maintenance": {"active": false},
	}


# ---------------------------------------------------------------- 결제

## 구매를 검증하고 지급받는다.
##
## 일반 전송 실패는 재시도하지 않는다. 인증 미들웨어가 원장에 닿기 전에
## 반환한 첫 401 session_expired만 refresh 후 한 번 replay한다. 결제 경로에서
## 다른 실패를 자동으로 반복하면 서버가 멱등이라 해도 응답을 기다리는 사이
## 사용자가 두 번 결제한 것처럼 보이는 상황을 만든다.
##
## proof 키: platform, product_id, token
func verify_purchase(proof: Dictionary, callback: Callable) -> void:
	var platform := String(proof.get("platform", ""))
	var product_id := String(proof.get("product_id", ""))
	var token := String(proof.get("token", ""))

	if platform.is_empty() or product_id.is_empty() or token.is_empty():
		callback.call(_client_error("purchase_proof_invalid", "구매 정보가 비어 있어요"))
		return

	# 익명은 결제할 수 없다. 서버도 막지만 왕복을 아낀다.
	if is_anonymous():
		callback.call(_client_error("anonymous_not_allowed", "로그인 후에 구매할 수 있어요"))
		return

	_iap_request(
		{
			"method": "POST",
			"path": "/v1/iap/verify",
			"body": {
				"platform": platform,
				"productId": product_id,
				"token": token,
			},
		},
		callback,
	)


## 활성 entitlement 목록.
##
## 마켓 SDK 없이도 환불 반영을 확인할 수 있는 경로다.
func list_entitlements(callback: Callable) -> void:
	_iap_request(
		{"method": "GET", "path": "/v1/iap/entitlements"},
		callback,
	)


## 신규 구매 전에 마켓 결제 화면에 넣을 계정 참조.
func account_references(callback: Callable) -> void:
	_iap_request(
		{"method": "POST", "path": "/v1/iap/account-references"},
		callback,
	)


## 인증이 필요한 IAP 요청의 단일 경로다.
##
## 전송 계층의 일반 재시도는 모두 끈다. 인증 미들웨어가 원장에 닿기 전에
## 거부한 첫 401 session_expired만 refresh 후 한 번 재전송한다. 다른 실패를
## 반복하지 않아 IAP 불변식 1·2의 멱등 원장과 별개인 클라이언트 완료 모호성을
## 만들지 않는다.
func _iap_request(request_data: Dictionary, callback: Callable) -> void:
	with_token(func(session_token: String, error: Dictionary) -> void:
		if session_token.is_empty():
			callback.call(_auth_error_response(error))
			return

		_send_iap_request(
			request_data,
			session_token,
			callback,
			false,
			_auth_generation,
		)
	)


func _send_iap_request(
	request_data: Dictionary,
	session_token: String,
	callback: Callable,
	auth_replayed: bool,
	request_auth_generation: int,
) -> void:
	if request_auth_generation != _auth_generation:
		callback.call(_auth_state_changed_error())
		return

	var authorized_request := request_data.duplicate(true)
	authorized_request["base_url"] = _iap_base_url
	authorized_request["token"] = session_token
	# 5xx, timeout, 403은 그대로 반환한다. 아래의 명시적 인증 replay만 허용한다.
	authorized_request["no_retry"] = true

	_transport.request(authorized_request, func(response: Dictionary) -> void:
		if request_auth_generation != _auth_generation:
			callback.call(_auth_state_changed_error())
			return
		if auth_replayed or not _is_session_expired_response(response):
			callback.call(response)
			return

		_refresh_after_session_expired(
			session_token,
			func(refreshed_token: String, error: Dictionary) -> void:
				if refreshed_token.is_empty():
					callback.call(_auth_error_response(error))
					return
				_send_iap_request(
					request_data,
					refreshed_token,
					callback,
					true,
					request_auth_generation,
				)
		)
	)


func _is_session_expired_response(response: Dictionary) -> bool:
	return (
		not bool(response.get("ok", false))
		and int(response.get("http_status", 0)) == 401
		and String(response.get("code", "")) == "session_expired"
	)


# ---------------------------------------------------------------- 광고

## 광고 정책을 읽는다. 실패 응답은 광고 허용이 아니다.
## 앱 adapter는 load 직전과 show 직전에 이 메서드를 호출하고 ok가 아니면 중단한다.
func get_ads_policy(callback: Callable) -> void:
	_ads_request("GET", "/v1/ads/policy", {}, callback)


func create_reward_claim(claim: Dictionary, callback: Callable) -> void:
	_ads_request("POST", "/v1/ads/reward-claims", claim, callback, true)


func get_reward_claim(claim_id: String, callback: Callable) -> void:
	_ads_request("GET", "/v1/ads/reward-claims/%s" % claim_id.uri_encode(), {}, callback)


## AppsInToss claim만 client_confirmed로 전이한다.
func confirm_reward_claim(claim_id: String, transaction_id: String, callback: Callable) -> void:
	_ads_request(
		"POST",
		"/v1/ads/reward-claims/%s/confirm" % claim_id.uri_encode(),
		{"transactionId": transaction_id},
		callback,
		true,
	)


## 로컬 exactly-once 보상 정산 뒤에만 호출한다.
func ack_reward_claim(claim_id: String, callback: Callable) -> void:
	_ads_request(
		"POST",
		"/v1/ads/reward-claims/%s/ack" % claim_id.uri_encode(),
		{},
		callback,
		true,
	)


func _ads_request(method: String, path: String, body: Dictionary, callback: Callable, no_retry := false) -> void:
	with_token(func(session_token: String, error: Dictionary) -> void:
		if session_token.is_empty():
			callback.call(_client_error(String(error.get("code", "auth_required")), String(error.get("message", "로그인이 필요해요"))))
			return
		var request := {"method": method, "path": path, "base_url": _ads_base_url, "token": session_token}
		if method == "POST" and not body.is_empty():
			request["body"] = body
		if no_retry:
			request["no_retry"] = true
		_transport.request(request, callback)
	)


# ---------------------------------------------------------------- 내부

func _invoke(callback: Callable, response: Dictionary) -> void:
	if callback.is_valid():
		callback.call(response)


func _client_error(code: String, message: String) -> Dictionary:
	return {
		"valid": false,
		"ok": false,
		"result": {},
		"code": code,
		"message": message,
		"local": true,
		"http_status": 0,
	}


func _auth_state_changed_error() -> Dictionary:
	return _client_error("auth_state_changed", "인증 상태가 바뀌었어요")


## 서버/전송 계층의 완전한 envelope는 status와 local 판정을 보존한다.
## with_token의 간단한 로컬 오류처럼 필드가 부족할 때만 SDK envelope로 감싼다.
func _auth_error_response(error: Dictionary) -> Dictionary:
	for key in ["valid", "ok", "result", "code", "message", "local", "http_status"]:
		if not error.has(key):
			return _client_error(
				String(error.get("code", "auth_required")),
				String(error.get("message", "로그인이 필요해요")),
			)
	return error.duplicate(true)
