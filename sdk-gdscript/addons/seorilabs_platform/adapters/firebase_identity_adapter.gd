## Platform Custom Token으로 Firebase 신원을 만드는 표준 Godot adapter.
##
## 직접 signUp 익명 인증 경로를 두지 않는다. 기존 Firebase UID가 있으면
## 유효한 ID token을 Platform에 넘겨 같은 UID로 이전하고, 실패하면 새 UID로
## 우회하지 않는다.
class_name SeoriFirebaseIdentityAdapter
extends Node

const AtomicJsonStore := preload("../core/atomic_json_store.gd")
const DEFAULT_STATE_PATH := "user://seori_firebase_identity.json"
const TOKEN_REFRESH_MARGIN_SECONDS := 300
const IDENTITY_BASE_URL := "https://identitytoolkit.googleapis.com/v1"
const SECURE_TOKEN_BASE_URL := "https://securetoken.googleapis.com/v1"
const PLATFORM_AUTH_PROVIDER := "platform_custom_token_v1"
const CALLBACK_TIMEOUT_MS := 15 * 1000

var _api_key := ""
var _platform_client: Node
var _state_path := DEFAULT_STATE_PATH
var _app_check_token := ""
var _state: Dictionary = {}
var _current_id_token := ""
var _loaded := false
var _state_valid := true
var _state_dirty := false


## options:
##   firebase_api_key : String 필수
##   platform_client  : SeoriPlatformClient 필수
##   state_path       : String 선택
##   app_check_token  : String 선택. attestation 갱신 뒤 set_app_check_token으로 교체
func configure(options: Dictionary) -> void:
	_api_key = String(options.get("firebase_api_key", "")).strip_edges()
	_platform_client = options.get("platform_client") as Node
	_state_path = String(options.get("state_path", DEFAULT_STATE_PATH)).strip_edges()
	if _state_path.is_empty():
		_state_path = DEFAULT_STATE_PATH
	_app_check_token = String(options.get("app_check_token", "")).strip_edges()


func set_app_check_token(token: String) -> void:
	_app_check_token = token.strip_edges()


func ensure_identity() -> Dictionary:
	_load_state_once()
	if not _state_valid:
		return _failure("firebase_identity_state_invalid")
	if _api_key.is_empty():
		return _failure("firebase_api_key_missing")
	if _platform_client == null or not _platform_client.has_method("create_firebase_custom_token"):
		return _failure("platform_auth_sdk_unavailable")

	var now := int(Time.get_unix_time_from_system())
	var platform_managed := String(_state.get("auth_provider", "")) == PLATFORM_AUTH_PROVIDER
	var id_token := _current_id_token
	if platform_managed and not id_token.is_empty() \
			and int(_state.get("expires_at", 0)) > now + TOKEN_REFRESH_MARGIN_SECONDS:
		if _state_dirty and not _save_state():
			return _failure("firebase_identity_persist_failed")
		return _identity_result()
	if platform_managed:
		if String(_state.get("refresh_token", "")).is_empty():
			return _failure("platform_identity_refresh_unavailable")
		var refreshed: Dictionary = await _refresh_identity()
		if bool(refreshed.get("success", false)):
			return refreshed
		return _failure("platform_identity_refresh_failed")

	var legacy_uid := String(_state.get("uid", ""))
	var existing_id_token := ""
	if not _state.is_empty():
		if not id_token.is_empty() \
				and int(_state.get("expires_at", 0)) > now + TOKEN_REFRESH_MARGIN_SECONDS:
			existing_id_token = id_token
		elif not String(_state.get("refresh_token", "")).is_empty():
			var legacy_refresh: Dictionary = await _refresh_identity()
			if not bool(legacy_refresh.get("success", false)):
				return _failure("legacy_identity_refresh_failed")
			existing_id_token = String(legacy_refresh.get("id_token", ""))
			legacy_uid = String(legacy_refresh.get("uid", legacy_uid))
		else:
			return _failure("legacy_identity_migration_unavailable")
	return await _sign_in_with_platform_custom_token(existing_id_token, legacy_uid)


func current_identity() -> Dictionary:
	_load_state_once()
	return _identity_result() if not _state.is_empty() else {}


func clear_local_state() -> void:
	_state = {}
	_current_id_token = ""
	_loaded = true
	_state_valid = true
	_state_dirty = false
	if FileAccess.file_exists(_state_path):
		DirAccess.remove_absolute(ProjectSettings.globalize_path(_state_path))


func _sign_in_with_platform_custom_token(existing_id_token: String, expected_uid: String) -> Dictionary:
	var response: Dictionary = await _await_platform_call(func(callback: Callable) -> void:
		_platform_client.create_firebase_custom_token(
			existing_id_token, _app_check_token, callback)
	)
	if not bool(response.get("ok", false)):
		return _failure(String(response.get("code", "platform_auth_failed")))
	var bridge_result: Dictionary = response.get("result", {})
	var custom_token := String(bridge_result.get("firebaseCustomToken", ""))
	if custom_token.is_empty():
		return _failure("platform_custom_token_missing")

	var result: Dictionary = await _request_json(
		"%s/accounts:signInWithCustomToken?key=%s" % [IDENTITY_BASE_URL, _api_key.uri_encode()],
		HTTPClient.METHOD_POST,
		[],
		JSON.stringify({"token": custom_token, "returnSecureToken": true}),
	)
	if not bool(result.get("success", false)):
		return _failure("firebase_custom_token_sign_in_failed")
	var data: Dictionary = result.get("data", {})
	var next_id_token := String(data.get("idToken", ""))
	var next_refresh_token := String(data.get("refreshToken", ""))
	var next_uid := String(data.get("localId", ""))
	if next_uid.is_empty():
		next_uid = _firebase_uid_from_id_token(next_id_token)
	if next_uid.is_empty() or next_id_token.is_empty() or next_refresh_token.is_empty():
		return _failure("firebase_custom_token_invalid_response")
	if not expected_uid.is_empty() and next_uid != expected_uid:
		return _failure("platform_uid_mismatch")

	_current_id_token = next_id_token
	_state = {
		"uid": next_uid,
		"refresh_token": next_refresh_token,
		"expires_at": int(Time.get_unix_time_from_system()) + int(data.get("expiresIn", 3600)),
		"auth_provider": PLATFORM_AUTH_PROVIDER,
	}
	_state_dirty = true
	if not _save_state():
		return _failure("firebase_identity_persist_failed")
	return _identity_result()


