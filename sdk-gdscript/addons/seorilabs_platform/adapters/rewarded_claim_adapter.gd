## Platform의 보상 광고 claim과 AdMob SSV 상태 전이를 소유하는 표준 adapter.
##
## 네이티브 광고 로드/표시와 실제 게임 보상 정산은 소비자 게임이 소유한다.
## 이 adapter는 Platform 로그인, fail-closed 정책, claim, SSV 조회, ack 재시도만 맡는다.
class_name SeoriRewardedClaimAdapter
extends Node

const AtomicJsonStore := preload("../core/atomic_json_store.gd")
const DEFAULT_CLAIM_MAP_PATH := "user://seori_rewarded_claims.json"
const DEFAULT_ACK_QUEUE_PATH := "user://seori_rewarded_ack_queue.json"
const CALLBACK_TIMEOUT_MS := 15 * 1000
const STORAGE_LIMIT := 64
const REQUIRED_PLATFORM_METHODS := [
	"is_signed_in",
	"sign_in",
	"get_ads_policy",
	"create_reward_claim",
	"get_reward_claim",
	"ack_reward_claim",
]

var _platform_client: Node
var _identity_adapter: Node
var _client_platform := ""
var _claim_map_path := DEFAULT_CLAIM_MAP_PATH
var _ack_queue_path := DEFAULT_ACK_QUEUE_PATH
var _signing_in := false
var _claim_refs: Dictionary = {}
var _loaded := false
var _claim_storage_valid := true
var _ack_storage_valid := true


## options:
##   platform_client  : SeoriPlatformClient 필수
##   identity_adapter : SeoriFirebaseIdentityAdapter 필수
##   client_platform  : android | ios 필수
##   claim_map_path   : String 선택
##   ack_queue_path   : String 선택
func configure(options: Dictionary) -> void:
	_platform_client = options.get("platform_client") as Node
	_identity_adapter = options.get("identity_adapter") as Node
	_client_platform = String(options.get("client_platform", "")).strip_edges().to_lower()
	_claim_map_path = String(options.get("claim_map_path", DEFAULT_CLAIM_MAP_PATH)).strip_edges()
	_ack_queue_path = String(options.get("ack_queue_path", DEFAULT_ACK_QUEUE_PATH)).strip_edges()
	if _claim_map_path.is_empty():
		_claim_map_path = DEFAULT_CLAIM_MAP_PATH
	if _ack_queue_path.is_empty():
		_ack_queue_path = DEFAULT_ACK_QUEUE_PATH
	_load_once()


func policy() -> Dictionary:
	var session: Dictionary = await ensure_session()
	if not bool(session.get("success", false)):
		return {"success": false, "allowed": false, "reason": String(session.get("reason", "platform_auth_unavailable"))}
	var response: Dictionary = await _await_platform_call(func(callback: Callable) -> void:
		_platform_client.get_ads_policy(callback)
	)
	if not bool(response.get("ok", false)):
		return {"success": false, "allowed": false, "reason": String(response.get("code", "policy_unavailable"))}
	var result: Dictionary = response.get("result", {})
	var allowed := bool(result.get("appUsesAds", false)) and bool(result.get("adsEnabled", false))
	return {"success": true, "allowed": allowed, "reason": "" if allowed else "policy_blocked"}


func ensure_session(identity: Dictionary = {}) -> Dictionary:
	if not _has_required_contract():
		return _failure("platform_adapter_unavailable")
	if _client_platform not in ["android", "ios"]:
		return _failure("platform_invalid")
	if _platform_client.is_signed_in():
		return {"success": true}
	if _signing_in:
		var wait_deadline := Time.get_ticks_msec() + CALLBACK_TIMEOUT_MS
		while _signing_in and Time.get_ticks_msec() < wait_deadline:
			await get_tree().process_frame
		return {"success": _platform_client.is_signed_in(), "reason": "" if _platform_client.is_signed_in() else "platform_auth_timeout"}

	var firebase_identity := identity
	if firebase_identity.is_empty():
		firebase_identity = await _identity_adapter.ensure_identity()
	if not bool(firebase_identity.get("success", false)):
		return _failure(String(firebase_identity.get("reason", "firebase_identity_unavailable")))
	_signing_in = true
	var response: Dictionary = await _await_platform_call(func(callback: Callable) -> void:
		_platform_client.sign_in({
			"kind": "firebase-id-token",
			"value": String(firebase_identity.get("id_token", "")),
		}, callback)
	)
	_signing_in = false
	if not bool(response.get("ok", false)) or not _platform_client.is_signed_in():
		return _failure(String(response.get("code", "platform_auth_unavailable")))
	return {"success": true}


