extends Node
const Identity = preload("res://addons/seorilabs_platform/adapters/firebase_identity_adapter.gd")
const Transport = preload("res://addons/seorilabs_platform/core/http_transport.gd")
func _ready() -> void:
	var cases: Array[Dictionary] = []
	var identity: Node = Identity.new()
	add_child(identity)
	var transport: Node = Transport.new()
	transport.configure(str(ProjectSettings.get_setting("test/cross_origin")), "synthetic-fixture", 0)
	add_child(transport)
	for exposure: String in ["same", "exposed", "hidden"]:
		var base: String = str(ProjectSettings.get_setting("test/same_origin" if exposure == "same" else "test/cross_origin"))
		var path: String = "/gzip/identity?exposure=" + exposure
		var response: Dictionary = await identity._request_json(base + path, HTTPClient.METHOD_POST, PackedStringArray(), "{}")
		var data: Dictionary = response.get("data", {})
		var actual: String = str(data.get("fixtureMarker", ""))
		cases.append({"layer":"identity", "exposure":exposure, "httpStatus":int(response.get("status", 0)), "actualMarker":actual, "pass":bool(response.get("success", false)) and actual == "gzip_fixture_v1"})
		var received: Array[Dictionary] = []
		transport.request({"method":"POST", "base_url":base, "path":"/gzip/envelope?exposure=" + exposure, "no_retry":true, "body":{"synthetic":true}}, func(r: Dictionary) -> void: received.append(r))
		var deadline: int = Time.get_ticks_msec() + 17000
		while received.is_empty() and Time.get_ticks_msec() < deadline:
			await get_tree().process_frame
		var envelope: Dictionary = received[0] if not received.is_empty() else {"code":"fixture_timeout"}
		var payload: Dictionary = envelope.get("result", {})
		actual = str(payload.get("fixtureMarker", ""))
		cases.append({"layer":"transport", "exposure":exposure, "httpStatus":int(envelope.get("http_status", 0)), "actualMarker":actual, "code":str(envelope.get("code", "")), "pass":bool(envelope.get("ok", false)) and actual == "gzip_fixture_v1"})
	var passed: int = 0
	for result: Dictionary in cases:
		if bool(result["pass"]): passed += 1
	var report: Dictionary = {"variant":ProjectSettings.get_setting("application/config/name"), "platform":"web" if OS.has_feature("web") else "native-headless", "passed":passed, "total":cases.size(), "cases":cases, "realFirebaseCalls":0}
	print("GZIP_FIXTURE_RESULT " + JSON.stringify(report))
	if OS.has_feature("web"):
		var sink: JavaScriptObject = JavaScriptBridge.get_interface("fixtureSink")
		sink.done(JSON.stringify(report))
	else:
		get_tree().quit(0 if passed == cases.size() else 1)
