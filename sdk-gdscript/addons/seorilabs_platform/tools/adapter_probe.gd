## Firebase identity와 rewarded claim 표준 adapter의 상태 전이를 검증한다.
extends SceneTree

const FirebaseIdentityAdapter := preload("res://addons/seorilabs_platform/adapters/firebase_identity_adapter.gd")
const RewardedClaimAdapter := preload("res://addons/seorilabs_platform/adapters/rewarded_claim_adapter.gd")
const AtomicJsonStore := preload("res://addons/seorilabs_platform/core/atomic_json_store.gd")

const IDENTITY_PATH := "user://sdk_adapter_probe_identity.json"
const IDENTITY_FAIL_PATH := "user://sdk_adapter_probe_identity_fail.json"
const CLAIM_PATH := "user://sdk_adapter_probe_claims.json"
const ACK_PATH := "user://sdk_adapter_probe_acks.json"

var _failures: Array[String] = []


class PlatformSpy:
	extends Node

	var signed_in := false
	var account_uid := "linked-uid"
	var auth_generation := 0
	var sign_out_count := 0
	var custom_token_existing := ""
	var claim_request: Dictionary = {}
	var policy_response := {"ok": true, "result": {"appUsesAds": true, "adsEnabled": true}}

	func create_firebase_custom_token(existing: String, _app_check: String, callback: Callable) -> void:
		custom_token_existing = existing
		callback.call({"ok": true, "result": {"firebaseCustomToken": "one-time-token"}})

	func sign_in(_credential: Dictionary, callback: Callable) -> void:
		signed_in = true
		auth_generation += 1
		callback.call({"ok": true, "result": {}})

	func is_signed_in() -> bool:
		return signed_in

	func sign_out() -> void:
		signed_in = false
		auth_generation += 1
		sign_out_count += 1

	func authentication_generation() -> int:
		return auth_generation

	func current_session() -> Dictionary:
		return {"appUserId": account_uid, "isLinkedAccount": true} if signed_in else {}

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
	var persist_to_disk := false
	var defer_response := false
	signal response_ready(response: Dictionary)

	func _request_json(
		url: String,
		method: int,
		headers: PackedStringArray,
		body: String,
		add_json_header: bool = true,
	) -> Dictionary:
		requests.append({"url": url, "method": method, "headers": headers, "body": body, "json": add_json_header})
		if defer_response:
			return await response_ready
		return responses.pop_front() if not responses.is_empty() else {"success": false, "status": 0}

	func _save_state() -> bool:
		if persist_to_disk:
			return super._save_state()
		_state_dirty = not persist_success
		return persist_success


class IdentitySpy:
	extends Node

	func ensure_identity() -> Dictionary:
		return {"success": true, "uid": "pb_1", "id_token": "firebase-id-token"}


func _initialize() -> void:
	await _check_firebase_identity()
	await _check_account_link_adoption()
	await _check_account_link_disk_failure()
	await _check_account_link_auth_change()
	await _check_identity_read_failure()
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
	_expect(not adapter._state.has("id_token"), "Firebase ID token을 로컬 상태에 저장했다")
	_expect(not adapter._current_id_token.is_empty(), "Firebase ID token을 메모리에 유지하지 못했다")
	_expect(AtomicJsonStore.write(IDENTITY_PATH, {
		"uid": "pb_1", "id_token": "persisted-id-token",
		"refresh_token": "refresh", "expires_at": 0,
	}), "기존 identity 저장본을 준비하지 못했다")
	var migrated := FirebaseIdentityAdapter.new()
	root.add_child(migrated)
	migrated.configure({
		"firebase_api_key": "api-key",
		"platform_client": platform,
		"state_path": IDENTITY_PATH,
	})
	migrated._load_state_once()
	_expect(not migrated._state.has("id_token"), "기존 저장본의 Firebase ID token을 제거하지 못했다")
	_expect(
		not (AtomicJsonStore.read_dictionary(IDENTITY_PATH).get("value", {}) as Dictionary).has("id_token"),
		"기존 저장 파일의 Firebase ID token을 제거하지 못했다",
	)
	migrated.free()
	_expect(AtomicJsonStore.write(IDENTITY_FAIL_PATH, {
		"uid": "pb_1", "id_token": "persisted-id-token",
		"refresh_token": "refresh", "expires_at": 0,
	}), "실패 경로 identity 저장본을 준비하지 못했다")
	var failed_migration := FirebaseAdapterSpy.new()
	failed_migration.persist_success = false
	root.add_child(failed_migration)
	failed_migration.configure({
		"firebase_api_key": "api-key",
		"platform_client": platform,
		"state_path": IDENTITY_FAIL_PATH,
	})
	failed_migration._load_state_once()
	_expect(failed_migration._state.is_empty(), "ID token 제거 저장 실패 뒤 신원 상태가 메모리에 남았다")
	_expect(failed_migration._current_id_token.is_empty(), "ID token 제거 저장 실패 뒤 token이 메모리에 남았다")
	_expect(failed_migration.current_identity().get("reason") == "firebase_identity_state_invalid", "ID token 제거 저장 실패와 미가입을 구분하지 못했다")
	failed_migration.free()
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


