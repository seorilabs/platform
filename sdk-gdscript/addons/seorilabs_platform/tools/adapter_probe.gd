## Firebase identity와 rewarded claim 표준 adapter의 상태 전이를 검증한다.
extends SceneTree

const FirebaseIdentityAdapter := preload("res://addons/seorilabs_platform/adapters/firebase_identity_adapter.gd")
const RewardedClaimAdapter := preload("res://addons/seorilabs_platform/adapters/rewarded_claim_adapter.gd")

const CLAIM_PATH := "user://sdk_adapter_probe_claims.json"
const ACK_PATH := "user://sdk_adapter_probe_acks.json"

var _failures: Array[String] = []


class PlatformSpy:
	extends Node

	var signed_in := false
	var custom_token_existing := ""
	var claim_request: Dictionary = {}
	var policy_response := {"ok": true, "result": {"appUsesAds": true, "adsEnabled": true}}

	func create_firebase_custom_token(existing: String, _app_check: String, callback: Callable) -> void:
		custom_token_existing = existing
		callback.call({"ok": true, "result": {"firebaseCustomToken": "one-time-token"}})

	func sign_in(_credential: Dictionary, callback: Callable) -> void:
		signed_in = true
		callback.call({"ok": true, "result": {}})

	func is_signed_in() -> bool:
		return signed_in

	func get_ads_policy(callback: Callable) -> void:
		callback.call(policy_response.duplicate(true))

	func create_reward_claim(request: Dictionary, callback: Callable) -> void:
		claim_request = request.duplicate(true)
		callback.call({
			"ok": true,
			"result": {
				"claimId": "cl_platform",
				"admobSsv": {"customData": "cl_platform", "userId": "pu_1"},
			},
		})

	func get_reward_claim(_claim_id: String, callback: Callable) -> void:
		callback.call({"ok": true, "result": {"state": "confirmed", "assurance": "server_verified"}})

	func ack_reward_claim(_claim_id: String, callback: Callable) -> void:
		callback.call({"ok": true, "result": {"state": "delivered"}})


class FirebaseAdapterSpy:
	extends FirebaseIdentityAdapter

	var responses: Array[Dictionary] = []
	var requests: Array[Dictionary] = []
	var persist_success := true

	func _request_json(
		url: String,
		method: int,
		headers: PackedStringArray,
		body: String,
		add_json_header: bool = true,
	) -> Dictionary:
		requests.append({"url": url, "method": method, "headers": headers, "body": body, "json": add_json_header})
		return responses.pop_front() if not responses.is_empty() else {"success": false, "status": 0}

	func _save_state() -> bool:
		_state_dirty = not persist_success
		return persist_success


class IdentitySpy:
	extends Node

	func ensure_identity() -> Dictionary:
		return {"success": true, "uid": "pb_1", "id_token": "firebase-id-token"}


func _initialize() -> void:
	await _check_firebase_identity()
	await _check_rewarded_claim_flow()
	await _check_policy_fail_closed()
	await _check_invalid_adapter_contract()
	_cleanup()

	if _failures.is_empty():
		print("[adapter] 전부 통과")
		quit(0)
		return
	for failure in _failures:
		printerr("[adapter] 실패: %s" % failure)
	quit(1)


func _check_firebase_identity() -> void:
	var platform := PlatformSpy.new()
	root.add_child(platform)
	var adapter := FirebaseAdapterSpy.new()
	root.add_child(adapter)
	adapter.configure({"firebase_api_key": "api-key", "platform_client": platform})
	adapter._loaded = true
	adapter._state = {}
	adapter.responses.append({
		"success": true,
		"status": 200,
		"data": {
			"idToken": _id_token("pb_1"),
			"refreshToken": "firebase-refresh-token",
			"expiresIn": "3600",
		},
	})
	var result: Dictionary = await adapter.ensure_identity()
	_expect(bool(result.get("success", false)), "Custom Token Firebase 로그인이 실패했다")
	_expect(String(result.get("uid", "")) == "pb_1", "ID token의 UID를 복원하지 못했다")
	_expect(platform.custom_token_existing.is_empty(), "신규 신원에 기존 token을 보냈다")
	_expect(adapter.requests.size() == 1 and "signInWithCustomToken" in String(adapter.requests[0].url), "Firebase Custom Token endpoint를 사용하지 않았다")
	_expect("one-time-token" not in JSON.stringify(adapter._state), "일회용 Custom Token을 저장했다")
	var failed_persist := FirebaseAdapterSpy.new()
	root.add_child(failed_persist)
	failed_persist.configure({"firebase_api_key": "api-key", "platform_client": platform})
	failed_persist._loaded = true
	failed_persist._state = {}
	failed_persist.persist_success = false
	failed_persist.responses.append(adapter.responses[0] if not adapter.responses.is_empty() else {
		"success": true,
		"status": 200,
		"data": {"idToken": _id_token("pb_2"), "refreshToken": "refresh", "expiresIn": "3600"},
	})
	var persist_result: Dictionary = await failed_persist.ensure_identity()
	_expect(String(persist_result.get("reason", "")) == "firebase_identity_persist_failed", "신원 저장 실패가 성공으로 처리됐다")
	failed_persist.free()
	adapter.free()
	platform.free()


