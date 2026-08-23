/**
 * SDK 동작 검증. fake fetch로 서버 없이 확인한다.
 */

import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { Transport, PlatformError } from "../src/transport.ts";
import { SessionManager, MemorySessionStore } from "../src/session.ts";
import { Events, MemoryEventOutbox } from "../src/events.ts";
import { Iap } from "../src/iap.ts";
import { Config } from "../src/config.ts";
import { Ads } from "../src/ads.ts";
import { Platform, SDK_VERSION } from "../src/index.ts";

interface FakeCall {
  url: string;
  method: string;
  headers: Record<string, string>;
  body: unknown;
}

interface FakeReply {
  status: number;
  body: unknown;
  headers?: Record<string, string>;
}

/** 순서대로 응답을 돌려주는 fake fetch. */
function fakeFetch(replies: FakeReply[]) {
  const calls: FakeCall[] = [];
  let i = 0;

  const impl = (async (url: string | URL, init?: RequestInit) => {
    const reply = replies[Math.min(i, replies.length - 1)]!;
    i++;

    calls.push({
      url: String(url),
      method: init?.method ?? "GET",
      headers: (init?.headers ?? {}) as Record<string, string>,
      body: init?.body ? JSON.parse(init.body as string) : undefined,
    });

    return new Response(JSON.stringify(reply.body), {
      status: reply.status,
      headers: { "Content-Type": "application/json", ...reply.headers },
    });
  }) as unknown as typeof fetch;

  return { impl, calls, get count() { return i; } };
}

function newTransport(fetchImpl: typeof fetch, maxRetries = 3) {
  return new Transport({
    baseUrl: "https://platform.test",
    appId: "lizard-tycoon",
    fetchImpl,
    maxRetries,
    // 테스트에서 실제로 기다리지 않는다
    sleep: async () => {},
    random: () => 0.5,
    now: () => 1_700_000_000_000,
  });
}

const ok = (result: unknown): FakeReply => ({ status: 200, body: { ok: true, result } });
const fail = (status: number, code: string): FakeReply => ({
  status,
  body: { ok: false, error: { code, message: "테스트 오류" } },
});

