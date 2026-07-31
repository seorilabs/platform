/**
 * conformance 벡터 검증.
 *
 * spec/conformance/*.json이 계약의 정본이고 TS와 GDScript가 같은
 * 출력을 내야 한다. 벡터를 코드에 복사하지 않고 직접 읽는다 —
 * 복사하면 벡터가 바뀌어도 테스트는 통과한다.
 */

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { normalizeParams, PII_KEYS } from "../src/normalize.ts";
import { backoffDelayMs, isRetryableStatus, parseRetryAfterMs } from "../src/backoff.ts";
import { parseEnvelope, LOCAL_RESPONSE_INVALID } from "../src/envelope.ts";

const here = dirname(fileURLToPath(import.meta.url));
const conformanceDir = resolve(here, "../../../spec/conformance");

function loadVector<T>(name: string): T {
  return JSON.parse(readFileSync(resolve(conformanceDir, name), "utf8")) as T;
}

/**
 * 벡터의 sentinel 문자열을 실제 값으로 바꾼다.
 *
 * NaN과 Infinity는 JSON으로 표현할 수 없어서 벡터에 문자열로 들어 있다.
 */
function resolveSentinels(value: unknown): unknown {
  if (value === "__nan__") return Number.NaN;
  if (value === "__pos_inf__") return Number.POSITIVE_INFINITY;
  if (value === "__neg_inf__") return Number.NEGATIVE_INFINITY;
  if (value === "__null__") return null;
  return value;
}

function resolveInput(input: Record<string, unknown>): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(input)) {
    out[k] = resolveSentinels(v);
  }
  return out;
}

describe("파라미터 정규화", () => {
  interface NormalizationVector {
    pii_keys: string[];
    cases: Array<{
      name: string;
      in: Record<string, unknown>;
      out: Record<string, unknown>;
    }>;
  }

  const vector = loadVector<NormalizationVector>("param-normalization.json");

  it("벡터에 케이스가 있다", () => {
    // 벡터를 못 읽으면 0건 통과로 조용히 넘어간다
    assert.ok(vector.cases.length > 0, "케이스가 비었다");
  });

  for (const c of vector.cases) {
    it(c.name, () => {
      const got = normalizeParams(resolveInput(c.in));
      assert.deepEqual(got, c.out);
    });
  }

  it("PII 키 목록이 벡터와 같다", () => {
    // 한쪽에만 키를 추가하면 서버와 SDK 판정이 어긋난다
    assert.deepEqual([...PII_KEYS].sort(), [...vector.pii_keys].sort());
  });

  it("키 순서가 정렬되어 있다", () => {
    // 25개 상한에 걸릴 때 어느 것을 남기는지가 결정적이어야
    // GDScript와 같은 결과가 나온다
    const input: Record<string, unknown> = {};
    for (let i = 0; i < 30; i++) {
      input[`k${String(i).padStart(2, "0")}`] = i;
    }
    const got = normalizeParams(input);
    const keys = Object.keys(got);
    assert.equal(keys.length, 25);
    assert.deepEqual(keys, [...keys].sort());
  });
});

