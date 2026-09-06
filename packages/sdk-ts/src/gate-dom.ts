/**
 * 업데이트 게이트 기본 화면.
 *
 * **`index.ts`에서 export하지 않는다.** 이 패키지는 AppsInToss React Native에서도
 * 쓰이고, 진입점이 DOM 코드를 import하면 RN 번들러가 import 시점에 깨진다.
 * 웹과 AIT WebView는 `@seorilabs/platform-sdk/gate-dom`을 직접 import하고,
 * RN 앱은 `updateGateState()`가 준 결정으로 자기 TDS 화면을 그린다.
 */

import type { UpdateGateState } from "./config.ts";

export interface GateLabels {
  update: string;
  later: string;
}

const DEFAULT_LABELS: GateLabels = {
  update: "업데이트하기",
  later: "나중에",
};

/** 화면에 무엇을 그릴지. DOM 없이 판정만 한다. */
export interface GateView {
  message: string;
  /** 업데이트 버튼을 누르면 열 주소. 없으면 버튼을 그리지 않는다. */
  updateUrl?: string;
  /** 닫을 수 있는지. 강제와 점검은 닫히지 않는다. */
  dismissible: boolean;
}

/**
 * 게이트 상태를 화면 구성으로 옮긴다.
 *
 * `updateUrl`이 없으면 업데이트 버튼을 그리지 않는다. 눌러도 아무 일 없는
 * 버튼을 만들면 유저가 갇힌 것으로 느낀다.
 *
 * 점검은 닫히지 않지만 업데이트할 대상도 없다. 스토어에 가도 해결되지 않는
 * 문제이므로 버튼을 그리지 않는다.
 */
export function gateView(state: UpdateGateState): GateView | null {
  switch (state.kind) {
    case "ok":
      return null;
    case "recommended":
      return {
        message: state.message,
        ...(state.updateUrl ? { updateUrl: state.updateUrl } : {}),
        dismissible: true,
      };
    case "required":
      return {
        message: state.message,
        ...(state.updateUrl ? { updateUrl: state.updateUrl } : {}),
        dismissible: false,
      };
    case "maintenance":
      return { message: state.message, dismissible: false };
  }
}

export interface MountUpdateGateOptions {
  state: UpdateGateState;
  /** 생략하면 document.body에 붙인다. */
  container?: HTMLElement;
  /** 생략하면 새 탭으로 연다. */
  onUpdate?: (url: string) => void;
  onLater?: () => void;
  labels?: Partial<GateLabels>;
  /** 테스트 주입용. */
  documentImpl?: Document;
}

/**
 * 기본 게이트 화면을 띄운다.
 *
 * 돌려받은 함수를 부르면 내린다. `ok`면 아무것도 그리지 않고 no-op을 준다.
 */
export function mountUpdateGate(opts: MountUpdateGateOptions): () => void {
  const view = gateView(opts.state);
  if (!view) return () => {};

  const doc = opts.documentImpl ?? (globalThis as { document?: Document }).document;
  if (!doc) return () => {};

  const labels = { ...DEFAULT_LABELS, ...opts.labels };
  const root = doc.createElement("div");
  root.setAttribute("role", "dialog");
  root.setAttribute("aria-modal", "true");
  root.setAttribute("data-seori-update-gate", opts.state.kind);
  root.style.cssText = [
    "position:fixed",
    "inset:0",
    "z-index:2147483647",
    "display:flex",
    "align-items:center",
    "justify-content:center",
    "padding:24px",
    "background:rgba(0,0,0,0.55)",
    "font:14px/1.5 -apple-system,BlinkMacSystemFont,'Apple SD Gothic Neo',sans-serif",
  ].join(";");

  const panel = doc.createElement("div");
  panel.style.cssText = [
    "max-width:320px",
    "width:100%",
    "border-radius:16px",
    "background:#fff",
    "color:#191f28",
    "padding:24px 20px 16px",
    "text-align:center",
    "box-shadow:0 12px 32px rgba(0,0,0,0.24)",
  ].join(";");

  const message = doc.createElement("p");
  message.textContent = view.message;
  message.style.cssText = "margin:0 0 20px;white-space:pre-line";
  panel.appendChild(message);

  const remove = () => {
    if (root.parentNode) root.parentNode.removeChild(root);
  };

  if (view.updateUrl) {
    const url = view.updateUrl;
    const button = doc.createElement("button");
    button.type = "button";
    button.textContent = labels.update;
    button.style.cssText = [
      "width:100%",
      "border:0",
      "border-radius:12px",
      "padding:14px",
      "font-size:15px",
      "font-weight:600",
      "color:#fff",
      "background:#3182f6",
      "cursor:pointer",
    ].join(";");
    button.addEventListener("click", () => {
      if (opts.onUpdate) {
        opts.onUpdate(url);
        return;
      }
      (globalThis as { open?: (u: string, t?: string) => unknown }).open?.(url, "_blank");
    });
    panel.appendChild(button);
  }

  // 강제와 점검에는 닫기 수단을 만들지 않는다. 만들면 강제가 아니다.
  if (view.dismissible) {
    const later = doc.createElement("button");
    later.type = "button";
    later.textContent = labels.later;
    later.style.cssText = [
      "width:100%",
      "border:0",
      "background:transparent",
      "padding:12px",
      "margin-top:4px",
      "font-size:14px",
      "color:#8b95a1",
      "cursor:pointer",
    ].join(";");
    later.addEventListener("click", () => {
      remove();
      opts.onLater?.();
    });
    panel.appendChild(later);
  }

  root.appendChild(panel);
  (opts.container ?? doc.body).appendChild(root);
  return remove;
}