describe("Transport", () => {
  it("앱 헤더를 붙인다", async () => {
    const f = fakeFetch([ok({ value: 1 })]);
    await newTransport(f.impl).request({ method: "GET", path: "/v1/test" });

    assert.equal(f.calls[0]!.headers["X-Seori-App"], "lizard-tycoon");
  });

  it("App Check 공급자의 최신 토큰을 붙인다", async () => {
    const f = fakeFetch([ok({}), ok({})]);
    let token = "attested-1";
    const transport = new Transport({
      baseUrl: "https://platform.test",
      appId: "ungeul",
      fetchImpl: f.impl,
      appCheckToken: async () => token,
    });
    await transport.request({ method: "GET", path: "/v1/test" });
    token = "attested-2";
    await transport.request({ method: "GET", path: "/v1/test" });

    assert.equal(f.calls[0]!.headers["X-Firebase-AppCheck"], "attested-1");
    assert.equal(f.calls[1]!.headers["X-Firebase-AppCheck"], "attested-2");
  });

  it("App Check 공급자가 멈춰도 요청 제한 시간을 지킨다", async () => {
    const f = fakeFetch([ok({})]);
    const transport = new Transport({
      baseUrl: "https://platform.test",
      appId: "ungeul",
      fetchImpl: f.impl,
      timeoutMs: 5,
      appCheckToken: async () => new Promise(() => {}),
    });

    await assert.rejects(
      transport.request({ method: "GET", path: "/v1/test", noRetry: true }),
      (error: unknown) => error instanceof PlatformError && error.code === "network_error",
    );
    assert.equal(f.calls.length, 0);
  });

  it("토큰이 있으면 Authorization을 붙인다", async () => {
    const f = fakeFetch([ok({})]);
    await newTransport(f.impl).request({
      method: "GET",
      path: "/v1/test",
      token: "tok-1",
    });

    assert.equal(f.calls[0]!.headers["Authorization"], "Bearer tok-1");
  });

  it("토큰이 없으면 Authorization을 붙이지 않는다", async () => {
    const f = fakeFetch([ok({})]);
    await newTransport(f.impl).request({ method: "GET", path: "/v1/test" });

    assert.equal(f.calls[0]!.headers["Authorization"], undefined);
  });

  // 5xx는 일시적일 수 있다. 재시도가 성공하면 호출자는 실패를 모른다.
  it("5xx는 재시도한다", async () => {
    const f = fakeFetch([fail(503, "unavailable"), fail(503, "unavailable"), ok({ v: 9 })]);
    const got = await newTransport(f.impl).request<{ v: number }>({
      method: "GET",
      path: "/v1/test",
    });

    assert.equal(got.v, 9);
    assert.equal(f.count, 3);
  });

  // 4xx는 요청 자체가 잘못됐다는 뜻이다. 다시 보내도 같다.
  it("4xx는 재시도하지 않는다", async () => {
    const f = fakeFetch([fail(422, "product_not_allowed")]);

    await assert.rejects(
      () => newTransport(f.impl).request({ method: "POST", path: "/v1/test" }),
      (err: unknown) => {
        assert.ok(err instanceof PlatformError);
        assert.equal(err.code, "product_not_allowed");
        assert.equal(err.status, 422);
        assert.equal(err.local, false);
        return true;
      },
    );
    assert.equal(f.count, 1);
  });

  it("재시도를 다 쓰면 마지막 오류를 던진다", async () => {
    const f = fakeFetch([fail(500, "internal")]);

    await assert.rejects(() =>
      newTransport(f.impl, 2).request({ method: "GET", path: "/v1/test" }),
    );
    // 첫 시도 + 재시도 2회
    assert.equal(f.count, 3);
  });

  // 무한 재시도 루프는 비용 사고의 1순위 원인이다.
  it("noRetry면 한 번만 보낸다", async () => {
    const f = fakeFetch([fail(500, "internal")]);

    await assert.rejects(() =>
      newTransport(f.impl).request({ method: "POST", path: "/v1/test", noRetry: true }),
    );
    assert.equal(f.count, 1);
  });

  it("네트워크 오류는 로컬 코드로 감싼다", async () => {
    const impl = (async () => {
      throw new Error("connection refused");
    }) as unknown as typeof fetch;

    await assert.rejects(
      () => newTransport(impl, 0).request({ method: "GET", path: "/v1/test" }),
      (err: unknown) => {
        assert.ok(err instanceof PlatformError);
        assert.equal(err.code, "network_error");
        assert.equal(err.local, true);
        return true;
      },
    );
  });

  it("query를 URL에 붙인다", async () => {
    const f = fakeFetch([ok({})]);
    await newTransport(f.impl).request({
      method: "GET",
      path: "/v1/config",
      query: { appVersion: "1.0.7", platform: "android", locale: undefined },
    });

    const url = new URL(f.calls[0]!.url);
    assert.equal(url.searchParams.get("appVersion"), "1.0.7");
    assert.equal(url.searchParams.get("platform"), "android");
    // undefined는 빈 문자열로 붙이지 않는다
    assert.equal(url.searchParams.has("locale"), false);
  });
});

