## 이벤트 파라미터 정규화.
##
## 계약은 spec/conformance/param-normalization.json이 정본이고
## TypeScript SDK와 [b]바이트 단위로 같은 출력[/b]을 내야 한다.
##
## 조직에서 boolean 직렬화가 1/0, "true"/"false", 미처리로 갈려
## 같은 이벤트가 앱마다 다르게 쌓이고 있었다. 그것을 여기서 끝낸다.
class_name SeoriParamNormalizer
extends RefCounted

## 파라미터 개수 상한. GA4 규격을 따른다.
const MAX_PARAMS := 25

## 파라미터 이름 길이 상한.
const MAX_KEY_LENGTH := 40

## 문자열 값 길이 상한.
const MAX_STRING_LENGTH := 100

## 개인정보로 판정해 버리는 키 이름.
##
## 플랫폼은 PII를 저장하지 않는다. 서버에서도 거르지만 여기서 먼저
## 버려서 네트워크에 실리지 않게 한다. 실수로 넣은 값이 로그나
## 프록시에 남는 것을 막는다.
const PII_KEYS := [
	"email",
	"e_mail",
	"mail",
	"phone",
	"phone_number",
	"tel",
	"mobile",
	"name",
	"full_name",
	"first_name",
	"last_name",
	"real_name",
	"address",
	"addr",
	"zipcode",
	"postal_code",
	"birth",
	"birthday",
	"birthdate",
	"ssn",
	"passport",
	"card_number",
	"credit_card",
	"ip",
	"ip_address",
]


## 파라미터를 정규화한다.
##
## 버리는 것과 변환하는 것을 구분한다. 값을 조용히 문자열로 바꾸지
## 않는 것이 중요하다 — Dictionary를 문자열화하면 PII가 통째로 실려 나간다.
static func normalize(params: Dictionary) -> Dictionary:
	var kept: Array = []

	for key in params.keys():
		if typeof(key) != TYPE_STRING:
			continue
		if not _is_allowed_key(key):
			continue

		var normalized: Variant = _normalize_value(params[key])
		if normalized == null:
			continue

		kept.append([key, normalized])

	# 상한은 버릴 것을 다 버린 뒤에 적용한다.
	# 먼저 자르면 버려질 값이 자리를 차지해 멀쩡한 값이 밀려난다.
	if kept.size() > MAX_PARAMS:
		# 키 오름차순으로 앞의 것을 남긴다. 삽입 순서를 쓰면
		# 언어마다 결과가 달라져 TypeScript와 어긋난다.
		kept.sort_custom(func(a, b): return a[0] < b[0])
		kept.resize(MAX_PARAMS)

	var out := {}
	for pair in kept:
		out[pair[0]] = pair[1]
	return out


static func _is_allowed_key(key: String) -> bool:
	if key.is_empty() or key.length() > MAX_KEY_LENGTH:
		return false
	return not PII_KEYS.has(key.to_lower())


## 값 하나를 정규화한다. null이면 파라미터를 버린다는 뜻이다.
static func _normalize_value(value: Variant) -> Variant:
	match typeof(value):
		TYPE_BOOL:
			# 1/0으로 통일한다. happy-farm의 toScalar 규약을 채택했다.
			return 1 if value else 0

		TYPE_INT:
			return value

		TYPE_FLOAT:
			# NaN과 Infinity는 JSON으로 직렬화할 수 없다.
			# 버리지 않고 0으로 두는 이유는 "값이 있었다"는 사실 자체가
			# 신호이기 때문이다.
			if is_nan(value) or is_inf(value):
				return 0
			# 정수로 떨어지는 실수는 정수로 보낸다.
			# TypeScript는 1.0과 1을 구분하지 않아서 이렇게 해야 일치한다.
			if value == floor(value) and abs(value) < 9007199254740992.0:
				return int(value)
			return value

		TYPE_STRING, TYPE_STRING_NAME:
			var text := String(value)
			if text.length() > MAX_STRING_LENGTH:
				return text.substr(0, MAX_STRING_LENGTH)
			return text

		_:
			# null, Dictionary, Array, Object 등이 여기 온다.
			# Dictionary를 문자열로 바꾸지 않는다. 조용한 직렬화는
			# 의도치 않은 정보를 통째로 실어 보낸다.
			return null
