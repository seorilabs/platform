/**
 * RemoteConfig와 업데이트 게이트.
 *
 * kill switch, 업데이트 유도, 점검 안내를 담당한다.
 * Firebase RemoteConfig가 AIT와 Godot 런타임에서 동작하지 않아
 * 앱마다 제각각 구현하던 것을 하나로 모았다.
 */

import { PlatformError, type Transport } from "./transport.ts";

export interface SdkStatus {
  /**
   * 서버가 확정한다. 클라이언트는 버전을 비교하지 않는다.
   *
   * SDK 없이 raw HTTP로 붙는 앱도 그냥 동작해야 하고, 같은 비교 구현을
   * TS와 GDScript에 두 벌 두면 언젠가 갈라진다.
   */
  status: "ok" | "deprecated" | "blocked";
  message?: string;
  /** 플랫폼에 맞는 스토어 주소. 설치본이 없는 ait·web에는 없다. */
  updateUrl?: string;
  /** 수동 kill switch가 설정했을 때만 온다. */
  minSupportedVersion?: string;
  /** 유도할 목표 버전. */
  recommendedVersion?: string;
}

export interface Maintenance {
  active: boolean;
  message?: string;
  until?: string;
}

export interface RemoteConfig {
  values: Record<string, unknown>;
  features: Record<string, boolean>;
  sdk: SdkStatus;
  maintenance: Maintenance;
}

/**
 * `/v1/auth/session`이 응답에 얹어 주는 설정.
 *
 * 네 필드는 함께 오거나 함께 빠진다. 설정 조회가 실패해도 세션 발급은
 * 막지 않으며, 그때는 넷 다 없다.
 */
export interface SessionConfigOverlay {
  features?: Record<string, boolean>;
  sdk?: SdkStatus;
  maintenance?: Maintenance;
  configEtag?: string;
}

export interface ConfigTarget {
  appVersion: string;
  platform: "android" | "ios" | "web" | "ait";
  locale?: string;
}

/** 앱이 지금 무엇을 보여줘야 하는지. */
export type UpdateGateState =
  | { kind: "ok" }
  | {
      kind: "recommended";
      message: string;
      updateUrl?: string;
      recommendedVersion?: string;
    }
  | {
      kind: "required";
      message: string;
      updateUrl?: string;
      recommendedVersion?: string;
    }
  | { kind: "maintenance"; message: string; until?: string };

const DEFAULT_REQUIRED_MESSAGE = "업데이트가 필요해요. 스토어에서 최신 버전을 받아 주세요";
const DEFAULT_RECOMMENDED_MESSAGE = "새 버전이 나왔어요. 업데이트하면 더 편하게 쓸 수 있어요";
const DEFAULT_MAINTENANCE_MESSAGE = "지금 점검 중이에요. 잠시 후 다시 시도해 주세요";

/**
 * 서버가 확정한 상태를 게이트 결정으로 옮긴다.
 *
 * 버전을 비교하지 않는다. 서버가 `X-Seori-AppVer`와 `X-Seori-Runtime`을 보고
 * 이미 판정했다.
 *
 * 점검이 강제보다 우선한다. 점검은 시간이 정해져 있고 자동으로 해제되며
 * 안내 문구가 더 행동 가능하다.
 *
 * 모르는 status는 ok로 읽는다. 서버가 상태를 추가해도 구버전 SDK가
 * 스스로를 막지 않는다.
 */
export function updateGateState(config: RemoteConfig): UpdateGateState {
  const maintenance = config.maintenance;
  if (maintenance?.active) {
    return {
      kind: "maintenance",
      message: maintenance.message || DEFAULT_MAINTENANCE_MESSAGE,
      ...(maintenance.until ? { until: maintenance.until } : {}),
    };
  }

  const sdk = config.sdk;
  if (sdk?.status === "blocked") {
    return {
      kind: "required",
      message: sdk.message || DEFAULT_REQUIRED_MESSAGE,
      ...(sdk.updateUrl ? { updateUrl: sdk.updateUrl } : {}),
      ...(sdk.recommendedVersion ? { recommendedVersion: sdk.recommendedVersion } : {}),
    };
  }
  if (sdk?.status === "deprecated") {
    return {
      kind: "recommended",
      message: sdk.message || DEFAULT_RECOMMENDED_MESSAGE,
      ...(sdk.updateUrl ? { updateUrl: sdk.updateUrl } : {}),
      ...(sdk.recommendedVersion ? { recommendedVersion: sdk.recommendedVersion } : {}),
    };
  }
  return { kind: "ok" };
}