describe("SessionManager", () => {
  const sessionBody = {
    platformToken: "pt-1",
    refreshToken: "rt-1",
    platformUserId: "pu_01J",
    supportCode: "TEST-00000000",
    appUserId: "uid-1",
    isAnonymous: false,
    expiresIn: 3600,
  };

  it("로그인하면 세션을 저장한다", async () => {
    const f = fakeFetch([ok(sessionBody)]);
    const store = new MemorySessionStore();
    const sm = new SessionManager(newTransport(f.impl), store, () => 1_000_000);

    const s = await sm.signIn({ kind: "firebase-id-token", value: "id-token" });

    assert.equal(s.platformUserId, "pu_01J");
    assert.equal(s.expiresAt, 1_000_000 + 3600 * 1000);
    assert.deepEqual(await store.load(), s);
  });

  it("유효한 토큰은 그대로 쓴다", async () => {
    const f = fakeFetch([ok(sessionBody)]);
    const sm = new SessionManager(newTransport(f.impl), new MemorySessionStore(), () => 1_000_000);

    await sm.signIn({ kind: "anonymous", value: "anon" });
    const before = f.count;

    assert.equal(await sm.token(), "pt-1");
    // 갱신 호출이 없어야 한다
    assert.equal(f.count, before);
  });

  it("만료가 가까우면 갱신한다", async () => {
    let now = 1_000_000;
    const f = fakeFetch([
      ok(sessionBody),
      ok({ ...sessionBody, platformToken: "pt-2", refreshToken: "rt-2" }),
    ]);
    const sm = new SessionManager(newTransport(f.impl), new MemorySessionStore(), () => now);

    await sm.signIn({ kind: "anonymous", value: "anon" });

    // 만료 30초 전으로 이동. 여유(60초) 안이라 갱신해야 한다
    now += 3600 * 1000 - 30_000;
    assert.equal(await sm.token(), "pt-2");

    assert.equal(f.calls[1]!.url, "https://platform.test/v1/auth/refresh");
    assert.deepEqual(f.calls[1]!.body, { refreshToken: "rt-1" });
  });

  // 갱신이 겹치면 서버가 refresh token을 회전시키는 순간
  // 나머지 요청이 무효 토큰을 들고 실패한다.
  it("동시 갱신은 한 번만 한다", async () => {
    let now = 1_000_000;
    const f = fakeFetch([
      ok(sessionBody),
      ok({ ...sessionBody, platformToken: "pt-2" }),
    ]);
    const sm = new SessionManager(newTransport(f.impl), new MemorySessionStore(), () => now);

    await sm.signIn({ kind: "anonymous", value: "anon" });
    const afterSignIn = f.count;

    now += 3600 * 1000;
    const tokens = await Promise.all([sm.token(), sm.token(), sm.token()]);

    assert.deepEqual(tokens, ["pt-2", "pt-2", "pt-2"]);
    // 갱신 호출은 정확히 한 번
    assert.equal(f.count, afterSignIn + 1);
  });

  it("갱신이 401이면 자격증명으로 다시 로그인한다", async () => {
    let now = 1_000_000;
    const f = fakeFetch([
      ok(sessionBody),
      fail(401, "auth_invalid"),
      ok({ ...sessionBody, platformToken: "pt-3" }),
    ]);
    const sm = new SessionManager(newTransport(f.impl), new MemorySessionStore(), () => now);

    await sm.signIn({ kind: "firebase-id-token", value: "id-token" });
    now += 3600 * 1000;

    assert.equal(await sm.token(), "pt-3");
    // 마지막 호출은 재로그인이다
    assert.equal(f.calls[2]!.url, "https://platform.test/v1/auth/session");
  });

  it("연결 세션은 refresh 실패 때 이전 guest로 자동 강등하지 않는다", async () => {
    let now = 1_000_000;
    const f = fakeFetch([ok(sessionBody), fail(401, "auth_invalid")]);
    const sm = new SessionManager(newTransport(f.impl), new MemorySessionStore(), () => now);
    await sm.signIn({ kind: "firebase-id-token", value: "guest-id-token" });
    await sm.adopt({
      ...sessionBody,
      platformToken: "linked-token",
      refreshToken: "linked-refresh",
      isLinkedAccount: true,
    });
    now += 3600 * 1000;

    await assert.rejects(() => sm.token(), (err: unknown) => {
      assert.ok(err instanceof PlatformError);
      assert.equal(err.code, "auth_invalid");
      return true;
    });
    assert.equal(f.count, 2, "이전 guest credential 재로그인을 시도했다");
  });

  it("세션이 없으면 auth_required를 던진다", async () => {
    const f = fakeFetch([ok({})]);
    const sm = new SessionManager(newTransport(f.impl), new MemorySessionStore());

    await assert.rejects(
      () => sm.token(),
      (err: unknown) => {
        assert.ok(err instanceof PlatformError);
        assert.equal(err.code, "auth_required");
        return true;
      },
    );
  });

  it("로그아웃하면 세션을 지운다", async () => {
    const f = fakeFetch([ok(sessionBody)]);
    const store = new MemorySessionStore();
    const sm = new SessionManager(newTransport(f.impl), store);

    await sm.signIn({ kind: "anonymous", value: "a" });
    await sm.signOut();

    assert.equal(await store.load(), null);
  });
});

