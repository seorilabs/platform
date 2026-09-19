import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { gateView, mountUpdateGate } from "../src/gate-dom.ts";

describe("게이트 화면 구성", () => {
  it("정상이면 아무것도 그리지 않는다", () => {
    assert.equal(gateView({ kind: "ok" }), null);
  });

  // 강제에 닫기 수단을 만들면 강제가 아니다.
  it("강제는 닫을 수 없고 권장은 닫을 수 있다", () => {
    const required = gateView({
      kind: "required",
      message: "업데이트가 필요해요",
      updateUrl: "https://play.google.com/store/apps/details?id=com.a.b",
    });
    assert.equal(required?.dismissible, false);

    const recommended = gateView({ kind: "recommended", message: "새 버전이 나왔어요" });
    assert.equal(recommended?.dismissible, true);
  });

  // 눌러도 아무 일 없는 버튼을 만들면 유저가 갇힌 것으로 느낀다.
  it("스토어 주소가 없으면 업데이트 버튼을 그리지 않는다", () => {
    const view = gateView({ kind: "required", message: "업데이트가 필요해요" });
    assert.equal(view?.updateUrl, undefined);
  });

  // 점검은 스토어에 가도 해결되지 않는다.
  it("점검은 닫을 수 없고 업데이트 대상도 없다", () => {
    const view = gateView({ kind: "maintenance", message: "점검 중" });
    assert.equal(view?.dismissible, false);
    assert.equal(view?.updateUrl, undefined);
  });
});

/** Node에는 DOM이 없다. mount가 만드는 노드 구조만 확인할 최소 스텁이다. */
function stubDocument() {
  interface Node {
    tag: string;
    children: Node[];
    attrs: Record<string, string>;
    style: { cssText: string };
    text: string;
    type?: string;
    parentNode: Node | null;
    listeners: Record<string, Array<() => void>>;
    setAttribute(name: string, value: string): void;
    appendChild(child: Node): Node;
    removeChild(child: Node): Node;
    addEventListener(name: string, fn: () => void): void;
    click(): void;
    set textContent(value: string);
    get textContent(): string;
  }

  const create = (tag: string): Node => {
    const node: Node = {
      tag,
      children: [],
      attrs: {},
      style: { cssText: "" },
      text: "",
      parentNode: null,
      listeners: {},
      setAttribute(name, value) {
        node.attrs[name] = value;
      },
      appendChild(child) {
        child.parentNode = node;
        node.children.push(child);
        return child;
      },
      removeChild(child) {
        node.children = node.children.filter((c) => c !== child);
        child.parentNode = null;
        return child;
      },
      addEventListener(name, fn) {
        (node.listeners[name] ??= []).push(fn);
      },
      click() {
        for (const fn of node.listeners["click"] ?? []) fn();
      },
      set textContent(value: string) {
        node.text = value;
      },
      get textContent() {
        return node.text;
      },
    };
    return node;
  };

  const body = create("body");
  return { doc: { createElement: create, body }, body, create };
}

function buttons(node: { tag: string; children: unknown[] }): Array<{ text: string; click(): void }> {
  const out: Array<{ text: string; click(): void }> = [];
  const walk = (n: { tag: string; children: unknown[] }) => {
    if (n.tag === "button") out.push(n as never);
    for (const child of n.children) walk(child as never);
  };
  walk(node);
  return out;
}

describe("게이트 mount", () => {
  it("강제 화면에는 닫기 버튼이 없다", () => {
    const { doc, body } = stubDocument();

    mountUpdateGate({
      state: {
        kind: "required",
        message: "업데이트가 필요해요",
        updateUrl: "https://play.google.com/store/apps/details?id=com.a.b",
      },
      documentImpl: doc as never,
    });

    const labels = buttons(body as never).map((b) => b.text);
    assert.deepEqual(labels, ["업데이트하기"]);
  });

  it("권장 화면의 나중에 버튼이 화면을 내린다", () => {
    const { doc, body } = stubDocument();
    let later = 0;

    mountUpdateGate({
      state: { kind: "recommended", message: "새 버전이 나왔어요" },
      documentImpl: doc as never,
      onLater: () => {
        later += 1;
      },
    });
    assert.equal(body.children.length, 1);

    buttons(body as never).find((b) => b.text === "나중에")?.click();

    assert.equal(later, 1);
    assert.equal(body.children.length, 0);
  });

  it("업데이트 버튼이 스토어 주소를 넘긴다", () => {
    const { doc, body } = stubDocument();
    const opened: string[] = [];

    mountUpdateGate({
      state: {
        kind: "required",
        message: "업데이트가 필요해요",
        updateUrl: "https://apps.apple.com/app/id1234567890",
      },
      documentImpl: doc as never,
      onUpdate: (url) => opened.push(url),
    });

    buttons(body as never)[0]?.click();

    assert.deepEqual(opened, ["https://apps.apple.com/app/id1234567890"]);
  });

  it("정상이면 아무것도 붙이지 않고 no-op을 준다", () => {
    const { doc, body } = stubDocument();

    const unmount = mountUpdateGate({ state: { kind: "ok" }, documentImpl: doc as never });
    unmount();

    assert.equal(body.children.length, 0);
  });
});
