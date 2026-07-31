## 서버 응답 envelope 해석.
##
## 계약은 spec/conformance/envelope.json이 정본이다.
##
## 가장 중요한 규칙은 [b]미지 필드를 무시한다[/b]는 것이다.
## lizard-tycoon의 기존 IAP 클라이언트(_exact_keys)는 응답 키 개수까지
## 일치를 요구해서 서버가 필드를 하나 추가하면 구버전이 깨졌다.
## 마켓에 배포된 앱은 2~3년 살아남으므로 그 방식으로는
## "/v1은 영구히 깨지지 않는다"가 성립하지 않는다.
class_name SeoriEnvelope
extends RefCounted

## 로컬에서 판정한 오류 코드. 서버가 준 것이 아니다.
const LOCAL_RESPONSE_INVALID := "iap_response_invalid"


## HTTP 상태와 본문으로 envelope을 해석한다.
##
## 반환 Dictionary:
##   valid      : bool  — 해석 가능한 응답인가
##   ok         : bool  — 성공인가 (valid일 때만 의미 있음)
##   result     : Dictionary — 성공 결과
##   code       : String — 오류 코드
##   message    : String — 오류 메시지
##   local      : bool  — 로컬 판정인가
##
## 상태 코드와 ok가 어긋나면 무효로 본다. 둘 중 하나가 거짓말을 하는
## 상황이라 어느 쪽을 믿을지 정할 수 없다. 지급 여부가 걸린 응답에서
## 추측으로 진행하면 안 된다.
static func parse(http_status: int, body: Variant) -> Dictionary:
	if typeof(body) != TYPE_DICTIONARY:
		return _invalid("응답 본문이 올바르지 않아요")

	var dict: Dictionary = body

	if not dict.has("ok") or typeof(dict["ok"]) != TYPE_BOOL:
		return _invalid("응답 형식이 올바르지 않아요")

	var http_ok := http_status >= 200 and http_status < 300
	var body_ok: bool = dict["ok"]

	if http_ok != body_ok:
		return _invalid("응답 상태가 일치하지 않아요")

	if body_ok:
		# result가 없어도 된다. 본문 없는 성공 응답이 있다.
		var result: Variant = dict.get("result", {})
		if typeof(result) != TYPE_DICTIONARY:
			result = {}
		return {
			"valid": true,
			"ok": true,
			"result": result,
			"code": "",
			"message": "",
			"local": false,
		}

	var error: Variant = dict.get("error", null)
	if typeof(error) != TYPE_DICTIONARY:
		return _invalid("오류 응답 형식이 올바르지 않아요")

	var error_dict: Dictionary = error
	var code: Variant = error_dict.get("code", "")
	if typeof(code) != TYPE_STRING or String(code).is_empty():
		return _invalid("오류 응답 형식이 올바르지 않아요")

	var message: Variant = error_dict.get("message", "")
	if typeof(message) != TYPE_STRING:
		message = ""

	return {
		"valid": true,
		"ok": false,
		"result": {},
		"code": String(code),
		"message": String(message),
		"local": false,
	}


## 응답 본문 문자열을 파싱해서 해석한다.
static func parse_text(http_status: int, text: String) -> Dictionary:
	if text.strip_edges().is_empty():
		return _invalid("응답 본문이 비었어요")

	var json := JSON.new()
	if json.parse(text) != OK:
		return _invalid("응답을 해석하지 못했어요")

	return parse(http_status, json.data)


static func _invalid(message: String) -> Dictionary:
	return {
		"valid": false,
		"ok": false,
		"result": {},
		"code": LOCAL_RESPONSE_INVALID,
		"message": message,
		"local": true,
	}