func _check_account_link_adoption() -> void:
	var platform := PlatformSpy.new()
	root.add_child(platform)
	var adapter := FirebaseAdapterSpy.new()
	root.add_child(adapter)
	adapter.configure({"firebase_api_key": "api-key", "platform_client": platform})
	adapter._loaded = true
	var guest := {"uid": "guest-uid", "refresh_token": "guest-refresh", "auth_provider": "platform_custom_token_v1", "expires_at": 0}
	adapter._state = guest.duplicate(true)
	adapter._current_id_token = "guest-id-token"
	var link := {"session": {"appUserId": "linked-uid", "isLinkedAccount": true, "isAnonymous": false},
		"firebaseCustomToken": "one-time-linked-token", "restored": true}
	adapter._identity_busy = true
	_expect((await adapter.adopt_account_link(link)).get("reason") == "firebase_identity_busy", "진행 중인 갱신과 복원을 동시에 시작했다")
	_expect(not adapter.clear_local_state(), "신원 갱신 중 로컬 신원을 삭제했다")
	adapter._identity_busy = false
	var invalid: Dictionary = link.duplicate(true)
	invalid["restored"] = false
	_expect((await adapter.adopt_account_link(invalid)).get("reason") == "platform_uid_mismatch", "새 연결이라고 주장하며 UID를 바꿨다")
	_expect(adapter.requests.is_empty(), "거부한 계정 전환이 Firebase에 도달했다")
	adapter._api_key = ""
	platform.signed_in = true
	_expect((await adapter.adopt_account_link(link)).get("reason") == "firebase_api_key_missing", "설정 없는 복원을 Firebase에 보냈다")
	_expect(adapter.requests.is_empty() and adapter._state == guest, "설정 없는 복원이 기존 신원을 바꿨다")
	adapter._api_key = "api-key"
	platform.signed_in = true
	adapter.responses.append({"success": true, "data": {"localId": "wrong-uid", "idToken": "wrong-id", "refreshToken": "wrong-refresh"}})
	_expect((await adapter.adopt_account_link(link)).get("reason") == "platform_uid_mismatch", "Firebase가 다른 UID를 반환해도 복원했다")
	_expect(adapter._state == guest and adapter._current_id_token == "guest-id-token", "실패한 복원이 기존 신원을 바꿨다")
	var response := {"success": true, "data": {"localId": "linked-uid", "idToken": "linked-id", "refreshToken": "linked-refresh", "expiresIn": "3600"}}
	adapter.responses.append(response)
	adapter.persist_success = false
	platform.signed_in = true
	_expect((await adapter.adopt_account_link(link)).get("reason") == "firebase_identity_persist_failed", "신원 저장 실패를 성공으로 알렸다")
	_expect(adapter._state == guest and adapter._current_id_token == "guest-id-token", "저장 실패가 재시도 전에 신원을 바꿨다")
	_expect(not platform.signed_in, "Firebase 복원 실패 후 불일치한 Platform 세션이 남았다")
	adapter.persist_success = true
	adapter.responses.append(response)
	platform.signed_in = true
	var result: Dictionary = await adapter.adopt_account_link(link)
	_expect(result.get("success") == true and result.get("uid") == "linked-uid" and result.get("restored") == true, "기존 연결 계정 복원이 실패했다")
	_expect(not adapter._state.has("id_token") and not "one-time-linked-token" in JSON.stringify(adapter._state), "복원 일회용 token이 저장됐다")
	var linked_state: Dictionary = adapter._state.duplicate(true)
	adapter.responses.append({"success": true, "data": {"user_id": "different-uid", "id_token": "different-token", "refresh_token": "different-refresh"}})
	_expect((await adapter._refresh_identity()).get("reason") == "platform_uid_mismatch", "일반 갱신이 다른 계정으로 전환했다")
	_expect(adapter._state == linked_state and adapter._current_id_token == "linked-id", "거부한 갱신이 연결 신원을 바꿨다")
	adapter.free()
	platform.free()


