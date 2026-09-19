## RPI Edge 최근 활성 heartbeat.
##
## 제품 요청과 HTTPRequest를 공유하지 않는다. DNS, TLS, RPI, DB 장애가 길어져도
## 이 노드의 2초 timeout만 소모하고 게임·인증·결제 호출은 그대로 진행한다.
class_name SeoriPresenceClient
extends Node

const Envelope := preload("envelope.gd")

const TOKEN_TIMEOUT_SEC := 5.0
const EDGE_TIMEOUT_SEC := 2.0
const NORMAL_INTERVAL_MS := 60_000
const MAX_BACKOFF_MS := 300_000
const JITTER_RATIO := 0.2
const MAX_RESPONSE_BYTES := 16 * 1024

var _enabled := false
var _running := false
var _in_flight := false
var _token_base_url := ""
var _app_id := ""
var _context_source: Variant = {}
var _session_id := ""
var _token := ""
var _token_expires_at_ticks := 0
var _edge_url := ""
var _interval_ms := NORMAL_INTERVAL_MS
var _failures := 0
var _sequence := 0
var _last_attempt_ticks := 0
var _phase := ""
var _active_context: Dictionary = {}
var _http: HTTPRequest
var _timer: Timer
var _rng := RandomNumberGenerator.new()


func _ready() -> void:
	_ensure_nodes()


func configure(options: Dictionary) -> void:
	_ensure_nodes()
	_enabled = bool(options.get("enabled", false))
	_token_base_url = String(options.get("token_base_url", "")).rstrip("/")
	_app_id = String(options.get("app_id", "")).strip_edges()
	_context_source = options.get("context", {})
	if typeof(_context_source) == TYPE_DICTIONARY:
		_context_source = (_context_source as Dictionary).duplicate(true)
	if _session_id.is_empty():
		_session_id = _new_session_id()
	if _enabled:
		start()
	else:
		stop()


## 즉시 반환한다. 네트워크는 deferred callback에서 시작한다.
func start() -> void:
	if not _enabled or _running:
		return
	_running = true
	call_deferred("_cycle")


func stop() -> void:
	_running = false
	if is_instance_valid(_timer):
		_timer.stop()


func resume() -> void:
	if not _running or _in_flight:
		return
	if Time.get_ticks_msec() - _last_attempt_ticks < NORMAL_INTERVAL_MS:
		return
	if is_instance_valid(_timer):
		_timer.stop()
	_cycle()


func _notification(what: int) -> void:
	if what == NOTIFICATION_APPLICATION_FOCUS_IN:
		resume()


func _cycle() -> void:
	if not _running or _in_flight:
		return
	var context := _resolved_context()
	if String(context.get("platform", "")).is_empty():
		_fail_and_schedule()
		return
	_in_flight = true
	_last_attempt_ticks = Time.get_ticks_msec()
	_active_context = context
	if _token.is_empty() or _token_expires_at_ticks <= Time.get_ticks_msec() + NORMAL_INTERVAL_MS:
		_request_token(context)
		return
	_request_heartbeat(context)


func _request_token(context: Dictionary) -> void:
	_phase = "token"
	_http.timeout = TOKEN_TIMEOUT_SEC
	var body := {
		"sessionId": _session_id,
		"platform": context["platform"],
	}
	if context.has("appVersion"):
		body["appVersion"] = context["appVersion"]
	var headers := PackedStringArray([
		"Content-Type: application/json",
		"X-Seori-App: " + _app_id,
	])
	var error := _http.request(
		_token_base_url + "/v1/presence/token",
		headers,
		HTTPClient.METHOD_POST,
		JSON.stringify(body),
	)
	if error != OK:
		_fail_and_schedule()


func _request_heartbeat(context: Dictionary) -> void:
	_phase = "heartbeat"
	_http.timeout = EDGE_TIMEOUT_SEC
	var body := {
		"version": 1,
		"sequence": _sequence,
		"platform": context["platform"],
	}
	_sequence += 1
	if context.has("appVersion"):
		body["appVersion"] = context["appVersion"]
	var headers := PackedStringArray([
		"Content-Type: application/json",
		"Authorization: Bearer " + _token,
	])
	var error := _http.request(
		_edge_url + "/v1/presence/heartbeat",
		headers,
		HTTPClient.METHOD_POST,
		JSON.stringify(body),
	)
	if error != OK:
		_fail_and_schedule()


