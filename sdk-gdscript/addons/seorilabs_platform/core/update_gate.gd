## 업데이트 게이트 기본 오버레이.
##
## 앱마다 화면을 새로 짜지 않아도 되게 SDK가 기본형을 제공한다. 자기 UI로
## 그리고 싶으면 `update_gate_state()`가 준 Dictionary만 쓰면 된다.
class_name SeoriUpdateGate
extends CanvasLayer

## 게임 UI 위에 확실히 올라가야 한다. 아래로 깔리면 강제가 강제가 아니다.
const GATE_LAYER := 128

const DEFAULT_UPDATE_LABEL := "업데이트하기"
const DEFAULT_LATER_LABEL := "나중에"

signal later_pressed
signal update_pressed(url: String)

var _message_label: Label
var _update_button: Button
var _later_button: Button
var _update_url := ""


## 트리에 붙기 전에 화면을 다 만든다.
##
## _ready에서 만들면 add_child 직후 apply_state를 부르는 호출부가 아직
## 없는 노드를 건드린다. 진입 순서에 의존하지 않게 여기서 끝낸다.
func _init() -> void:
	layer = GATE_LAYER
	# 게임이 스스로 get_tree().paused = true를 걸어도 버튼이 동작해야 한다.
	# 강제 화면을 띄운 채 입력이 죽으면 유저가 갇힌다.
	process_mode = Node.PROCESS_MODE_ALWAYS

	var dimmer := ColorRect.new()
	dimmer.color = Color(0, 0, 0, 0.55)
	dimmer.set_anchors_preset(Control.PRESET_FULL_RECT)
	dimmer.mouse_filter = Control.MOUSE_FILTER_STOP
	add_child(dimmer)

	var center := CenterContainer.new()
	center.set_anchors_preset(Control.PRESET_FULL_RECT)
	center.mouse_filter = Control.MOUSE_FILTER_IGNORE
	add_child(center)

	var panel := PanelContainer.new()
	panel.custom_minimum_size = Vector2(320, 0)
	center.add_child(panel)

	var column := VBoxContainer.new()
	column.add_theme_constant_override("separation", 12)
	panel.add_child(column)

	_message_label = Label.new()
	_message_label.autowrap_mode = TextServer.AUTOWRAP_WORD_SMART
	_message_label.horizontal_alignment = HORIZONTAL_ALIGNMENT_CENTER
	_message_label.custom_minimum_size = Vector2(288, 0)
	column.add_child(_message_label)

	_update_button = Button.new()
	_update_button.pressed.connect(_on_update_pressed)
	column.add_child(_update_button)

	_later_button = Button.new()
	_later_button.pressed.connect(_on_later_pressed)
	column.add_child(_later_button)


## 상태를 화면에 반영한다. 이미 떠 있으면 내용만 갱신한다.
##
## options 키: labels(Dictionary: update, later)
func apply_state(state: Dictionary, options: Dictionary = {}) -> void:
	var labels: Dictionary = options.get("labels", {})
	_message_label.text = String(state.get("message", ""))
	_update_url = String(state.get("update_url", ""))

	# 눌러도 아무 일 없는 버튼을 만들지 않는다. 스토어 주소가 없거나
	# 점검이면 갈 곳 자체가 없다.
	_update_button.visible = not _update_url.is_empty()
	_update_button.text = String(labels.get("update", DEFAULT_UPDATE_LABEL))

	# 강제와 점검에는 닫기 수단을 만들지 않는다. 만들면 강제가 아니다.
	_later_button.visible = String(state.get("kind", "ok")) == "recommended"
	_later_button.text = String(labels.get("later", DEFAULT_LATER_LABEL))


func _on_update_pressed() -> void:
	if _update_url.is_empty():
		return
	update_pressed.emit(_update_url)


func _on_later_pressed() -> void:
	later_pressed.emit()
