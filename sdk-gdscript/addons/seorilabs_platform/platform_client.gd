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

## 세션이 갱신되면 발생한다.
signal session_changed(session: Dictionary)

## 설정이 갱신되면 발생한다.
signal config_changed(config: Dictionary)

## 만료 몇 ms 전부터 미리 갱신할지.
##
## 여유가 없으면 요청이 날아가는 중에 토큰이 죽는다.
const REFRESH_MARGIN_MS := 60_000

## 한 번에 보낼 최대 이벤트 수.
const MAX_EVENT_BATCH := 20

## 이벤트 outbox 상한. 넘으면 오래된 것부터 버린다.
const MAX_EVENT_OUTBOX := 500

var _transport: HttpTransport
var _session: Dictionary = {}
var _iap_base_url := ""
var _ingest_base_url := ""
var _credential: Dictionary = {}
var _config: Dictionary = _fallback_config()

var _event_buffer: Array = []
var _event_outbox: Array = []
var _flushing := false
var _refreshing := false
var _refresh_waiters: Array[Callable] = []


func _ready() -> void:
	if _transport == null:
		_transport = HttpTransport.new()
		add_child(_transport)


## 설정한다. add_child 전에 불러도 된다.
##
## options 키:
##   base_url        : String (필수) — 세션·설정
##   iap_base_url    : String (선택) — 결제. 없으면 base_url
##   ingest_base_url : String (선택) — 이벤트. 없으면 base_url
##   app_id          : String (필수)
##   max_retries     : int (선택, 기본 3)
##
## 역할마다 Cloud Run 서비스가 다르다. 마켓 자격증명을 결제 서비스
## 하나에만 마운트하는 것이 경계라서 한 호스트로 합칠 수 없다.
func configure(options: Dictionary) -> void:
	if _transport == null:
		_transport = HttpTransport.new()
		add_child(_transport)

	var base := String(options.get("base_url", ""))
	_iap_base_url = String(options.get("iap_base_url", "")).strip_edges()
	if _iap_base_url.is_empty():
		_iap_base_url = base
	_ingest_base_url = String(options.get("ingest_base_url", "")).strip_edges()
	if _ingest_base_url.is_empty():
		_ingest_base_url = base

	_transport.configure(
		base,
		String(options.get("app_id", "")),
		int(options.get("max_retries", 3)),
	)


# ---------------------------------------------------------------- 인증

## 자격증명으로 세션을 연다.
##
## 자격증명을 보관하는 이유는 refresh가 실패했을 때 다시 로그인하기
## 위해서다. 앱이 매번 Firebase 토큰을 다시 받아오게 하면 Godot에서
## 호출 앞에 왕복이 두 번씩 붙는다.
func sign_in(credential: Dictionary, callback: Callable = Callable()) -> void:
	_credential = credential.duplicate(true)

	_transport.request(
		{
			"method": "POST",
			"path": "/v1/auth/session",
			"body": {"credential": credential},
		},
		func(response: Dictionary) -> void:
			if response.get("ok", false):
				_store_session(response["result"])
				# 세션 응답에 설정이 얹혀 오면 캐시를 채운다.
				# 앱 시작 시 왕복이 하나 준다.
				var result: Dictionary = response["result"]
				if result.has("config") and typeof(result["config"]) == TYPE_DICTIONARY:
					_config = result["config"]
					config_changed.emit(_config)
			_invoke(callback, response)
	)


func sign_out() -> void:
	_session = {}
	_credential = {}
	session_changed.emit({})


## 현재 세션. 없으면 빈 Dictionary다.
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

	# 갱신이 겹치지 않게 묶는다. 겹치면 서버가 refresh token을
	# 회전시키는 순간 나머지가 무효 토큰을 들고 실패한다.
	_refresh_waiters.append(callback)
	if _refreshing:
		return

	_refreshing = true
	_refresh()


func _needs_refresh() -> bool:
	var expires_at := int(_session.get("expiresAt", 0))
	return Time.get_ticks_msec() + REFRESH_MARGIN_MS >= expires_at