func _on_request_completed(
	result: int,
	response_code: int,
	headers: PackedStringArray,
	body: PackedByteArray,
) -> void:
	if result != HTTPRequest.RESULT_SUCCESS:
		_fail_and_schedule()
		return
	var response := Envelope.parse_text(response_code, body.get_string_from_utf8())
	if not bool(response.get("valid", false)) or not bool(response.get("ok", false)):
		if _phase == "heartbeat" and response_code == 401:
			_token = ""
			_token_expires_at_ticks = 0
		var retry_after := _retry_after_ms(headers)
		_fail_and_schedule(retry_after)
		return

	if _phase == "token":
		var bootstrap: Dictionary = response.get("result", {})
		_interval_ms = clampi(
			int(bootstrap.get("heartbeatIntervalSeconds", 60)) * 1000,
			NORMAL_INTERVAL_MS,
			MAX_BACKOFF_MS,
		)
		if not bool(bootstrap.get("enabled", false)):
			_fail_and_schedule(MAX_BACKOFF_MS)
			return
		_token = String(bootstrap.get("token", ""))
		_edge_url = String(bootstrap.get("edgeUrl", "")).rstrip("/")
		var expires_in := int(bootstrap.get("expiresIn", 0))
		if _token.is_empty() or _edge_url.is_empty() or expires_in <= 0:
			_fail_and_schedule()
			return
		_token_expires_at_ticks = Time.get_ticks_msec() + expires_in * 1000
		if String(_active_context.get("platform", "")).is_empty():
			_fail_and_schedule()
			return
		_request_heartbeat(_active_context)
		return

	_failures = 0
	_in_flight = false
	_schedule(_next_delay_ms())


func _fail_and_schedule(explicit_delay_ms: int = -1) -> void:
	_failures += 1
	_in_flight = false
	if _running:
		_schedule(explicit_delay_ms if explicit_delay_ms > 0 else _next_delay_ms())


func _next_delay_ms() -> int:
	var base := _backoff_base_ms(_failures, _interval_ms)
	var jitter := float(base) * JITTER_RATIO * (_rng.randf() * 2.0 - 1.0)
	return clampi(int(round(float(base) + jitter)), NORMAL_INTERVAL_MS, MAX_BACKOFF_MS)


static func _backoff_base_ms(failures: int, interval_ms: int) -> int:
	if failures <= 1:
		return interval_ms
	if failures == 2:
		return mini(interval_ms * 2, MAX_BACKOFF_MS)
	return MAX_BACKOFF_MS


func _schedule(delay_ms: int) -> void:
	_timer.stop()
	_timer.wait_time = float(clampi(delay_ms, NORMAL_INTERVAL_MS, MAX_BACKOFF_MS)) / 1000.0
	_timer.start()


func _resolved_context() -> Dictionary:
	var raw: Variant = _context_source
	if typeof(raw) == TYPE_CALLABLE:
		var provider: Callable = raw
		if provider.is_valid():
			raw = provider.call()
	var source: Dictionary = raw if typeof(raw) == TYPE_DICTIONARY else {}
	var result := {}
	var platform := String(source.get("platform", "")).strip_edges().to_lower()
	if platform in ["android", "ios", "web", "ait"]:
		result["platform"] = platform
	var app_version := String(source.get("appVersion", "")).strip_edges()
	if app_version.length() > 32:
		app_version = app_version.substr(0, 32)
	if not app_version.is_empty():
		result["appVersion"] = app_version
	return result


func _retry_after_ms(headers: PackedStringArray) -> int:
	for header in headers:
		if header.to_lower().begins_with("retry-after:"):
			var seconds := String(header).get_slice(":", 1).strip_edges().to_int()
			if seconds > 0:
				return clampi(seconds * 1000, NORMAL_INTERVAL_MS, MAX_BACKOFF_MS)
	return -1


func _new_session_id() -> String:
	var bytes := PackedByteArray()
	bytes.resize(24)
	for index in 24:
		bytes[index] = randi() % 256
	return bytes.hex_encode()


func _ensure_nodes() -> void:
	if not is_instance_valid(_http):
		_http = HTTPRequest.new()
		_http.body_size_limit = MAX_RESPONSE_BYTES
		_http.request_completed.connect(_on_request_completed)
		add_child(_http)
	if not is_instance_valid(_timer):
		_timer = Timer.new()
		_timer.one_shot = true
		_timer.timeout.connect(_cycle)
		add_child(_timer)
