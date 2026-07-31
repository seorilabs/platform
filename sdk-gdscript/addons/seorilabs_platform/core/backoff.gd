## 재시도 백오프.
##
## 계약은 spec/conformance/backoff.json이 정본이다.
##
## 클라이언트의 무한 재시도 루프는 비용 사고의 1순위 원인이다.
## 앱 수만 대가 동시에 실패한 요청을 반복하면 Cloud Run 인스턴스가
## 상한까지 치솟는다. 그래서 정책을 CI에서 고정한다.
class_name SeoriBackoff
extends RefCounted

const BASE_MS := 1000
const FACTOR := 2
const MAX_MS := 60000
const JITTER_RATIO := 0.2


## 시도 횟수에 따른 대기 시간(ms)이다. 지터는 포함하지 않는다.
##
##   delay = min(base * factor^(attempt-1), max)
##
## attempt는 1부터 센다.
static func delay_ms(attempt: int) -> int:
	var n := attempt if attempt >= 1 else 1

	var delay := BASE_MS
	for i in range(1, n):
		delay *= FACTOR
		# 지수 계산이 상한을 넘으면 즉시 끊는다.
		# pow로 한 번에 계산하면 큰 attempt에서 오버플로가 난다.
		if delay >= MAX_MS:
			return MAX_MS
	return delay


## 지터를 적용한 실제 대기 시간(ms)이다.
##
## 지터가 없으면 동시에 실패한 클라이언트들이 같은 시각에 다시 몰려온다.
## 그 자체가 다음 장애의 원인이 된다.
static func delay_with_jitter_ms(attempt: int, rng: RandomNumberGenerator = null) -> int:
	var base := delay_ms(attempt)
	var spread := float(base) * JITTER_RATIO

	var r := randf() if rng == null else rng.randf()
	var jitter := (r * 2.0 - 1.0) * spread

	return maxi(0, int(round(float(base) + jitter)))


## 재시도해도 될 응답인지 본다.
##
## 4xx는 재시도하지 않는다. 요청 자체가 잘못됐다는 뜻이라
## 같은 요청을 다시 보내도 결과가 같다. 429만 예외다.
##
## status 0은 네트워크 오류나 타임아웃이다.
static func is_retryable_status(status: int) -> bool:
	if status == 0:
		return true
	if status == 429:
		return true
	return status >= 500


## Retry-After 헤더를 밀리초로 읽는다.
##
## 서버가 언제 다시 오라고 했으면 그 말을 따른다.
## 우리 백오프로 덮어쓰면 rate limit을 계속 두드리게 된다.
##
## 읽지 못하면 -1을 준다.
static func parse_retry_after_ms(header_value: String) -> int:
	var trimmed := header_value.strip_edges()
	if trimmed.is_empty():
		return -1

	# delay-seconds 형식
	if trimmed.is_valid_int():
		var seconds := trimmed.to_int()
		if seconds < 0:
			return -1
		return seconds * 1000

	# HTTP-date는 Godot에 파서가 없다.
	# 서버가 초 단위로만 보내도록 맞춘다.
	return -1