describe("재시도 백오프", () => {
  interface BackoffVector {
    policy: { base_ms: number; factor: number; max_ms: number; jitter_ratio: number };
    schedule: Array<{ attempt: number; delay_ms: number }>;
    retryable_cases: Array<{ status: number; retry: boolean }>;
  }

  const vector = loadVector<BackoffVector>("backoff.json");

  it("벡터에 케이스가 있다", () => {
    assert.ok(vector.schedule.length > 0);
    assert.ok(vector.retryable_cases.length > 0);
  });

  for (const s of vector.schedule) {
    it(`시도 ${s.attempt}회는 ${s.delay_ms}ms 대기`, () => {
      assert.equal(backoffDelayMs(s.attempt), s.delay_ms);
    });
  }

  for (const c of vector.retryable_cases) {
    it(`status ${c.status}는 재시도 ${c.retry ? "함" : "안 함"}`, () => {
      assert.equal(isRetryableStatus(c.status), c.retry);
    });
  }

  it("정책 상수가 벡터와 같다", () => {
    assert.equal(backoffDelayMs(1), vector.policy.base_ms);
    assert.equal(backoffDelayMs(999), vector.policy.max_ms);
  });

  it("attempt가 0이나 음수여도 첫 간격을 준다", () => {
    assert.equal(backoffDelayMs(0), vector.policy.base_ms);
    assert.equal(backoffDelayMs(-5), vector.policy.base_ms);
  });

  it("Retry-After를 우선한다", () => {
    assert.equal(parseRetryAfterMs("30"), 30_000);
    assert.equal(parseRetryAfterMs("0"), 0);
    assert.equal(parseRetryAfterMs(null), undefined);
    assert.equal(parseRetryAfterMs(""), undefined);
    assert.equal(parseRetryAfterMs("나중에"), undefined);
  });

  it("Retry-After가 HTTP-date여도 읽는다", () => {
    const now = () => Date.parse("2026-07-31T12:00:00Z");
    assert.equal(parseRetryAfterMs("Fri, 31 Jul 2026 12:00:30 GMT", now), 30_000);
    // 이미 지난 시각은 0이다. 음수를 그대로 쓰면 즉시 재시도 루프가 된다
    assert.equal(parseRetryAfterMs("Fri, 31 Jul 2026 11:59:00 GMT", now), 0);
  });
});

describe("응답 envelope", () => {
  interface EnvelopeVector {
    cases: Array<{
      name: string;
      http_status: number;
      /** JSON 본문. raw_body와 배타적이다. */
      body?: unknown;
      /** 파싱 전 원문. JSON이 아닌 응답을 표현한다. */
      raw_body?: string;
      expect: { valid: boolean; ok?: boolean; code?: string; local_code?: string };
    }>;
  }

  /**
   * 케이스에서 파싱 대상 본문을 얻는다.
   *
   * raw_body는 서버가 JSON이 아닌 것을 보낸 상황이다.
   * Transport가 하는 것과 같은 파싱을 여기서 재현한다.
   */
  function caseBody(c: EnvelopeVector["cases"][number]): unknown {
    if (c.raw_body === undefined) {
      return c.body;
    }
    if (c.raw_body === "") {
      return null;
    }
    try {
      return JSON.parse(c.raw_body);
    } catch {
      return null;
    }
  }

  const vector = loadVector<EnvelopeVector>("envelope.json");

  it("벡터에 케이스가 있다", () => {
    assert.ok(vector.cases.length > 0);
  });

  it("body와 raw_body 중 하나는 있다", () => {
    // 둘 다 없으면 undefined를 파싱해 우연히 통과한다
    for (const c of vector.cases) {
      assert.ok(
        "body" in c || "raw_body" in c,
        `케이스 '${c.name}'에 본문이 없다`,
      );
    }
  });

  for (const c of vector.cases) {
    it(c.name, () => {
      const got = parseEnvelope(c.http_status, caseBody(c));

      assert.equal(got.valid, c.expect.valid, "유효성 판정이 다르다");

      if (!got.valid) {
        assert.equal(got.localCode, c.expect.local_code ?? LOCAL_RESPONSE_INVALID);
        return;
      }

      if (c.expect.ok !== undefined) {
        assert.equal(got.ok, c.expect.ok);
      }
      if (c.expect.code !== undefined) {
        assert.ok(!got.ok, "오류를 기대했는데 성공이다");
        assert.equal(got.error.code, c.expect.code);
      }
    });
  }

  it("미지 필드가 있어도 깨지지 않는다", () => {
    // R4의 전제다. 서버가 필드를 추가해도 구버전 SDK가 살아 있어야 한다.
    const got = parseEnvelope(200, {
      ok: true,
      result: { entitlements: [], 미래필드: 1 },
      또다른미래필드: "x",
    });
    assert.ok(got.valid && got.ok);
  });
});
