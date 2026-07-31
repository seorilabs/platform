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

var _failures: Array[String] = []


func _initialize() -> void:
	_check_loads()
	_check_client_defaults()
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
