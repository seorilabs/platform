## HTTP 전송과 재시도.
##
## Godot의 HTTPRequest는 한 번에 요청 하나만 처리한다.
## 그래서 큐를 두고 직렬로 흘린다. 동시 요청이 필요하면
## 노드를 여러 개 두는 쪽이 맞지만, 플랫폼 호출은 빈도가 낮아
## 하나로 충분하고 그 편이 소켓도 아낀다.
##
## 재시도 정책은 backoff.gd가 정하고 여기서는 실행만 한다.
class_name SeoriHttpTransport
extends Node

const Backoff := preload("backoff.gd")
const Envelope := preload("envelope.gd")

## 응답 크기 상한. 마켓 응답이 이상하게 커도 메모리를 다 쓰지 않게 한다.
const MAX_RESPONSE_BYTES := 256 * 1024

## 대기 큐 상한. 넘으면 오래된 것부터 버린다.
const MAX_PENDING := 16

## 요청 하나의 제한 시간(초).
##
## Apple 검증은 외부 API 왕복이 붙어 오래 걸린다.
const DEFAULT_TIMEOUT_SEC := 15.0

var base_url := ""
var app_id := ""
var max_retries := 3

var _http: HTTPRequest
var _busy := false
var _pending: Array = []
var _current: Dictionary = {}
var _rng := RandomNumberGenerator.new()


func _ready() -> void:
	_ensure_http()


func configure(p_base_url: String, p_app_id: String, p_max_retries: int = 3) -> void:
	base_url = p_base_url.rstrip("/")
	app_id = p_app_id
	max_retries = p_max_retries


## 요청을 보낸다.
##
## callback은 Dictionary 하나를 받는다. envelope.gd의 반환 형식과 같고
## http_status가 추가된다. 던지지 않는다 — GDScript에는 예외가 없고
## 게임 루프에서 실패를 조용히 다루는 편이 안전하다.
##
## request_data 키:
##   method   : "GET" | "POST" | "DELETE"
##   path     : "/v1/..."
##   body     : Dictionary (선택)
##   token    : String (선택)
##   query    : Dictionary (선택)
##   no_retry : bool (선택) — 결제처럼 중복이 위험한 요청
##   base_url : String (선택) — 이 요청만 다른 호스트로 보낸다
func request(request_data: Dictionary, callback: Callable) -> void:
	if base_url.is_empty() or app_id.is_empty():
		callback.call(_local_error(0, "transport_not_configured", "전송 설정이 없어요"))
		return

	if _pending.size() >= MAX_PENDING:
		# 큐가 밀렸다. 가장 오래된 것을 버리고 알린다.
		var dropped: Dictionary = _pending.pop_front()
		var cb: Callable = dropped["callback"]
		if cb.is_valid():
			cb.call(_local_error(0, "request_dropped", "요청이 밀려 취소됐어요"))

	_pending.append({
		"data": request_data,
		"callback": callback,
		"attempt": 0,
	})
	_drain()


func _drain() -> void:
	if _busy or _pending.is_empty():
		return

	_current = _pending.pop_front()
	_busy = true
	_send(_current)


func _send(entry: Dictionary) -> void:
	_ensure_http()

	var data: Dictionary = entry["data"]
	var url := _build_url(data)
	var headers := _build_headers(data)

	var method := HTTPClient.METHOD_GET
	match String(data.get("method", "GET")):
		"POST":
			method = HTTPClient.METHOD_POST
		"DELETE":
			method = HTTPClient.METHOD_DELETE
		_:
			method = HTTPClient.METHOD_GET

	# 본문 없는 POST도 빈 JSON 객체를 보낸다.
	#
	# Godot의 HTTPRequest는 본문이 빈 문자열이면 Content-Length를 붙이지
	# 않는다. Google 프론트엔드는 그 POST를 411 Length Required로 거부하고
	# HTML을 돌려준다. 컨테이너까지 오지 않으니 서버 로그에는 아무것도
	# 남지 않고, 클라이언트는 "응답을 해석하지 못했어요"만 본다.
	#
	# 실기기에서 이것 때문에 결제 첫 단계인 계정 참조 발급이 통째로
	# 막혔다. 원인을 찾는 데 가장 오래 걸린 자리다.
	var body := ""
	if data.has("body"):
		body = JSON.stringify(data["body"])
	elif String(data.get("method", "GET")) == "POST":
		body = "{}"

	var err := _http.request(url, headers, method, body)
	if err != OK:
		_finish(_local_error(0, "network_error", "요청을 보내지 못했어요"))