describe("Events", () => {
  // 전송 재시도를 끈다. 이벤트는 outbox가 재시도를 담당하므로
  // 여기서 무엇이 재시도했는지 헷갈리지 않게 한다.
  function newEvents(fetchImpl: typeof fetch, outbox = new MemoryEventOutbox()) {
    return {
      events: new Events({
        transport: newTransport(fetchImpl, 0),
        outbox,
        now: () => 1_700_000_000_000,
      }),
      outbox,
    };
  }

  it("파라미터를 정규화해서 보낸다", async () => {
    const f = fakeFetch([ok({})]);
    const { events } = newEvents(f.impl);

    events.track({ name: "level_up", params: { level: 3, is_new: true, email: "a@b.c" } });
    await events.flush();

    const body = f.calls[0]!.body as { events: Array<{ name: string; params: unknown }> };
    assert.equal(body.events[0]!.name, "level_up");
    // boolean은 1/0, PII는 제거
    assert.deepEqual(body.events[0]!.params, { level: 3, is_new: 1 });
  });

  it("전송에 실패하면 outbox에 남긴다", async () => {
    const f = fakeFetch([fail(500, "internal")]);
    const { events, outbox } = newEvents(f.impl);

    events.track({ name: "e1" });
    // 던지지 않는다. 계측이 게임을 막으면 안 된다
    await events.flush();

    assert.equal(await outbox.size(), 1);
  });

  it("다음 flush에서 outbox를 다시 보낸다", async () => {
    const f = fakeFetch([fail(500, "internal"), ok({})]);
    const { events, outbox } = newEvents(f.impl);

    events.track({ name: "e1" });
    await events.flush();
    assert.equal(await outbox.size(), 1);

    await events.flush();
    assert.equal(await outbox.size(), 0);
  });

  it("보낼 것이 없으면 호출하지 않는다", async () => {
    const f = fakeFetch([ok({})]);
    const { events } = newEvents(f.impl);

    await events.flush();
    assert.equal(f.count, 0);
  });

  it("이름이 없는 이벤트는 무시한다", async () => {
    const f = fakeFetch([ok({})]);
    const { events } = newEvents(f.impl);

    events.track({ name: "" });
    await events.flush();

    assert.equal(f.count, 0);
  });

  it("start를 반복해도 세션 시작 이벤트는 한 번만 기록한다", async () => {
    const f = fakeFetch([ok({})]);
    const events = new Events({
      transport: newTransport(f.impl, 0),
      flushIntervalMs: 0,
    });

    events.start();
    events.stop();
    events.start();
    await events.flush();

    const body = f.calls[0]!.body as { events: Array<{ name: string; sessionId: string }> };
    assert.deepEqual(body.events.map((event) => event.name), ["seori_session_start"]);
    assert.ok(body.events[0]!.sessionId.length >= 8);
  });

  it("outbox는 상한을 넘으면 오래된 것을 버린다", async () => {
    const outbox = new MemoryEventOutbox(3);
    await outbox.push([1, 2, 3, 4, 5].map((i) => ({
      eventId: `event-${i}`,
      name: `e${i}`,
      sessionId: "session-1",
      params: {},
      tsUnixMs: i,
    })));

    assert.equal(await outbox.size(), 3);
    const drained = await outbox.drain(10);
    // 뒤의 3개가 남는다
    assert.deepEqual(drained.map((e) => e.name), ["e3", "e4", "e5"]);
  });
});