func _check_rewarded_claim_flow() -> void:
	var platform := PlatformSpy.new()
	root.add_child(platform)
	var identity := IdentitySpy.new()
	root.add_child(identity)
	var adapter := RewardedClaimAdapter.new()
	root.add_child(adapter)
	adapter.configure({
		"platform_client": platform,
		"identity_adapter": identity,
		"client_platform": "android",
		"claim_map_path": CLAIM_PATH,
		"ack_queue_path": ACK_PATH,
	})

	var policy: Dictionary = await adapter.policy()
	_expect(bool(policy.get("allowed", false)), "허용 정책이 거부됐다")
	var request := {"request_id": "local-1", "placement": "hint", "reward_key": "hint", "reward_amount": 3}
	var created: Dictionary = await adapter.create_admob_claim(request)
	_expect(bool(created.get("success", false)), "Platform claim 생성이 실패했다")
	_expect(platform.claim_request == {
		"requestId": "local-1", "placement": "hint", "provider": "admob",
		"clientPlatform": "android", "reward": {"key": "hint", "amount": 3},
	}, "claim 요청 계약이 다르다")
	_expect(adapter.ssv_options("local-1") == {"custom_data": "cl_platform", "user_id": "pu_1"}, "SSV options가 다르다")
	var recovered: Dictionary = await adapter.recover_admob_claim(request)
	_expect(String(recovered.get("status", "")) == "verified", "server_verified claim을 복원하지 못했다")
	_expect(await adapter.acknowledge("local-1"), "ack가 실패했다")
	_expect(adapter.ssv_options("local-1").is_empty(), "ack 뒤 claim 참조가 남았다")
	var failed_request := {
		"request_id": "local-failed", "placement": "hint",
		"reward_key": "hint", "reward_amount": 3,
	}
	_expect(
		bool((await adapter.create_admob_claim(failed_request)).get("success", false)),
		"폐기 검사용 claim 생성이 실패했다",
	)
	_expect(adapter.discard_unsettled_claim("local-failed"), "미정산 claim을 폐기하지 못했다")
	_expect(adapter.ssv_options("local-failed").is_empty(), "폐기 뒤 claim 참조가 남았다")
	var ack_pending_request := {
		"request_id": "local-ack-pending", "placement": "hint",
		"reward_key": "hint", "reward_amount": 3,
	}
	_expect(
		bool((await adapter.create_admob_claim(ack_pending_request)).get("success", false)),
		"ack 대기열 검사용 claim 생성이 실패했다",
	)
	_expect(adapter._enqueue_ack("local-ack-pending"), "ack 대기열 준비가 실패했다")
	_expect(
		not adapter.discard_unsettled_claim("local-ack-pending"),
		"ack 대기열의 정산 완료 claim이 폐기됐다",
	)
	_expect(
		not adapter.ssv_options("local-ack-pending").is_empty(),
		"폐기 거부 뒤 claim 참조가 사라졌다",
	)
	_expect(await adapter.acknowledge("local-ack-pending"), "ack 대기열 검사용 claim 정리가 실패했다")
	adapter.free()
	identity.free()
	platform.free()


func _check_policy_fail_closed() -> void:
	var platform := PlatformSpy.new()
	platform.signed_in = true
	platform.policy_response = {"ok": false, "code": "platform_unavailable"}
	root.add_child(platform)
	var identity := IdentitySpy.new()
	root.add_child(identity)
	var adapter := RewardedClaimAdapter.new()
	root.add_child(adapter)
	adapter.configure({"platform_client": platform, "identity_adapter": identity, "client_platform": "ios"})
	var policy: Dictionary = await adapter.policy()
	_expect(not bool(policy.get("allowed", true)) and not bool(policy.get("success", true)), "정책 실패가 광고 허용으로 바뀌었다")
	adapter.free()
	identity.free()
	platform.free()


func _check_invalid_adapter_contract() -> void:
	var incomplete_platform := Node.new()
	root.add_child(incomplete_platform)
	var identity := IdentitySpy.new()
	root.add_child(identity)
	var adapter := RewardedClaimAdapter.new()
	root.add_child(adapter)
	adapter.configure({
		"platform_client": incomplete_platform,
		"identity_adapter": identity,
		"client_platform": "android",
	})
	var result: Dictionary = await adapter.ensure_session()
	_expect(
		not bool(result.get("success", true))
			and String(result.get("reason", "")) == "platform_adapter_unavailable",
		"불완전한 Platform client 계약이 fail-closed되지 않았다",
	)
	adapter.free()
	identity.free()
	incomplete_platform.free()

	var platform := PlatformSpy.new()
	root.add_child(platform)
	var incomplete_identity := Node.new()
	root.add_child(incomplete_identity)
	var identity_adapter := RewardedClaimAdapter.new()
	root.add_child(identity_adapter)
	identity_adapter.configure({
		"platform_client": platform,
		"identity_adapter": incomplete_identity,
		"client_platform": "android",
	})
	var identity_result: Dictionary = await identity_adapter.ensure_session()
	_expect(
		not bool(identity_result.get("success", true))
			and String(identity_result.get("reason", "")) == "platform_adapter_unavailable",
		"불완전한 identity adapter 계약이 fail-closed되지 않았다",
	)
	identity_adapter.free()
	incomplete_identity.free()
	platform.free()


func _id_token(uid: String) -> String:
	var payload := Marshalls.utf8_to_base64(JSON.stringify({"user_id": uid, "sub": uid}))
	payload = payload.replace("+", "-").replace("/", "_").trim_suffix("=").trim_suffix("=")
	return "header.%s.signature" % payload


func _cleanup() -> void:
	for path in [CLAIM_PATH, ACK_PATH]:
		if FileAccess.file_exists(path):
			DirAccess.remove_absolute(ProjectSettings.globalize_path(path))


func _expect(condition: bool, message: String) -> void:
	if not condition:
		_failures.append(message)
