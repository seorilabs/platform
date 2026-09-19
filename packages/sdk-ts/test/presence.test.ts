import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { Presence } from "../src/presence.ts";
import type { RequestOptions } from "../src/transport.ts";

class FakeTokenTransport {
  calls: RequestOptions[] = [];
  async request<T>(options: RequestOptions): Promise<T> {
    this.calls.push(options);
    return {
      enabled: true,
      token: "edge-token",
      expiresAt: "2026-08-26T13:00:00Z",
      expiresIn: 3600,
      edgeUrl: "https://edge.vzyx.xyz",
      heartbeatIntervalSeconds: 60,
    } as T;
  }
}

describe("Presence", () => {
  it("start가 네트워크를 기다리지 않고 heartbeat를 outbox 없이 한 번 보낸다", async () => {
    const transport = new FakeTokenTransport();
    const edgeCalls: Array<{ url: string; body: unknown }> = [];
    let finish: (() => void) | undefined;
    const finished = new Promise<void>((resolve) => { finish = resolve; });
    const fetchImpl = (async (url: string | URL, init?: RequestInit) => {
      edgeCalls.push({ url: String(url), body: JSON.parse(String(init?.body)) });
      finish?.();
      return new Response(JSON.stringify({ ok: true, result: { acceptedAt: "2026-08-26T12:00:00Z" } }), {
        status: 200,
      });
    }) as typeof fetch;
    const timers: number[] = [];
    const presence = new Presence({
      enabled: true,
      tokenTransport: transport,
      context: { platform: "ait", appVersion: "1.2.0" },
      fetchImpl,
      now: () => Date.parse("2026-08-26T12:00:00Z"),
      random: () => 0.5,
      setTimer: ((_callback: () => void, delay: number) => {
        timers.push(delay);
        return { unref() {} } as unknown as ReturnType<typeof setTimeout>;
      }),
      clearTimer: () => {},
    });

    presence.start();
    assert.equal(edgeCalls.length, 0, "start가 동기 네트워크 완료를 기다렸다");
    await finished;
    await new Promise<void>((resolve) => setImmediate(resolve));

    assert.equal(transport.calls.length, 1);
    assert.equal(transport.calls[0]?.noRetry, true);
    assert.equal(edgeCalls.length, 1);
    assert.equal(edgeCalls[0]?.url, "https://edge.vzyx.xyz/v1/presence/heartbeat");
    assert.deepEqual(edgeCalls[0]?.body, {
      version: 1,
      sequence: 0,
      platform: "ait",
      appVersion: "1.2.0",
    });
    assert.deepEqual(timers, [60_000]);
    presence.stop();
  });

  it("Edge timeout을 호출자에게 던지지 않고 다음 새 heartbeat를 backoff한다", async () => {
    const transport = new FakeTokenTransport();
    const timers: number[] = [];
    const fetchImpl = ((_: string | URL, init?: RequestInit) => new Promise<Response>((_, reject) => {
      init?.signal?.addEventListener("abort", () => reject(new Error("aborted")), { once: true });
    })) as typeof fetch;
    const presence = new Presence({
      enabled: true,
      tokenTransport: transport,
      context: { platform: "web" },
      fetchImpl,
      timeoutMs: 5,
      now: () => Date.parse("2026-08-26T12:00:00Z"),
      random: () => 0.5,
      setTimer: ((_callback: () => void, delay: number) => {
        timers.push(delay);
        return { unref() {} } as unknown as ReturnType<typeof setTimeout>;
      }),
      clearTimer: () => {},
    });

    assert.doesNotThrow(() => presence.start());
    await new Promise((resolve) => setTimeout(resolve, 20));
    assert.deepEqual(timers, [60_000]);
    presence.stop();
  });

  it("비활성 상태에서는 token과 Edge를 호출하지 않는다", () => {
    const transport = new FakeTokenTransport();
    let edgeCalls = 0;
    const presence = new Presence({
      enabled: false,
      tokenTransport: transport,
      context: { platform: "android" },
      fetchImpl: (async () => {
        edgeCalls++;
        return new Response();
      }) as typeof fetch,
    });
    presence.start();
    assert.equal(transport.calls.length, 0);
    assert.equal(edgeCalls, 0);
  });

  it("서버가 presence를 비활성화하면 token 확인도 5분 뒤로 미룬다", async () => {
    const transport = new FakeTokenTransport();
    transport.request = async <T>(options: RequestOptions) => {
      transport.calls.push(options);
      return {
        enabled: false,
        heartbeatIntervalSeconds: 60,
      } as T;
    };
    const timers: number[] = [];
    let edgeCalls = 0;
    const presence = new Presence({
      enabled: true,
      tokenTransport: transport,
      context: { platform: "android" },
      fetchImpl: (async () => {
        edgeCalls++;
        return new Response();
      }) as typeof fetch,
      random: () => 0.5,
      setTimer: ((_callback: () => void, delay: number) => {
        timers.push(delay);
        return { unref() {} } as unknown as ReturnType<typeof setTimeout>;
      }),
      clearTimer: () => {},
    });

    presence.start();
    await new Promise<void>((resolve) => setImmediate(resolve));

    assert.equal(transport.calls.length, 1);
    assert.equal(edgeCalls, 0);
    assert.deepEqual(timers, [300_000]);
    presence.stop();
  });

  it("잘못된 heartbeat 간격이 와도 즉시 재호출 루프를 만들지 않는다", async () => {
    const transport = new FakeTokenTransport();
    transport.request = async <T>(options: RequestOptions) => {
      transport.calls.push(options);
      return {
        enabled: true,
        token: "edge-token",
        expiresIn: 3600,
        edgeUrl: "https://edge.vzyx.xyz",
      } as T;
    };
    const timers: number[] = [];
    const presence = new Presence({
      enabled: true,
      tokenTransport: transport,
      context: { platform: "web" },
      fetchImpl: (async () => new Response(null, { status: 200 })) as typeof fetch,
      random: () => 0.5,
      setTimer: ((_callback: () => void, delay: number) => {
        timers.push(delay);
        return { unref() {} } as unknown as ReturnType<typeof setTimeout>;
      }),
      clearTimer: () => {},
    });

    presence.start();
    await new Promise<void>((resolve) => setImmediate(resolve));

    assert.deepEqual(timers, [60_000]);
    presence.stop();
  });

  it("연속 실패의 새 heartbeat를 60초, 120초, 최대 300초로 늦춘다", async () => {
    const transport = new FakeTokenTransport();
    const scheduled: Array<{ callback: () => void; delay: number }> = [];
    const presence = new Presence({
      enabled: true,
      tokenTransport: transport,
      context: { platform: "ait" },
      fetchImpl: (async () => new Response(null, { status: 503 })) as typeof fetch,
      random: () => 0.5,
      setTimer: ((callback: () => void, delay: number) => {
        scheduled.push({ callback, delay });
        return { unref() {} } as unknown as ReturnType<typeof setTimeout>;
      }),
      clearTimer: () => {},
    });

    presence.start();
    await new Promise<void>((resolve) => setImmediate(resolve));
    scheduled[0]?.callback();
    await new Promise<void>((resolve) => setImmediate(resolve));
    scheduled[1]?.callback();
    await new Promise<void>((resolve) => setImmediate(resolve));

    assert.deepEqual(scheduled.map((entry) => entry.delay), [60_000, 120_000, 300_000]);
    presence.stop();
  });
});