describe("Platform routing", () => {
  it("API, ingest, IAP를 각각 지정한 호스트로 보낸다", async () => {
    const f = fakeFetch([
      ok({ values: {}, features: {}, sdk: { status: "ok" }, maintenance: { active: false } }),
      ok({ accepted: 2, dropped: 0 }),
    ]);
    const platform = new Platform({
      baseUrl: "https://api.platform.test",
      ingestBaseUrl: "https://ingest.platform.test",
      iapBaseUrl: "https://iap.platform.test",
      appId: "happy-farm",
      fetchImpl: f.impl,
      maxRetries: 0,
      eventFlushIntervalMs: 0,
      eventAllowlist: ["seori_session_start", "game_start"],
      eventContext: {
        platform: "ait",
        appVersion: "1.2.3",
        locale: "ko-KR",
      },
    });

    await platform.config.fetch({ appVersion: "1.2.3", platform: "android" });
    platform.start();
    platform.events.track({ name: "game_start" });
    platform.events.track({ name: "crop_harvested" });
    await platform.events.flush();

    assert.equal(f.calls[0]!.url, "https://api.platform.test/v1/config?appVersion=1.2.3&platform=android");
    assert.equal(f.calls[1]!.url, "https://ingest.platform.test/v1/events");
    const body = f.calls[1]!.body as {
      events: Array<{ name: string; sessionId: string }>;
      context: Record<string, string>;
    };
    assert.deepEqual(body.events.map((event) => event.name), ["seori_session_start", "game_start"]);
    assert.equal(new Set(body.events.map((event) => event.sessionId)).size, 1);
    assert.deepEqual(body.context, {
      platform: "ait",
      appVersion: "1.2.3",
      locale: "ko-KR",
      sdkVersion: SDK_VERSION,
    });
  });

  it("IAP는 별도 호스트를 사용한다", async () => {
    const f = fakeFetch([
      ok({
        platformToken: "pt-1",
        refreshToken: "rt-1",
        platformUserId: "pu_1",
        appUserId: "uid-1",
        isAnonymous: false,
        expiresIn: 3600,
      }),
      ok({ googlePlayObfuscatedAccountId: "gp", appStoreAppAccountToken: "ios" }),
    ]);
    const platform = new Platform({
      baseUrl: "https://api.platform.test",
      ingestBaseUrl: "https://ingest.platform.test",
      iapBaseUrl: "https://iap.platform.test",
      appId: "happy-farm",
      fetchImpl: f.impl,
      maxRetries: 0,
    });

    await platform.signIn({ kind: "firebase-id-token", value: "id-token" });
    await platform.iap.accountReferences();

    assert.equal(f.calls[0]!.url, "https://api.platform.test/v1/auth/session");
    assert.equal(f.calls[1]!.url, "https://iap.platform.test/v1/iap/account-references");
  });

  it("AIT 로그인과 광고는 Ads 호스트를 사용한다", async () => {
    const f = fakeFetch([
      ok({
        platformToken: "pt-1", refreshToken: "rt-1", platformUserId: "pu_1",
        supportCode: "SUPPORT", appUserId: "", isAnonymous: false, expiresIn: 3600,
      }),
      ok({ appUsesAds: true, adsEnabled: true, disabledBy: [], checkedAt: "2026-08-09T00:00:00Z" }),
    ]);
    const platform = new Platform({
      baseUrl: "https://api.platform.test",
      adsBaseUrl: "https://ads.platform.test",
      appId: "happy-farm",
      fetchImpl: f.impl,
      maxRetries: 0,
    });

    await platform.signIn({ kind: "ait-login", value: "authorization-code", referrer: "SANDBOX" });
    await platform.ads.policy();

    assert.equal(f.calls[0]!.url, "https://ads.platform.test/v1/auth/session");
    assert.equal(f.calls[1]!.url, "https://ads.platform.test/v1/ads/policy");
    assert.deepEqual(f.calls[0]!.body, {
      credential: { kind: "ait-login", value: "authorization-code", referrer: "SANDBOX" },
    });
  });

  it("별도 호스트가 없으면 baseUrl로 하위 호환된다", async () => {
    const f = fakeFetch([ok({ accepted: 1, dropped: 0 })]);
    const platform = new Platform({
      baseUrl: "https://platform.test",
      appId: "happy-farm",
      fetchImpl: f.impl,
      maxRetries: 0,
      eventFlushIntervalMs: 0,
    });

    platform.start();
    await platform.events.flush();

    assert.equal(f.calls[0]!.url, "https://platform.test/v1/events");
  });
});