/** 권장 안내를 마지막으로 띄운 기록. */
export interface GatePromptLog {
  /** 그때의 권장 기준 버전. 기준이 올라가면 즉시 다시 띄운다. */
  version: string;
  promptedAt: number;
}

/** 권장 안내 노출 이력을 앱 재시작 후에도 유지하기 위한 저장소. */
export interface GateStore {
  load(): Promise<GatePromptLog | null>;
  save(log: GatePromptLog): Promise<void>;
}

/** 아무것도 저장하지 않는 저장소. 재시작하면 다시 띄운다. */
export class MemoryGateStore implements GateStore {
  private log: GatePromptLog | null = null;

  async load(): Promise<GatePromptLog | null> {
    return this.log;
  }
  async save(log: GatePromptLog): Promise<void> {
    this.log = log;
  }
}

const GATE_STORAGE_KEY = "seorilabs.platform.updateGate.v1";

/**
 * 브라우저 저장소를 쓰는 기본 구현.
 *
 * 웹과 AIT WebView는 주입 없이 바로 맞게 동작한다. React Native는
 * `localStorage`가 없으므로 앱이 `sessionStore`와 같은 방식으로
 * AsyncStorage 어댑터를 넣는다.
 */
export function createDefaultGateStore(): GateStore {
  try {
    const storage = (globalThis as { localStorage?: Storage }).localStorage;
    if (!storage) return new MemoryGateStore();
    // 접근 자체가 던지는 환경(private browsing 등)을 여기서 걸러낸다.
    storage.getItem(GATE_STORAGE_KEY);
    return {
      async load() {
        try {
          const raw = storage.getItem(GATE_STORAGE_KEY);
          if (!raw) return null;
          const value: unknown = JSON.parse(raw);
          if (!value || typeof value !== "object") return null;
          const log = value as Partial<GatePromptLog>;
          if (typeof log.version !== "string" || typeof log.promptedAt !== "number") {
            return null;
          }
          return { version: log.version, promptedAt: log.promptedAt };
        } catch {
          return null;
        }
      },
      async save(log: GatePromptLog) {
        try {
          storage.setItem(GATE_STORAGE_KEY, JSON.stringify(log));
        } catch {
          // quota나 private browsing 오류가 안내를 막으면 안 된다.
        }
      },
    };
  } catch {
    return new MemoryGateStore();
  }
}

export interface ConfigOptions {
  transport: Transport;
  /** 캐시 유효 시간. 서버도 max-age 60을 준다. */
  ttlMs?: number;
  now?: () => number;
  gateStore?: GateStore;
}

const DEFAULT_TTL_MS = 60_000;

/**
 * 권장 안내를 다시 띄우기까지의 간격.
 *
 * 매 실행마다 띄우면 짜증나고, 버전당 1회면 한 번 닫은 유저가 그대로
 * 구버전에 남는다. 하루 1회는 잔존하는 유저에게 반복 노출되므로 누적
 * 전환율이 높고 이탈은 적다.
 */
const RECOMMEND_PROMPT_INTERVAL_MS = 24 * 60 * 60 * 1000;

/**
 * 서버에 닿지 못했을 때 쓰는 값.
 *
 * **열린 상태로 둔다.** 설정을 못 읽었다고 앱을 막으면 서버 장애가
 * 전체 서비스 중단으로 번진다. 차단은 서버가 명시적으로 지시할 때만 한다.
 */
const FALLBACK: RemoteConfig = {
  values: {},
  features: {},
  sdk: { status: "ok" },
  maintenance: { active: false },
};

export class Config {
  private readonly transport: Transport;
  private readonly ttlMs: number;
  private readonly now: () => number;
  private readonly gateStore: GateStore;

  private cached: RemoteConfig | null = null;
  private cachedAt = 0;
  private etag: string | undefined;
  private inflight: Promise<RemoteConfig> | null = null;

  constructor(opts: ConfigOptions) {
    this.transport = opts.transport;
    this.ttlMs = opts.ttlMs ?? DEFAULT_TTL_MS;
    this.now = opts.now ?? Date.now;
    this.gateStore = opts.gateStore ?? createDefaultGateStore();
  }

