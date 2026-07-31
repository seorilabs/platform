## conformance 벡터를 GDScript 구현으로 검증한다.
##
## TypeScript SDK와 [b]같은 벡터 파일[/b]을 읽는다. 둘이 다른 값을 내면
## 같은 이벤트가 앱마다 다르게 쌓이고, 그건 조직이 이미 겪은 문제다.
##
##   godot --headless --script addons/seorilabs_platform/tools/conformance_probe.gd
##
## 벡터 경로는 --conformance-dir로 넘긴다. 없으면 저장소 기본 위치를 쓴다.
extends SceneTree

const Normalizer := preload("res://addons/seorilabs_platform/core/param_normalizer.gd")
const Backoff := preload("res://addons/seorilabs_platform/core/backoff.gd")
const Envelope := preload("res://addons/seorilabs_platform/core/envelope.gd")

## 최소 검사 수.
##
## 벡터가 늘면 이 값도 올린다. 줄어들면 무언가 조용히 빠진 것이다.
const MIN_EXPECTED_CHECKS := 65

var _failures: Array[String] = []
var _checks := 0


func _initialize() -> void:
	var dir := _conformance_dir()
	print("[conformance] 벡터 경로: %s" % dir)

	_check_normalization(dir)
	_check_backoff(dir)
	_check_envelope(dir)

	print("[conformance] 검사 %d건" % _checks)

	# 스크립트 오류로 검사가 중간에 끊겨도 "전부 통과"로 보고되면
	# CI가 초록불을 준다. 최소 검사 수로 그것을 막는다.
	if _checks < MIN_EXPECTED_CHECKS:
		_fail("검사가 %d건뿐이다. 최소 %d건이어야 한다 (중간에 끊겼는지 확인)"
			% [_checks, MIN_EXPECTED_CHECKS])

	if _failures.is_empty():
		print("[conformance] 전부 통과")
		quit(0)
		return

	for failure in _failures:
		printerr("[conformance] 실패: %s" % failure)
	printerr("[conformance] %d건 실패" % _failures.size())
	quit(1)


func _conformance_dir() -> String:
	var args := OS.get_cmdline_user_args()
	for i in args.size():
		if args[i] == "--conformance-dir" and i + 1 < args.size():
			return args[i + 1]
	# 저장소 배치 기준 상대 경로다.
	# sdk-gdscript/ 아래에서 두 단계 올라가면 저장소 루트다.
	return ProjectSettings.globalize_path("res://") + "../../spec/conformance"


func _load_vector(dir: String, name: String) -> Variant:
	var path := dir.path_join(name)
	var text := FileAccess.get_file_as_string(path)
	if text.is_empty():
		_fail("벡터를 읽지 못했다: %s" % path)
		return null

	var json := JSON.new()
	if json.parse(text) != OK:
		_fail("벡터 JSON 오류: %s" % path)
		return null
	return json.data


## 벡터의 sentinel을 실제 값으로 바꾼다.
##
## NaN과 Infinity는 JSON으로 표현할 수 없어 문자열로 들어 있다.
func _resolve_sentinel(value: Variant) -> Variant:
	if typeof(value) != TYPE_STRING:
		return value
	match String(value):
		"__nan__":
			return NAN
		"__pos_inf__":
			return INF
		"__neg_inf__":
			return -INF
		"__null__":
			return null
		_:
			return value


func _resolve_input(input: Dictionary) -> Dictionary:
	var out := {}
	for key in input.keys():
		out[key] = _resolve_sentinel(input[key])
	return out


func _check_normalization(dir: String) -> void:
	var vector: Variant = _load_vector(dir, "param-normalization.json")
	if vector == null:
		return

	var cases: Array = vector.get("cases", [])
	if cases.is_empty():
		_fail("정규화 케이스가 비었다")
		return

	for case in cases:
		var got := Normalizer.normalize(_resolve_input(case["in"]))
		var want: Dictionary = case["out"]

		if not _dict_equals(got, want):
			_fail("정규화 '%s': got=%s want=%s" % [case["name"], got, want])
		_checks += 1

	# PII 키 목록이 벡터와 같아야 한다.
	# 한쪽에만 추가하면 서버와 SDK 판정이 어긋난다.
	var vector_pii: Array = vector.get("pii_keys", [])
	var sdk_pii := Normalizer.PII_KEYS.duplicate()
	sdk_pii.sort()
	var want_pii := vector_pii.duplicate()
	want_pii.sort()

	if sdk_pii != want_pii:
		_fail("PII 키 목록이 벡터와 다르다")
	_checks += 1