describe("Ads", () => {
  function newAds(fetchImpl: typeof fetch) {
    return new Ads(newTransport(fetchImpl), async () => "tok-1");
  }

  it("정책 오류를 허용 상태로 바꾸지 않는다", async () => {
    const f = fakeFetch([fail(503, "platform_unavailable")]);
    await assert.rejects(() => newAds(f.impl).policy());
    assert.equal(f.count, 4);
  });

  it("claim 생성은 자동 재시도하지 않는다", async () => {
    const f = fakeFetch([fail(503, "platform_unavailable")]);
    await assert.rejects(() => newAds(f.impl).createClaim({
      requestId: "req-1",
      placement: "harvest_boost",
      provider: "admob",
      clientPlatform: "android",
      reward: { key: "harvest_boost", amount: 1 },
    }));
    assert.equal(f.count, 1);
  });

  it("confirm과 ack를 서로 다른 상태 전이로 보낸다", async () => {
    const claim = {
      claimId: "cl_1", appId: "happy-farm", placement: "harvest_boost",
      provider: "apps_in_toss", clientPlatform: "apps_in_toss",
      reward: { key: "harvest_boost", amount: 1 }, state: "confirmed",
      assurance: "client_confirmed", createdAt: "now", expiresAt: "later",
    };
    const f = fakeFetch([ok(claim), ok({ ...claim, state: "delivered" })]);
    const ads = newAds(f.impl);
    await ads.confirm("cl_1", "tx-1");
    await ads.ack("cl_1");
    assert.equal(f.calls[0]!.url, "https://platform.test/v1/ads/reward-claims/cl_1/confirm");
    assert.deepEqual(f.calls[0]!.body, { transactionId: "tx-1" });
    assert.equal(f.calls[1]!.url, "https://platform.test/v1/ads/reward-claims/cl_1/ack");
  });
});

describe("Iap", () => {
  function newIap(fetchImpl: typeof fetch) {
    return new Iap({
      transport: newTransport(fetchImpl),
      getToken: async () => "tok-1",
    });
  }

  it("검증 요청을 보낸다", async () => {
    const f = fakeFetch([
      ok({ status: "verified", entitlementId: "sp_a", granted: true, entitlements: ["sp_a"] }),
    ]);

    const got = await newIap(f.impl).verifyPurchase({
      platform: "google_play",
      productId: "gecko_galaxy",
      token: "purchase-token",
    });

    assert.equal(got.status, "verified");
    assert.equal(got.granted, true);
    assert.deepEqual(f.calls[0]!.body, {
      platform: "google_play",
      productId: "gecko_galaxy",
      token: "purchase-token",
    });
  });

  // 결제 경로는 자동 재시도하지 않는다.
  it("검증은 재시도하지 않는다", async () => {
    const f = fakeFetch([fail(503, "provider_unavailable")]);

    await assert.rejects(() =>
      newIap(f.impl).verifyPurchase({
        platform: "google_play",
        productId: "p",
        token: "t",
      }),
    );
    assert.equal(f.count, 1);
  });

  it("증명이 비면 네트워크를 타지 않는다", async () => {
    const f = fakeFetch([ok({})]);

    await assert.rejects(
      () => newIap(f.impl).verifyPurchase({ platform: "google_play", productId: "", token: "" }),
      (err: unknown) => {
        assert.ok(err instanceof PlatformError);
        assert.equal(err.code, "purchase_proof_invalid");
        assert.equal(err.local, true);
        return true;
      },
    );
    assert.equal(f.count, 0);
  });

  it("entitlement 목록을 읽는다", async () => {
    const f = fakeFetch([ok({ entitlements: ["sp_a", "sp_b"] })]);
    assert.deepEqual(await newIap(f.impl).listEntitlements(), ["sp_a", "sp_b"]);
  });

  it("목록이 없으면 빈 배열", async () => {
    const f = fakeFetch([ok({})]);
    assert.deepEqual(await newIap(f.impl).listEntitlements(), []);
  });
});

