extends SceneTree
const Client = preload("res://addons/seorilabs_platform/platform_client.gd")
const Transport = preload("res://addons/seorilabs_platform/core/http_transport.gd")
class Server extends Transport:
	var response: Dictionary = {"ok":false,"code":"network_error"}
	func request(_data: Dictionary, callback: Callable) -> void: callback.call(response)
var failures := 0
func check(value: bool, label: String) -> void:
	if not value: failures+=1;print("FAIL ",label)
func _initialize() -> void:
	var client := Client.new()
	var server := Server.new()
	client._transport=server
	client.add_child(server)
	var responses: Array = []
	client.fetch_config({},func(r: Dictionary): responses.append(r))
	check(responses[0].ok,"offline remains fail open")
	check(responses[0].get("networkOk")==false,"fallback is not a network success")
	server.response={"ok":true,"result":{"features":{},"sdk":{"status":"blocked"},"maintenance":{"active":false}}}
	client.fetch_config({},func(r: Dictionary): responses.append(r))
	check(responses[1].get("networkOk")==true and client.is_sdk_blocked(),"successful fetch marked")
	server.response={"ok":false,"code":"network_error"}
	client.fetch_config({},func(r: Dictionary): responses.append(r))
	check(responses[2].get("networkOk")==false and client.is_sdk_blocked(),"failure retains last known block")
	client.free()
	print("Config observation: 4 checks, ",failures," failures")
	quit(0 if failures==0 else 1)