func _check_backoff(dir: String) -> void:
	var vector: Variant = _load_vector(dir, "backoff.json")
	if vector == null:
		return

	var schedule: Array = vector.get("schedule", [])
	if schedule.is_empty():
		_fail("백오프 스케줄이 비었다")
		return

	for entry in schedule:
		var attempt := int(entry["attempt"])
		var want := int(entry["delay_ms"])
		var got := Backoff.delay_ms(attempt)

		if got != want:
			_fail("백오프 attempt=%d: got=%d want=%d" % [attempt, got, want])
		_checks += 1

	for entry in vector.get("retryable_cases", []):
		var status := int(entry["status"])
		var want_retry: bool = entry["retry"]
		var got_retry := Backoff.is_retryable_status(status)

		if got_retry != want_retry:
			_fail("재시도 판정 status=%d: got=%s want=%s" % [status, got_retry, want_retry])
		_checks += 1

	# attempt가 0이나 음수여도 첫 간격을 준다
	if Backoff.delay_ms(0) != Backoff.BASE_MS:
		_fail("백오프 attempt=0이 첫 간격이 아니다")
	if Backoff.delay_ms(-5) != Backoff.BASE_MS:
		_fail("백오프 attempt=-5가 첫 간격이 아니다")
	_checks += 2

	# Retry-After를 우선한다
	if Backoff.parse_retry_after_ms("30") != 30000:
		_fail("Retry-After 30초를 읽지 못했다")
	if Backoff.parse_retry_after_ms("") != -1:
		_fail("빈 Retry-After를 값으로 읽었다")
	if Backoff.parse_retry_after_ms("나중에") != -1:
		_fail("해석 불가한 Retry-After를 값으로 읽었다")
	_checks += 3


func _check_envelope(dir: String) -> void:
	var vector: Variant = _load_vector(dir, "envelope.json")
	if vector == null:
		return

	var cases: Array = vector.get("cases", [])
	if cases.is_empty():
		_fail("envelope 케이스가 비었다")
		return

	for case in cases:
		# raw_body는 서버가 JSON이 아닌 것을 보낸 상황이다.
		# 전송 계층이 하는 것과 같은 파싱을 재현한다.
		var got: Dictionary
		if case.has("raw_body"):
			got = Envelope.parse_text(int(case["http_status"]), String(case["raw_body"]))
		elif case.has("body"):
			got = Envelope.parse(int(case["http_status"]), case["body"])
		else:
			_fail("envelope '%s': 본문이 없다" % case["name"])
			_checks += 1
			continue

		var want: Dictionary = case["expect"]

		if got["valid"] != want["valid"]:
			_fail("envelope '%s': valid got=%s want=%s" % [case["name"], got["valid"], want["valid"]])
			_checks += 1
			continue

		if not got["valid"]:
			var want_code: String = want.get("local_code", Envelope.LOCAL_RESPONSE_INVALID)
			if got["code"] != want_code:
				_fail("envelope '%s': code got=%s want=%s" % [case["name"], got["code"], want_code])
		else:
			if want.has("ok") and got["ok"] != want["ok"]:
				_fail("envelope '%s': ok got=%s want=%s" % [case["name"], got["ok"], want["ok"]])
			if want.has("code") and got["code"] != want["code"]:
				_fail("envelope '%s': code got=%s want=%s" % [case["name"], got["code"], want["code"]])

		_checks += 1

	# 미지 필드가 있어도 깨지지 않는다. R4의 전제다.
	var future := Envelope.parse(200, {
		"ok": true,
		"result": {"entitlements": [], "미래필드": 1},
		"또다른미래필드": "x",
	})
	if not future["valid"] or not future["ok"]:
		_fail("미지 필드가 있는 응답을 거부했다")
	_checks += 1


## Dictionary 비교. 숫자 타입이 달라도 값이 같으면 같다고 본다.
##
## JSON은 정수를 float으로 읽을 수 있어서 1과 1.0이 섞인다.
func _dict_equals(got: Dictionary, want: Dictionary) -> bool:
	if got.size() != want.size():
		return false

	for key in want.keys():
		if not got.has(key):
			return false

		var a: Variant = got[key]
		var b: Variant = want[key]

		if typeof(a) == TYPE_STRING or typeof(b) == TYPE_STRING:
			if String(a) != String(b):
				return false
			continue

		if not is_equal_approx(float(a), float(b)):
			return false

	return true


func _fail(message: String) -> void:
	_failures.append(message)