describe("Config", () => {
  const configBody = {
    values: { max_energy: 10 },
    features: { new_shop: true },
    sdk: { status: "ok" },
    maintenance: { active: false },
  };
  const target = { appVersion: "1.0.7", platform: "android" } as const;

  it("설정을 가져온다", async () => {
    const f = fakeFetch([ok(configBody)]);
    const c = new Config({ transport: newTransport(f.impl), now: () => 1_000 });

    const got = await c.fetch(target);
    assert.equal(got.features["new_shop"], true);
  });

  it("TTL 안에서는 캐시를 쓴다", async () => {
    let now = 1_000;
    const f = fakeFetch([ok(configBody)]);
    const c = new Config({ transport: newTransport(f.impl), ttlMs: 60_000, now: () => now });

    await c.fetch(target);
    now += 30_000;
    await c.fetch(target);

    assert.equal(f.count, 1);
  });

  it("TTL이 지나면 다시 가져온다", async () => {
    let now = 1_000;
    const f = fakeFetch([ok(configBody), ok({ ...configBody, values: { max_energy: 20 } })]);
    const c = new Config({ transport: newTransport(f.impl), ttlMs: 60_000, now: () => now });

    await c.fetch(target);
    now += 61_000;
    const got = await c.fetch(target);

    assert.equal(got.values["max_energy"], 20);
    assert.equal(f.count, 2);
  });

  // 설정을 못 읽었다고 앱을 막으면 서버 장애가 전체 중단으로 번진다.
  it("실패하면 열린 기본값을 준다", async () => {
    const f = fakeFetch([fail(503, "unavailable")]);
    const c = new Config({ transport: newTransport(f.impl), now: () => 1_000 });

    const got = await c.fetch(target);

    assert.equal(got.maintenance.active, false);
    assert.equal(got.sdk.status, "ok");
  });

  it("실패하면 마지막 캐시를 유지한다", async () => {
    let now = 1_000;
    const f = fakeFetch([ok(configBody), fail(503, "unavailable")]);
    const c = new Config({ transport: newTransport(f.impl), ttlMs: 1_000, now: () => now });

    await c.fetch(target);
    now += 2_000;
    const got = await c.fetch(target);

    assert.equal(got.values["max_energy"], 10);
  });

  it("세션 응답으로 캐시를 채운다", async () => {
    const f = fakeFetch([ok({})]);
    const c = new Config({ transport: newTransport(f.impl), ttlMs: 60_000, now: () => 1_000 });

    c.seed(configBody as never);
    await c.fetch(target);

    // seed가 캐시를 채웠으니 네트워크를 타지 않는다
    assert.equal(f.count, 0);
  });
});

describe("서버 계약", () => {
  // 이 테스트가 없어서 두 가지를 놓쳤다. SDK가 clientTimestamp를 보내
  // 배치 전체가 400으로 떨어졌고, eventId가 없어 서버가 200을 주면서도
  // 이벤트를 버렸다. 후자는 SDK가 성공으로 알고 outbox에서 지워
  // 조용히 유실됐다.
  //
  // 서버의 clientEvent 구조(internal/events/handler.go)와 맞춘다.
  const SERVER_EVENT_FIELDS = ["eventId", "name", "tsUnixMs", "sessionId", "params"];

  it("이벤트가 서버가 아는 필드만 보낸다", async () => {
    const f = fakeFetch([ok({})]);
    const events = new Events({
      transport: newTransport(f.impl, 0),
      now: () => 1_700_000_000_000,
    });

    events.track({ name: "seori_session_start", params: { level: 3 } });
    await events.flush();

    const body = f.calls[0]!.body as { events: Array<Record<string, unknown>> };
    const sent = body.events[0]!;

    for (const key of Object.keys(sent)) {
      assert.ok(
        SERVER_EVENT_FIELDS.includes(key),
        `서버가 모르는 필드를 보낸다: ${key} — DecodeStrict가 400을 준다`,
      );
    }
  });

  it("eventId를 반드시 채운다", async () => {
    const f = fakeFetch([ok({})]);
    const events = new Events({
      transport: newTransport(f.impl, 0),
      now: () => 1_700_000_000_000,
    });

    events.track({ name: "seori_session_start" });
    events.track({ name: "seori_sdk_error" });
    await events.flush();

    const body = f.calls[0]!.body as { events: Array<{ eventId?: string }> };

    const ids = body.events.map((e) => e.eventId);
    for (const id of ids) {
      // 비면 서버가 200을 주면서 그 이벤트만 버린다
      assert.ok(id && id.length >= 8, `eventId가 비었다: ${id}`);
    }
    // 같은 배치에서 충돌하면 서버가 중복으로 보고 하나를 버린다
    assert.equal(new Set(ids).size, ids.length, "eventId가 중복이다");
  });

  it("tsUnixMs를 밀리초 정수로 보낸다", async () => {
    const f = fakeFetch([ok({})]);
    const events = new Events({
      transport: newTransport(f.impl, 0),
      now: () => 1_700_000_000_123,
    });

    events.track({ name: "seori_session_start" });
    await events.flush();

    const body = f.calls[0]!.body as { events: Array<{ tsUnixMs?: number }> };
    const ts = body.events[0]!.tsUnixMs!;

    assert.equal(Number.isInteger(ts), true, "정수가 아니다");
    assert.equal(ts, 1_700_000_000_123);
  });
});