func _refresh() -> void:
	_transport.request(
		{
			"method": "POST",
			"path": "/v1/auth/refresh",
			"body": {"refreshToken": String(_session.get("refreshToken", ""))},
		},
		func(response: Dictionary) -> void:
			if response.get("ok", false):
				_store_session(response["result"])
				_resolve_refresh(String(_session.get("platformToken", "")), {})
				return

			# refresh가 만료됐거나 폐기됐다.
			# 자격증명이 있으면 다시 로그인한다.
			var status := int(response.get("http_status", 0))
			if not _credential.is_empty() and (status == 401 or status == 403):
				_session = {}
				sign_in(_credential, func(retry: Dictionary) -> void:
					if retry.get("ok", false):
						_resolve_refresh(String(_session.get("platformToken", "")), {})
					else:
						_resolve_refresh("", retry)
				)
				return

			_resolve_refresh("", response)
	)


func _resolve_refresh(token: String, error: Dictionary) -> void:
	_refreshing = false
	var waiters := _refresh_waiters.duplicate()
	_refresh_waiters.clear()

	for waiter in waiters:
		if waiter.is_valid():
			waiter.call(token, error)


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
		"expiresAt": Time.get_ticks_msec() + int(result.get("expiresIn", 3600)) * 1000,
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
		_transport.request(
			{
				"method": "POST",
				"path": "/v1/events",
				"base_url": _ingest_base_url,
				"token": token,
				"body": {"events": batch},
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


## 이벤트 식별자를 만든다. 서버가 중복 제거에 쓴다.
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
				_config = response["result"]
				config_changed.emit(_config)
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


## 점검 중인지 본다.
func is_under_maintenance() -> bool:
	var maintenance: Dictionary = _config.get("maintenance", {})
	return bool(maintenance.get("active", false))


## SDK가 차단됐는지 본다. 강제 업데이트 판단에 쓴다.
func is_sdk_blocked() -> bool:
	var sdk: Dictionary = _config.get("sdk", {})
	return String(sdk.get("status", "ok")) == "blocked"


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
## 재시도하지 않는다. 결제 경로에서 같은 요청을 자동으로 반복하면
## 서버가 멱등이라 해도 응답을 기다리는 사이 사용자가 두 번
## 결제한 것처럼 보이는 상황을 만든다.
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

	with_token(func(session_token: String, error: Dictionary) -> void:
		if session_token.is_empty():
			callback.call(_client_error(
				String(error.get("code", "auth_required")),
				String(error.get("message", "로그인이 필요해요")),
			))
			return

		_transport.request(
			{
				"method": "POST",
				"path": "/v1/iap/verify",
				"base_url": _iap_base_url,
				"token": session_token,
				"no_retry": true,
				"body": {
					"platform": platform,
					"productId": product_id,
					"token": token,
				},
			},
			callback,
		)
	)


## 활성 entitlement 목록.
##
## 마켓 SDK 없이도 환불 반영을 확인할 수 있는 경로다.
func list_entitlements(callback: Callable) -> void:
	with_token(func(session_token: String, error: Dictionary) -> void:
		if session_token.is_empty():
			callback.call(_client_error(
				String(error.get("code", "auth_required")),
				String(error.get("message", "로그인이 필요해요")),
			))
			return

		_transport.request(
			{
				"method": "GET",
				"path": "/v1/iap/entitlements",
				"base_url": _iap_base_url,
				"token": session_token,
			},
			callback,
		)
	)


## 신규 구매 전에 마켓 결제 화면에 넣을 계정 참조.
func account_references(callback: Callable) -> void:
	with_token(func(session_token: String, error: Dictionary) -> void:
		if session_token.is_empty():
			callback.call(_client_error(
				String(error.get("code", "auth_required")),
				String(error.get("message", "로그인이 필요해요")),
			))
			return

		_transport.request(
			{
				"method": "POST",
				"path": "/v1/iap/account-references",
				"base_url": _iap_base_url,
				"token": session_token,
			},
			callback,
		)
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