func _build_url(data: Dictionary) -> String:
	# 역할마다 Cloud Run 서비스가 다르다. 세션은 api, 결제는 iap,
	# 이벤트는 ingest다. 마켓 자격증명을 iap 서비스에만 두려면
	# 한 호스트로 합칠 수 없다.
	var host := String(data.get("base_url", "")).rstrip("/")
	if host.is_empty():
		host = base_url
	var url := host + String(data.get("path", ""))

	var query: Dictionary = data.get("query", {})
	if query.is_empty():
		return url

	var parts: Array[String] = []
	# 키를 정렬해 요청 URL이 결정적이 되게 한다. 캐시와 로그가 읽기 쉬워진다.
	var keys := query.keys()
	keys.sort()
	for key in keys:
		var value: Variant = query[key]
		if value == null:
			continue
		parts.append("%s=%s" % [String(key).uri_encode(), String(value).uri_encode()])

	if parts.is_empty():
		return url
	return url + "?" + "&".join(parts)


func _build_headers(data: Dictionary) -> PackedStringArray:
	var headers := PackedStringArray([
		"Content-Type: application/json",
		# 서버가 앱을 식별하는 헤더다. 레지스트리 조회의 키가 된다.
		"X-Seori-App: " + app_id,
	])

	var token := String(data.get("token", ""))
	if not token.is_empty():
		headers.append("Authorization: Bearer " + token)

	return headers


func _ensure_http() -> void:
	if is_instance_valid(_http):
		return

	_http = HTTPRequest.new()
	_http.timeout = DEFAULT_TIMEOUT_SEC
	_http.body_size_limit = MAX_RESPONSE_BYTES
	_http.request_completed.connect(_on_request_completed)
	add_child(_http)


func _on_request_completed(
	result: int,
	response_code: int,
	headers: PackedStringArray,
	body: PackedByteArray,
) -> void:
	var status := response_code
	var parsed: Dictionary

	if result != HTTPRequest.RESULT_SUCCESS:
		# 네트워크 오류나 타임아웃이다. status 0으로 재시도 대상이 된다.
		status = 0
		parsed = _local_error(0, "network_error", "연결에 실패했어요")
	else:
		parsed = Envelope.parse_text(status, body.get_string_from_utf8())
		parsed["http_status"] = status

	if parsed.get("valid", false) and parsed.get("ok", false):
		_finish(parsed)
		return

	# 실패다. 재시도할지 판단한다.
	var data: Dictionary = _current.get("data", {})
	var attempt := int(_current.get("attempt", 0)) + 1
	var no_retry: bool = data.get("no_retry", false)

	var can_retry := (
		not no_retry
		and attempt <= max_retries
		and Backoff.is_retryable_status(status)
	)

	if not can_retry:
		_finish(parsed)
		return

	_current["attempt"] = attempt
	var delay_ms := _resolve_delay(attempt, headers)
	_retry_after_delay(delay_ms)


func _resolve_delay(attempt: int, headers: PackedStringArray) -> int:
	# 서버가 언제 다시 오라고 했으면 그 말을 따른다.
	# 우리 백오프로 덮어쓰면 rate limit을 계속 두드린다.
	for header in headers:
		var lower := header.to_lower()
		if lower.begins_with("retry-after:"):
			var value := header.substr(header.find(":") + 1)
			var explicit := Backoff.parse_retry_after_ms(value)
			if explicit >= 0:
				return explicit

	return Backoff.delay_with_jitter_ms(attempt, _rng)


func _retry_after_delay(delay_ms: int) -> void:
	if delay_ms <= 0:
		_send(_current)
		return

	var timer := get_tree().create_timer(float(delay_ms) / 1000.0)
	timer.timeout.connect(func() -> void:
		# 대기 중에 노드가 사라졌을 수 있다.
		if is_instance_valid(self) and _busy:
			_send(_current)
	)


func _finish(response: Dictionary) -> void:
	var callback: Callable = _current.get("callback", Callable())
	_current = {}
	_busy = false

	if callback.is_valid():
		callback.call(response)

	# 큐에 남은 것을 이어서 처리한다.
	_drain()


func _local_error(status: int, code: String, message: String) -> Dictionary:
	return {
		"valid": false,
		"ok": false,
		"result": {},
		"code": code,
		"message": message,
		"local": true,
		"http_status": status,
	}