describe("Content와 Identity", () => {
  it("세션과 App Check로 콘텐츠 버전을 조회한다", async () => {
    const f = fakeFetch([
      ok({
        platformToken: "pt-content", refreshToken: "rt-content",
        platformUserId: "pu-content", appUserId: "uid-content",
        isAnonymous: false, expiresIn: 3600,
      }),
      ok({ schemaVersion: 1, contentVersion: `sha256-${"a".repeat(64)}` }),
    ]);
    const platform = new Platform({
      baseUrl: "https://platform.test", appId: "ungeul", fetchImpl: f.impl,
      appCheckToken: async () => "attested-content",
    });
    await platform.signIn({ kind: "firebase-id-token", value: "firebase-id-token" });
    const version = await platform.content.version();

    assert.equal(version.schemaVersion, 1);
    assert.equal(f.calls[1]!.url, "https://platform.test/v1/content/version");
    assert.equal(f.calls[1]!.headers.Authorization, "Bearer pt-content");
    assert.equal(f.calls[1]!.headers["X-Firebase-AppCheck"], "attested-content");
  });

  it("Firebase custom token bridge는 기존 ID token을 선택적으로 보낸다", async () => {
    const f = fakeFetch([ok({ firebaseCustomToken: "custom", appUserId: "uid" })]);
    const platform = new Platform({
      baseUrl: "https://platform.test", appId: "ungeul", fetchImpl: f.impl,
      appCheckToken: async () => "attested-content",
    });
    const got = await platform.identity.firebaseCustomToken("existing-id-token");

    assert.equal(got.firebaseCustomToken, "custom");
    assert.deepEqual(f.calls[0]!.body, {
      appId: "ungeul", existingFirebaseIdToken: "existing-id-token",
    });
  });

  it("계정 연결은 현재 세션과 App Check를 쓰고 linked 세션으로 교체한다", async () => {
    const f = fakeFetch([
      ok({
        platformToken: "guest-token", refreshToken: "guest-refresh",
        platformUserId: "pu-guest", supportCode: "UG-GUEST",
        appUserId: "guest-uid", isAnonymous: true, isLinkedAccount: false, expiresIn: 3600,
      }),
      ok({ provider: "kakao", nonce: "server-nonce", expiresAt: "2026-08-23T01:07:03Z" }),
      ok({
        firebaseCustomToken: "firebase-custom", provider: "kakao", restored: true,
        session: {
          platformToken: "linked-token", refreshToken: "linked-refresh",
          platformUserId: "pu-existing", supportCode: "UG-EXISTING",
          appUserId: "existing-uid", isAnonymous: false, isLinkedAccount: true, expiresIn: 3600,
        },
      }),
    ]);
    const platform = new Platform({
      baseUrl: "https://platform.test", appId: "ungeul", fetchImpl: f.impl,
      appCheckToken: async () => "attested-content",
    });
    await platform.signIn({ kind: "firebase-id-token", value: "guest-id-token" });
    const challenge = await platform.identity.beginAccountLink("kakao");
    const linked = await platform.identity.completeAccountLink(
      "kakao", "kakao-id-token", challenge.nonce,
    );

    assert.equal(f.calls[1]!.headers.Authorization, "Bearer guest-token");
    assert.equal(f.calls[2]!.headers.Authorization, "Bearer guest-token");
    assert.equal(f.calls[2]!.headers["X-Firebase-AppCheck"], "attested-content");
    assert.deepEqual(f.calls[2]!.body, {
      provider: "kakao", idToken: "kakao-id-token", nonce: "server-nonce",
    });
    assert.equal(linked.restored, true);
    assert.equal(linked.session.isLinkedAccount, true);
    assert.equal((await platform.session.current())?.platformToken, "linked-token");
  });
});