  /**
   * 설정을 가져온다. 캐시가 유효하면 네트워크를 타지 않는다.
   *
   * 실패하면 마지막 캐시를 주고, 그것도 없으면 열린 기본값을 준다.
   * 던지지 않는다 — 설정 조회 실패가 앱 시작을 막으면 안 된다.
   */
  async fetch(target: ConfigTarget): Promise<RemoteConfig> {
    if (this.cached && this.now() - this.cachedAt < this.ttlMs) {
      return this.cached;
    }

    // 동시 호출을 하나로 묶는다.
    if (!this.inflight) {
      this.inflight = this.load(target).finally(() => {
        this.inflight = null;
      });
    }
    return this.inflight;
  }

  /** 마지막으로 받은 설정. 네트워크를 타지 않는다. */
  current(): RemoteConfig {
    return this.cached ?? FALLBACK;
  }

  /** 지금 앱이 무엇을 보여줘야 하는지. 네트워크를 타지 않는다. */
  gate(): UpdateGateState {
    return updateGateState(this.current());
  }

  /**
   * 이 상태를 지금 띄워도 되는지 본다.
   *
   * 강제와 점검은 언제나 띄운다. 이력은 권장에만 적용된다.
   */
  async shouldPrompt(state: UpdateGateState): Promise<boolean> {
    if (state.kind === "ok") return false;
    if (state.kind !== "recommended") return true;

    const log = await this.gateStore.load().catch(() => null);
    if (!log) return true;
    // 권장 기준이 올라갔으면 이력을 무시하고 다시 띄운다.
    if (log.version !== (state.recommendedVersion ?? "")) return true;
    return this.now() - log.promptedAt >= RECOMMEND_PROMPT_INTERVAL_MS;
  }

  /** 권장 안내를 띄운 사실을 남긴다. 강제와 점검은 기록하지 않는다. */
  async markPrompted(state: UpdateGateState): Promise<void> {
    if (state.kind !== "recommended") return;
    await this.gateStore
      .save({ version: state.recommendedVersion ?? "", promptedAt: this.now() })
      .catch(() => {
        // 저장 실패가 안내를 막으면 안 된다. 다음에 한 번 더 뜰 뿐이다.
      });
  }

  /**
   * 세션 응답에 함께 온 설정을 캐시에 넣는다.
   *
   * `/v1/auth/session`이 설정을 얹어 주므로 앱 시작 시 왕복이 하나 준다.
   *
   * 통째로 덮지 않고 병합한다. 세션 오버레이에는 `values`가 없어서,
   * 덮으면 앞서 `/v1/config`로 받은 값이 사라진다.
   */
  seedSession(overlay: SessionConfigOverlay): void {
    if (
      overlay.features === undefined &&
      overlay.sdk === undefined &&
      overlay.maintenance === undefined
    ) {
      // 서버가 설정을 싣지 못했다. 캐시 수명을 갱신하면 진짜 조회가
      // TTL 동안 막힌다.
      return;
    }

    const base = this.cached ?? FALLBACK;
    this.cached = {
      values: base.values,
      features: overlay.features ?? base.features,
      sdk: overlay.sdk ?? base.sdk,
      maintenance: overlay.maintenance ?? base.maintenance,
    };
    this.cachedAt = this.now();
    if (overlay.configEtag) {
      this.etag = overlay.configEtag;
    }
  }

  private async load(target: ConfigTarget): Promise<RemoteConfig> {
    try {
      const res = await this.transport.request<RemoteConfig>({
        method: "GET",
        path: "/v1/config",
        query: {
          appVersion: target.appVersion,
          platform: target.platform,
          locale: target.locale,
        },
        headers: this.etag ? { "If-None-Match": this.etag } : {},
      });

      this.cached = res;
      this.cachedAt = this.now();
      return res;
    } catch (err) {
      // 304는 "캐시가 그대로 유효하다"는 뜻이다. Transport가 응답 헤더를
      // 노출하지 않아 빈 본문이 파싱 오류로 올라오지만, 캐시 수명은
      // 갱신해야 한다. 안 하면 TTL이 만료된 뒤 매 호출이 왕복을 한 번씩 한다.
      if (err instanceof PlatformError && err.status === 304 && this.cached) {
        this.cachedAt = this.now();
        return this.cached;
      }
      // 마지막으로 성공한 값을 유지한다.
      // 없으면 열린 기본값이다.
      return this.cached ?? FALLBACK;
    }
  }
}