func _has_required_contract() -> bool:
	if _platform_client == null or _identity_adapter == null \
			or not _identity_adapter.has_method("ensure_identity"):
		return false
	for method in REQUIRED_PLATFORM_METHODS:
		if not _platform_client.has_method(method):
			return false
	return true


## request 키: request_id, placement, reward_key, reward_amount
func create_admob_claim(request: Dictionary) -> Dictionary:
	_load_once()
	if not _claim_storage_valid:
		return _failure("claim_storage_invalid")
	var request_id := String(request.get("request_id", "")).strip_edges()
	var placement := String(request.get("placement", "")).strip_edges()
	var reward_key := String(request.get("reward_key", "")).strip_edges()
	var reward_amount := int(request.get("reward_amount", 0))
	if request_id.is_empty() or placement.is_empty() or reward_key.is_empty() or reward_amount <= 0:
		return _failure("claim_invalid")
	var existing: Dictionary = _claim_refs.get(request_id, {})
	if not existing.is_empty():
		return {"success": true, "claim": existing.duplicate(true)}
	if _claim_refs.size() >= STORAGE_LIMIT:
		return _failure("claim_storage_full")
	var session: Dictionary = await ensure_session()
	if not bool(session.get("success", false)):
		return session
	var response: Dictionary = await _await_platform_call(func(callback: Callable) -> void:
		_platform_client.create_reward_claim({
			"requestId": request_id,
			"placement": placement,
			"provider": "admob",
			"clientPlatform": _client_platform,
			"reward": {"key": reward_key, "amount": reward_amount},
		}, callback)
	)
	if not bool(response.get("ok", false)):
		return _failure(String(response.get("code", "claim_rejected")))
	var result: Dictionary = response.get("result", {})
	var ssv: Dictionary = result.get("admobSsv", {})
	var claim_ref := {
		"claim_id": String(result.get("claimId", "")),
		"custom_data": String(ssv.get("customData", "")),
		"user_id": String(ssv.get("userId", "")),
	}
	if String(claim_ref.claim_id).is_empty() or String(claim_ref.custom_data).is_empty() or String(claim_ref.user_id).is_empty():
		return _failure("platform_claim_invalid")
	_claim_refs[request_id] = claim_ref
	if not _save_claim_refs():
		_claim_refs.erase(request_id)
		return _failure("claim_reference_persist_failed")
	return {"success": true, "claim": claim_ref.duplicate(true)}


func ssv_options(request_id: String) -> Dictionary:
	_load_once()
	var claim: Dictionary = _claim_refs.get(request_id, {})
	if claim.is_empty():
		return {}
	return {
		"custom_data": String(claim.get("custom_data", "")),
		"user_id": String(claim.get("user_id", "")),
	}


