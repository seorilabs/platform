/**
 * 재시도 백오프.
 *
 * 계약은 spec/conformance/backoff.json이 정본이다.
 *
 * 클라이언트의 무한 재시도 루프는 비용 사고의 1순위 원인이다.
 * 앱 수만 대가 동시에 실패한 요청을 반복하면 Cloud Run 인스턴스가
 * 상한까지 치솟는다. 그래서 정책을 CI에서 고정한다.
 */

export const BACKOFF_BASE_MS = 1000;
export const BACKOFF_FACTOR = 2;
export const BACKOFF_MAX_MS = 60_000;
export const BACKOFF_JITTER_RATIO = 0.2;

/**
 * 시도 횟수에 따른 대기 시간을 계산한다. 지터는 포함하지 않는다.
 *
 *   delay = min(base * factor^(attempt-1), max)
 *
 * attempt는 1부터 센다.
 */
export function backoffDelayMs(attempt: number): number {
  const n = attempt < 1 ? 1 : Math.floor(attempt);

  let delay = BACKOFF_BASE_MS;
  for (let i = 1; i < n; i++) {
    delay *= BACKOFF_FACTOR;
    // 지수 계산이 상한을 넘으면 즉시 끊는다.
    // Math.pow로 한 번에 계산하면 큰 attempt에서 Infinity가 된다.
    if (delay >= BACKOFF_MAX_MS) {
      return BACKOFF_MAX_MS;
    }
  }
  return delay;
}

/**
 * 지터를 적용한 실제 대기 시간이다.
 *
 * 지터가 없으면 동시에 실패한 클라이언트들이 같은 시각에 다시 몰려온다.
 * 그 자체가 다음 장애의 원인이 된다.
 */
export function backoffWithJitter(
  attempt: number,
  random: () => number = Math.random,
): number {
  const base = backoffDelayMs(attempt);
  const spread = base * BACKOFF_JITTER_RATIO;
  // random()이 [0,1)이므로 [-spread, +spread) 범위가 된다.
  const jitter = (random() * 2 - 1) * spread;
  return Math.max(0, Math.round(base + jitter));
}

/**
 * 재시도해도 될 응답인지 본다.
 *
 * 4xx는 재시도하지 않는다. 요청 자체가 잘못됐다는 뜻이라
 * 같은 요청을 다시 보내도 결과가 같다. 429만 예외다.
 *
 * status 0은 네트워크 오류나 타임아웃이다.
 */
export function isRetryableStatus(status: number): boolean {
  if (status === 0) {
    return true;
  }
  if (status === 429) {
    return true;
  }
  return status >= 500;
}

/**
 * `Retry-After` 헤더를 밀리초로 읽는다.
 *
 * 서버가 언제 다시 오라고 했으면 그 말을 따른다.
 * 우리 백오프로 덮어쓰면 rate limit을 계속 두드리게 된다.
 *
 * 초 단위 숫자와 HTTP-date 두 형식을 모두 받는다.
 */
export function parseRetryAfterMs(
  headerValue: string | null | undefined,
  now: () => number = Date.now,
): number | undefined {
  if (!headerValue) {
    return undefined;
  }

  const trimmed = headerValue.trim();
  if (trimmed === "") {
    return undefined;
  }

  // delay-seconds 형식
  if (/^\d+$/.test(trimmed)) {
    return Number(trimmed) * 1000;
  }

  // HTTP-date 형식
  const at = Date.parse(trimmed);
  if (Number.isNaN(at)) {
    return undefined;
  }
  const delta = at - now();
  return delta > 0 ? delta : 0;
}

/**
 * 다음 재시도까지 기다릴 시간이다.
 *
 * `Retry-After`가 있으면 그것을 쓰고, 없으면 백오프를 쓴다.
 */
export function nextDelayMs(
  attempt: number,
  retryAfterHeader?: string | null,
  random: () => number = Math.random,
  now: () => number = Date.now,
): number {
  const explicit = parseRetryAfterMs(retryAfterHeader, now);
  if (explicit !== undefined) {
    return explicit;
  }
  return backoffWithJitter(attempt, random);
}