func _check_account_link_disk_failure() -> void:
	var platform := PlatformSpy.new()
	platform.signed_in = true
	root.add_child(platform)
	var adapter := FirebaseAdapterSpy.new()
	root.add_child(adapter)
	adapter.configure({"firebase_api_key": "api-key", "platform_client": platform, "state_path": IDENTITY_PATH})
	adapter.persist_to_disk = true
	var guest := {"uid": "guest-uid", "refresh_token": "guest-refresh", "auth_provider": "platform_custom_token_v1", "expires_at": 0}
	_expect(AtomicJsonStore.write(IDENTITY_PATH, guest), "복원 전 신원 저장 실패")
	var original := FileAccess.get_file_as_string(IDENTITY_PATH)
	var temp_path := IDENTITY_PATH + AtomicJsonStore.TEMP_SUFFIX
	_expect(DirAccess.make_dir_absolute(temp_path) == OK, "저장 실패 조건 생성 실패")
	var link := {"session": {"appUserId": "linked-uid", "isLinkedAccount": true, "isAnonymous": false},
		"firebaseCustomToken": "one-time-linked-token", "restored": true}
	var response := {"success": true, "data": {"localId": "linked-uid", "idToken": "linked-id", "refreshToken": "linked-refresh"}}
	adapter.responses.append(response)
	_expect((await adapter.adopt_account_link(link)).get("reason") == "firebase_identity_persist_failed", "파일 생성 실패 후 복원이 성공했다")
	_expect(FileAccess.get_file_as_string(IDENTITY_PATH) == original and adapter.current_identity().get("uid") == "guest-uid", "저장 실패 후 이전 파일 또는 신원이 변경됐다")
	_expect(DirAccess.remove_absolute(temp_path) == OK, "저장 실패 조건 제거 실패")
	adapter.responses.append(response)
	platform.signed_in = true
	_expect((await adapter.adopt_account_link(link)).get("success") == true, "저장 복구 후 재시도 실패")
	var reopened := FirebaseIdentityAdapter.new()
	reopened.configure({"state_path": IDENTITY_PATH})
	_expect(reopened.current_identity().get("uid") == "linked-uid", "재시작 후 복원 신원이 유실됐다")
	var stored := FileAccess.get_file_as_string(IDENTITY_PATH)
	_expect(not "one-time-linked-token" in stored and not "linked-id" in stored, "일회용 토큰 또는 ID 토큰이 파일에 남았다")
	reopened.free()
	adapter.free()
	platform.free()


func _collect_adoption(adapter: FirebaseAdapterSpy, link: Dictionary, results: Array[Dictionary]) -> void:
	results.append(await adapter.adopt_account_link(link))


