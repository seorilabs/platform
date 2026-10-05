import { test } from "node:test";
import assert from "node:assert/strict";
import {
  InterstitialController,
  normalizeInterstitialEvent,
} from "../src/interstitial.ts";
import type {
  InterstitialState,
  InterstitialEvent,
  CompletionOpportunity,
} from "../src/interstitial.ts";
const policy = {
  version: "test",
  enabled: true,
  cooldown_s: 90,
  session_cap: 6,
  daily_cap: 20,
  session_reset_s: 1800,
  completions_between: 2,
};
function fixture(initial: InterstitialState | null = null) {
  let now = Date.UTC(2026, 9, 5),
    state = initial,
    ready = true,
    failed = false,
    shown = 0,
    next = 0;
  let emit: (e: InterstitialEvent) => void = () => {};
  const records: { name: string; params: Record<string, string | number> }[] =
    [];
  const events: string[] = [],
    suspensions: boolean[] = [];
  const store = {
    read: () => state,
    write: (s: InterstitialState) => {
      if (failed) throw Error("disk");
      state = structuredClone(s);
    },
  };
  const controller = new InterstitialController({
    policy,
    store,
    now: () => now,
    adapter: {
      ready: () => ready,
      show: (e) => {
        shown++;
        emit = e;
        return () => {};
      },
    },
    track: (n, p) => {
      events.push(n);
      records.push({ name: n, params: p });
    },
    suspended: (v) => suspensions.push(v),
  });
  let seq = 0;
  return {
    controller,
    store,
    events,
    records,
    suspensions,
    op: (extra: Partial<CompletionOpportunity> = {}) =>
      controller.opportunity(
        {
          id: String(++seq),
          placement: "play",
          saved: true,
          confirmed: true,
          tutorialComplete: true,
          adRemoved: false,
          ...extra,
        },
        () => next++,
      ),
    emit: (e: InterstitialEvent) => emit(e),
    advance: (ms: number) => (now += ms),
    setReady: (v: boolean) => (ready = v),
    setFailed: () => (failed = true),
    get callback() {
      return emit;
    },
    get state() {
      return state!;
    },
    get shown() {
      return shown;
    },
    get next() {
      return next;
    },
  };
}
test("tutorial/save/confirmation/removal and not-ready fail open with no replay", () => {
  const f = fixture();
  for (const extra of [
    { tutorialComplete: false },
    { saved: false },
    { confirmed: false },
    { adRemoved: true },
  ])
    f.op(extra);
  f.setReady(false);
  f.op();
  f.setReady(true);
  assert.equal(f.shown, 0);
  assert.equal(f.next, 5);
  f.op();
  assert.equal(f.shown, 1);
});
test("impression exactly once, two completions, cooldown from close, duplicate/late callbacks", () => {
  const f = fixture();
  f.op({ id: "first" });
  f.emit("show");
  f.emit("impression");
  f.emit("impression");
  assert.equal(f.state.daily, 1);
  const late = f.callback;
  f.op();
  assert.equal(f.next, 1);
  f.emit("dismissed");
  f.emit("dismissed");
  assert.equal(f.next, 2);
  f.op({ id: "first" });
  f.op();
  f.op();
  assert.equal(f.shown, 1);
  f.advance(89999);
  f.op();
  assert.equal(f.shown, 1);
  f.advance(1);
  f.op();
  assert.equal(f.shown, 2);
  late("impression");
  assert.equal(f.state.daily, 1);
  f.emit("impression");
  assert.equal(f.state.daily, 2);
  f.emit("error");
  assert.equal(f.next, 7);
});
test("session cap aggregated across placements, restart, inactivity and daily cap", () => {
  const f = fixture();
  for (let i = 0; i < 6; i++) {
    f.op({ placement: i % 2 ? "craft" : "play" });
    f.emit("impression");
    f.emit("dismissed");
    f.advance(90000);
    f.op();
  }
  assert.equal(f.state.session, 6);
  f.op();
  assert.equal(f.shown, 6);
  const restarted = fixture(f.state);
  restarted.op();
  assert.equal(restarted.shown, 0);
  f.advance(1800000);
  f.op();
  assert.equal(f.shown, 7);
  const cap = fixture({
    ...f.state,
    session: 0,
    daily: 20,
    pending: null,
    lastClosed: 0,
  });
  cap.op({ id: "cap1" });
  assert.equal(cap.shown, 0);
  cap.advance(86400000);
  cap.op({ id: "cap2" });
  assert.equal(cap.shown, 1);
});
test("ad external navigation does not reset session; finish in background stays suspended", () => {
  const f = fixture();
  f.op();
  f.emit("impression");
  f.controller.setForeground(false);
  f.advance(3600000);
  f.controller.setForeground(true);
  f.emit("dismissed");
  assert.equal(f.state.session, 1);
  f.advance(90000);
  f.op();
  f.op();
  f.emit("show");
  f.controller.setForeground(false);
  f.emit("dismissed");
  assert.equal(f.suspensions.at(-1), true);
});
test("persistent write failure skips; failed show does not count; interrupted reservation is conservative", () => {
  const f = fixture();
  f.setFailed();
  f.op();
  assert.equal(f.shown, 0);
  assert.equal(f.next, 1);
  const err = fixture();
  err.op();
  err.emit("failedToShow");
  assert.equal(err.state.daily, 0);
  assert.equal(err.next, 1);
  err.op();
  const recovery = fixture(err.state);
  assert.equal(recovery.state.daily, 0);
  assert.equal(recovery.state.reservedDaily, 1);
  recovery.op();
  assert.equal(recovery.shown, 0);
});
test("event normalization excludes reward and unknown events", () => {
  assert.equal(normalizeInterstitialEvent("impression"), "impression");
  assert.equal(normalizeInterstitialEvent("userEarnedReward"), null);
});

test("actual visible ad duration excludes external background time", () => {
  const f = fixture();
  f.op();
  f.emit("show");
  f.advance(1000);
  f.emit("clicked");
  f.controller.setForeground(false);
  f.advance(60000);
  f.controller.setForeground(true);
  f.advance(2000);
  f.emit("dismissed");
  assert.equal(
    f.records.find((r) => r.name === "ad_duration")?.params.duration_ms,
    3000,
  );
  assert.equal(
    f.records.find((r) => r.name === "ad_duration")?.params.elapsed_ms,
    63000,
  );
  assert.ok(f.events.includes("ad_clicked"));
});
