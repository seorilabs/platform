## SDK의 신원·보상 상태를 원자적으로 보존하는 내부 JSON 저장소.
class_name SeoriAtomicJsonStore
extends RefCounted

const TEMP_SUFFIX := ".tmp"


static func read_dictionary(path: String) -> Dictionary:
	if not FileAccess.file_exists(path):
		return {"ok": true, "exists": false, "value": {}}
	var parsed: Variant = JSON.parse_string(FileAccess.get_file_as_string(path))
	if parsed is Dictionary:
		return {"ok": true, "exists": true, "value": parsed}
	return {"ok": false, "exists": true, "value": {}}


static func read_string_array(path: String) -> Dictionary:
	if not FileAccess.file_exists(path):
		return {"ok": true, "exists": false, "value": []}
	var parsed: Variant = JSON.parse_string(FileAccess.get_file_as_string(path))
	if not parsed is Array:
		return {"ok": false, "exists": true, "value": []}
	var values: Array[String] = []
	for raw_value in parsed:
		var value := String(raw_value).strip_edges()
		if not value.is_empty() and value not in values:
			values.append(value)
	return {"ok": true, "exists": true, "value": values}


static func write(path: String, value: Variant) -> bool:
	var temp_path := path + TEMP_SUFFIX
	var file := FileAccess.open(temp_path, FileAccess.WRITE)
	if file == null:
		return false
	file.store_string(JSON.stringify(value))
	file.flush()
	var write_error := file.get_error()
	file.close()
	if write_error != OK:
		_remove_if_exists(temp_path)
		return false
	var rename_error := DirAccess.rename_absolute(
		ProjectSettings.globalize_path(temp_path),
		ProjectSettings.globalize_path(path),
	)
	if rename_error != OK:
		_remove_if_exists(temp_path)
		return false
	return true


static func _remove_if_exists(path: String) -> void:
	if FileAccess.file_exists(path):
		DirAccess.remove_absolute(ProjectSettings.globalize_path(path))
