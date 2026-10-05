## 로컬 httptest + Firestore emulator와 실제 SDK 전송을 연결한다.
extends SceneTree
const Client = preload("res://addons/seorilabs_platform/platform_client.gd")
var sdk: Node
var result: Dictionary = {}
var failures: Array[String] = []
func _initialize() -> void:
 call_deferred("run")
func response(value: Dictionary) -> void: result = value
func wait_response() -> void:
 var deadline := Time.get_ticks_msec() + 15000
 while result.is_empty() and Time.get_ticks_msec() < deadline: await process_frame
 if result.is_empty(): failures.append("response timeout")
func check(value: bool, message: String) -> void:
 if not value: failures.append(message)
func run() -> void:
 var url := ""
 for argument in OS.get_cmdline_user_args():
  if argument.begins_with("http://127.0.0.1:"): url=argument
 if url.is_empty(): push_error("local fixture URL required");quit(1);return
 sdk=Client.new()
 sdk.configure({"base_url":url,"app_id":"sdk-fixture","max_retries":0})
 root.add_child(sdk)
 sdk._store_session({"platformToken":"fixture-session","platformUserId":"pu_01ARZ3NDEKTSV4RRFFQ69G5FAV","expiresIn":3600})
 sdk.list_inbox("",response);await wait_response()
 check(result.get("ok",false) and result.get("result",{}).get("messages",[]).size()==1,"list contract")
 result={};sdk.read_inbox("welcome",response);await wait_response()
 check(result.get("ok",false) and result.result.readAt>0 and result.result.claimedAt==0,"read != claim")
 result={};sdk.claim_inbox_batch(["welcome","missing"],response);await wait_response()
 check(result.get("ok",false) and result.result.results[0].ok and not result.result.results[1].ok,"batch partial failure")
 result={};sdk.claim_inbox("welcome",response);await wait_response()
 check(result.get("ok",false) and result.result.claimedAt>0,"claim replay")
 sdk.sign_out()
 result={};sdk.claim_inbox("welcome",response);await wait_response()
 check(not result.get("ok",true),"unauthenticated claim accepted")
 sdk.queue_free()
 await process_frame
 if failures.is_empty(): print("Inbox SDK + HTTP + Firestore integration passed");quit(0)
 else:
  for failure in failures: push_error(failure)
  quit(1)