func _refresh_identity() -> Dictionary:
	var refresh_token := String(_state.get("refresh_token", ""))
	var auth_provider := String(_state.get("auth_provider", ""))
	if refresh_token.is_empty():
		return _failure("firebase_refresh_token_missing")
	var result: Dictionary = await _request_json(
		"%s/token?key=%s" % [SECURE_TOKEN_BASE_URL, _api_key.uri_encode()],
		HTTPClient.METHOD_POST,
		["Content-Type: application/x-www-form-urlencoded"],
		"grant_type=refresh_token&refresh_token=%s" % refresh_token.uri_encode(),
		false,
	)
	if not bool(result.get("success", false)):
		return _failure("firebase_token_refresh_failed")
	var data: Dictionary = result.get("data", {})
	var next_uid := String(data.get("user_id", _state.get("uid", "")))
	var next_id_token := String(data.get("id_token", ""))
	var next_refresh_token := String(data.get("refresh_token", refresh_token))
	if next_uid.is_empty() or next_id_token.is_empty() or next_refresh_token.is_empty():
		return _failure("firebase_token_refresh_invalid_response")
	_current_id_token = next_id_token
	_state = {
		"uid": next_uid,
		"refresh_token": next_refresh_token,
		"expires_at": int(Time.get_unix_time_from_system()) + int(data.get("expires_in", 3600)),
	}
	if not auth_provider.is_empty():
		_state["auth_provider"] = auth_provider
	_state_dirty = true
	if not _save_state():
		return _failure("firebase_identity_persist_failed")
	return _identity_result()


func _request_json(
	url: String,
	method: int,
	headers: PackedStringArray,
	body: String,
	add_json_header: bool = true,
) -> Dictionary:
	var request := HTTPRequest.new()
	add_child(request)
	var safe_headers := headers.duplicate()
	if add_json_header and not safe_headers.has("Content-Type: application/json"):
		safe_headers.append("Content-Type: application/json")
	var start_error := request.request(url, safe_headers, method, body)
	if start_error != OK:
		request.queue_free()
		return {"success": false, "status": 0, "reason": "http_start_failed"}
	var response: Array = await request.request_completed
	request.queue_free()
	var status := int(response[1])
	var raw_body := (response[3] as PackedByteArray).get_string_from_utf8()
	var json := JSON.new()
	var data: Dictionary = {}
	if json.parse(raw_body) == OK and json.data is Dictionary:
		data = json.data
	return {"success": status >= 200 and status < 300, "status": status, "data": data}


func _await_platform_call(start: Callable) -> Dictionary:
	var completed: Array[Dictionary] = []
	start.call(func(response: Dictionary) -> void: completed.append(response))
	var deadline := Time.get_ticks_msec() + CALLBACK_TIMEOUT_MS
	while completed.is_empty() and Time.get_ticks_msec() < deadline:
		await get_tree().process_frame
	if completed.is_empty():
		return {"ok": false, "code": "platform_timeout", "http_status": 0}
	return completed[0]


func _load_state_once() -> void:
	if _loaded:
		return
	_loaded = true
	var stored := AtomicJsonStore.read_dictionary(_state_path)
	_state_valid = bool(stored.get("ok", false))
	if _state_valid:
		_state = (stored.get("value", {}) as Dictionary).duplicate(true)
		# 0.6.1까지 저장하던 ID token은 메모리로만 옮기고 디스크에서 즉시 제거한다.
		if _state.has("id_token"):
			_current_id_token = String(_state.get("id_token", ""))
			_state.erase("id_token")
			_state_dirty = true
			if not _save_state():
				_state_valid = false


func _save_state() -> bool:
	if not AtomicJsonStore.write(_state_path, _state):
		return false
	_state_dirty = false
	return true


func _identity_result() -> Dictionary:
	return {
		"success": true,
		"uid": String(_state.get("uid", "")),
		"id_token": _current_id_token,
	}


func _firebase_uid_from_id_token(id_token: String) -> String:
	var parts := id_token.split(".")
	if parts.size() != 3:
		return ""
	var payload := String(parts[1]).replace("-", "+").replace("_", "/")
	while payload.length() % 4 != 0:
		payload += "="
	var json := JSON.new()
	if json.parse(Marshalls.base64_to_utf8(payload)) != OK or not json.data is Dictionary:
		return ""
	return String(json.data.get("user_id", json.data.get("sub", "")))


func _failure(reason: String) -> Dictionary:
	return {"success": false, "reason": reason}