## create_admob_claim에 사용한 request를 그대로 받는다.
func recover_admob_claim(request: Dictionary) -> Dictionary:
	_load_once()
	var request_id := String(request.get("request_id", ""))
	if not _claim_storage_valid:
		return _status("pending", request_id, "claim_storage_invalid", false)
	var session: Dictionary = await ensure_session()
	if not bool(session.get("success", false)):
		return _status("pending", request_id, String(session.get("reason", "platform_auth_unavailable")), true)
	var claim_ref: Dictionary = _claim_refs.get(request_id, {})
	if claim_ref.is_empty():
		var created: Dictionary = await create_admob_claim(request)
		if not bool(created.get("success", false)):
			return _status("pending", request_id, String(created.get("reason", "claim_reference_unavailable")), true)
		claim_ref = _claim_refs.get(request_id, {})
	var platform_claim_id := String(claim_ref.get("claim_id", ""))
	var response: Dictionary = await _await_platform_call(func(callback: Callable) -> void:
		_platform_client.get_reward_claim(platform_claim_id, callback)
	)
	if not bool(response.get("ok", false)):
		var http_status := int(response.get("http_status", 0))
		return _status(
			"pending" if http_status == 0 or http_status >= 500 else "failed",
			request_id,
			String(response.get("code", "ssv_status_unavailable")),
			true,
		)
	var remote: Dictionary = response.get("result", {})
	var state := String(remote.get("state", ""))
	var assurance := String(remote.get("assurance", ""))
	if state in ["confirmed", "delivered"] and assurance == "server_verified":
		return _status("verified", request_id, "", true)
	if state == "expired":
		return _status("failed", request_id, "expired", true)
	return _status("pending", request_id, state, true)


## 로컬 exactly-once 정산이 끝난 뒤에만 호출한다.
func acknowledge(request_id: String) -> bool:
	_load_once()
	if not _claim_storage_valid:
		return false
	var claim_ref: Dictionary = _claim_refs.get(request_id, {})
	var platform_claim_id := String(claim_ref.get("claim_id", ""))
	if platform_claim_id.is_empty():
		return false
	if not _enqueue_ack(request_id):
		return false
	var session: Dictionary = await ensure_session()
	if not bool(session.get("success", false)):
		return false
	var response: Dictionary = await _await_platform_call(func(callback: Callable) -> void:
		_platform_client.ack_reward_claim(platform_claim_id, callback)
	)
	if not bool(response.get("ok", false)):
		return false
	if not _remove_ack(request_id):
		return false
	_claim_refs.erase(request_id)
	if not _save_claim_refs():
		_claim_refs[request_id] = claim_ref
		_enqueue_ack(request_id)
		return false
	return true


func flush_ack_queue() -> void:
	for request_id in _load_ack_queue():
		await acknowledge(request_id)


func _await_platform_call(start: Callable) -> Dictionary:
	var completed: Array[Dictionary] = []
	start.call(func(response: Dictionary) -> void: completed.append(response))
	var deadline := Time.get_ticks_msec() + CALLBACK_TIMEOUT_MS
	while completed.is_empty() and Time.get_ticks_msec() < deadline:
		await get_tree().process_frame
	if completed.is_empty():
		return {"ok": false, "code": "platform_timeout", "http_status": 0}
	return completed[0]


func _load_once() -> void:
	if _loaded:
		return
	_loaded = true
	var stored := AtomicJsonStore.read_dictionary(_claim_map_path)
	_claim_storage_valid = bool(stored.get("ok", false))
	if _claim_storage_valid:
		_claim_refs = (stored.get("value", {}) as Dictionary).duplicate(true)


func _save_claim_refs() -> bool:
	return AtomicJsonStore.write(_claim_map_path, _claim_refs)


func _load_ack_queue() -> Array[String]:
	var stored := AtomicJsonStore.read_string_array(_ack_queue_path)
	_ack_storage_valid = bool(stored.get("ok", false))
	if not _ack_storage_valid:
		return []
	var queue: Array[String] = []
	queue.assign(stored.get("value", []))
	return queue


func _enqueue_ack(request_id: String) -> bool:
	var queue := _load_ack_queue()
	if not _ack_storage_valid:
		return false
	if request_id not in queue:
		if queue.size() >= STORAGE_LIMIT:
			return false
		queue.append(request_id)
	return AtomicJsonStore.write(_ack_queue_path, queue)


func _remove_ack(request_id: String) -> bool:
	var queue := _load_ack_queue()
	if not _ack_storage_valid:
		return false
	queue.erase(request_id)
	return AtomicJsonStore.write(_ack_queue_path, queue)


func _failure(reason: String) -> Dictionary:
	return {"success": false, "reason": reason}


func _status(status: String, request_id: String, reason: String, final: bool) -> Dictionary:
	return {"status": status, "claim_id": request_id, "reason": reason, "final": final}