func _check_account_link_auth_change() -> void:
	for action in ["logout", "different-account", "same-account-login"]:
		for success in [true, false]:
			var platform := PlatformSpy.new()
			root.add_child(platform)
			platform.signed_in = true
			var adapter := FirebaseAdapterSpy.new()
			root.add_child(adapter)
			adapter.configure({"firebase_api_key": "api-key", "platform_client": platform, "state_path": IDENTITY_PATH})
			adapter.persist_to_disk = true
			adapter.defer_response = true
			var guest := {"uid": "guest-uid", "refresh_token": "guest-refresh", "auth_provider": "platform_custom_token_v1"}
			_expect(AtomicJsonStore.write(IDENTITY_PATH, guest), "이전 신원 파일 생성 실패")
			var original := FileAccess.get_file_as_string(IDENTITY_PATH)
			var link := {"session": {"appUserId": "linked-uid", "isLinkedAccount": true, "isAnonymous": false},
				"firebaseCustomToken": "one-time-linked-token", "restored": true}
			var results: Array[Dictionary] = []
			_collect_adoption(adapter, link, results)
			_expect(adapter.requests.size() == 1 and results.is_empty() and adapter._identity_busy, "비동기 Firebase 교환 대기 조건이 없다")
			platform.sign_out()
			if action != "logout":
				platform.account_uid = "different-uid" if action == "different-account" else "linked-uid"
				platform.sign_in({}, func(_response: Dictionary) -> void: pass)
			adapter.response_ready.emit({"success": success, "data": {"localId": "linked-uid", "idToken": "linked-id", "refreshToken": "linked-refresh"}})
			await process_frame
			_expect(results.size() == 1 and results[0].get("reason") == "auth_state_changed", "인증 변경 뒤 늦은 Firebase 결과를 받아들였다")
			_expect(adapter.current_identity().get("uid") == "guest-uid" and FileAccess.get_file_as_string(IDENTITY_PATH) == original, "늦은 결과가 이전 신원 파일을 덮어썼다")
			_expect(platform.sign_out_count == 1 and platform.signed_in == (action != "logout"), "늦은 실패가 새 세션을 종료하거나 로그아웃을 되돌렸다")
			_expect(not adapter._identity_busy, "폐기한 Firebase 교환이 후속 신원 요청을 막았다")
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


func _check_identity_read_failure() -> void:
	for contents: String in ["{broken", "[]", "{}", '{"refresh_token":"fixture"}', '{"uid":42}', '{"uid":" "}',
		JSON.stringify({"uid": "a".repeat(129)}), JSON.stringify({"uid": "가".repeat(43)})]:
		var file := FileAccess.open(IDENTITY_FAIL_PATH, FileAccess.WRITE)
		file.store_string(contents)
		file.close()
		var adapter := FirebaseIdentityAdapter.new()
		adapter.configure({"state_path": IDENTITY_FAIL_PATH})
		var result := adapter.current_identity()
		_expect(result.get("success") == false and result.get("reason") == "firebase_identity_state_invalid", "신원 읽기 실패를 미가입으로 반환했다")
		var ensured: Dictionary = await adapter.ensure_identity()
		_expect(ensured.get("reason") == "firebase_identity_state_invalid", "손상된 기존 신원이 새 가입 경로로 진행됐다")
		_expect(FileAccess.get_file_as_string(IDENTITY_FAIL_PATH) == contents, "조회만으로 손상된 신원을 제거했다")
		adapter.free()
	DirAccess.remove_absolute(IDENTITY_FAIL_PATH)
	DirAccess.make_dir_absolute(IDENTITY_FAIL_PATH)
	var unreadable := FirebaseIdentityAdapter.new()
	unreadable.configure({"state_path": IDENTITY_FAIL_PATH})
	_expect(unreadable.current_identity().get("success") == false, "읽을 수 없는 신원 경로를 미가입으로 반환했다")
	unreadable.free()
	DirAccess.remove_absolute(IDENTITY_FAIL_PATH)
	var absent := FirebaseIdentityAdapter.new()
	absent.configure({"state_path": IDENTITY_FAIL_PATH})
	_expect(absent.current_identity().is_empty(), "실제로 없는 신원은 가입 없이 빈 결과를 반환해야 한다")
	absent.free()
	_expect(AtomicJsonStore.write(IDENTITY_FAIL_PATH, {"uid": "a".repeat(128)}), "최대 길이 신원을 저장하지 못했다")
	var maximum := FirebaseIdentityAdapter.new()
	maximum.configure({"state_path": IDENTITY_FAIL_PATH})
	_expect(maximum.current_identity().get("uid") == "a".repeat(128), "서버가 허용하는 128바이트 UID를 거부했다")
	maximum.free()


func _id_token(uid: String) -> String:
	var payload := Marshalls.utf8_to_base64(JSON.stringify({"user_id": uid, "sub": uid}))
	payload = payload.replace("+", "-").replace("/", "_").trim_suffix("=").trim_suffix("=")
	return "header.%s.signature" % payload


func _cleanup() -> void:
	for path in [IDENTITY_PATH, IDENTITY_FAIL_PATH, CLAIM_PATH, ACK_PATH]:
		if FileAccess.file_exists(path):
			DirAccess.remove_absolute(ProjectSettings.globalize_path(path))


func _expect(condition: bool, message: String) -> void:
	if not condition:
		_failures.append(message)
