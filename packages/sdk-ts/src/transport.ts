/**
 * HTTP 전송과 재시도.
 *
 * 재시도 정책은 backoff.ts가 정하고 여기서는 실행만 한다.
 */

import { isRetryableStatus, nextDelayMs } from "./backoff.ts";
import { parseEnvelope, type PlatformErrorBody } from "./envelope.ts";
import { SDK_VERSION } from "./version.ts";

/** 플랫폼 호출이 실패했을 때 던지는 오류. */
export class PlatformError extends Error {
  readonly code: string;
  readonly status: number;
  /** 로컬 판정이면 true다. 서버가 준 오류가 아니다. */
  readonly local: boolean;

  constructor(code: string, message: string, status: number, local = false) {
    super(message);
    this.name = "PlatformError";
    this.code = code;
    this.status = status;
    this.local = local;
  }
}

export interface TransportOptions {
  baseUrl: string;
  appId: string;
  /** 최대 재시도 횟수. 첫 시도는 여기 포함되지 않는다. */
  maxRetries?: number;
  fetchImpl?: typeof fetch;
  /** 테스트에서 시간을 건너뛰기 위해 주입한다. */
  sleep?: (ms: number) => Promise<void>;
  random?: () => number;
  now?: () => number;
  /** 요청 하나의 제한 시간. */
  timeoutMs?: number;
  /** Firebase App Check token 공급자. 요청 시점마다 호출해 만료 토큰을 피한다. */
  appCheckToken?: () => Promise<string>;
  /**
   * 요청마다 붙일 실행 환경. `X-Seori-AppVer`, `X-Seori-Runtime`으로 나간다.
   *
   * 서버는 이 값으로 구버전 트래픽을 관측하고, 세션 경로에서 처음 보는
   * (앱, 런타임, 버전) 조합을 새 빌드의 실유입 개시로 기록한다.
   */
  clientContext?: () => ClientContext;
}

export interface ClientContext {
  appVersion?: string | undefined;
  runtime?: string | undefined;
}

/**
 * 서버가 관측 헤더에 허용하는 값이다. 상한 32자에 안전 문자만 받는다.
 *
 * 이 값은 헤더 줄에 그대로 들어가므로 여기서 좁히지 않으면 개행 하나로
 * 요청이 통째로 실패한다.
 */
const CLIENT_CONTEXT_PATTERN = /^[A-Za-z0-9._/+-]{1,32}$/;

export interface RequestOptions {
  method: "GET" | "POST" | "DELETE";
  path: string;
  body?: unknown;
  /** 세션 토큰. 없으면 Authorization 헤더를 붙이지 않는다. */
  token?: string | undefined;
  /** 조회 파라미터. */
  query?: Record<string, string | number | undefined>;
  headers?: Record<string, string>;
  /** 재시도하지 않는다. 결제처럼 중복이 위험한 요청에 쓴다. */
  noRetry?: boolean;
}

const DEFAULT_MAX_RETRIES = 3;
const DEFAULT_TIMEOUT_MS = 15_000;

export class Transport {
  private readonly baseUrl: string;
  private readonly appId: string;
  private readonly maxRetries: number;
  private readonly fetchImpl: typeof fetch;
  private readonly sleep: (ms: number) => Promise<void>;
  private readonly random: () => number;
  private readonly now: () => number;
  private readonly timeoutMs: number;
  private readonly appCheckToken: (() => Promise<string>) | undefined;
  private readonly clientContext: (() => ClientContext) | undefined;

  constructor(opts: TransportOptions) {
    if (!opts.baseUrl) {
      throw new Error("baseUrl이 필요해요");
    }
    if (!opts.appId) {
      throw new Error("appId가 필요해요");
    }

    this.baseUrl = opts.baseUrl.replace(/\/+$/, "");
    this.appId = opts.appId;
    this.maxRetries = opts.maxRetries ?? DEFAULT_MAX_RETRIES;
    this.fetchImpl = opts.fetchImpl ?? globalThis.fetch;
    this.sleep = opts.sleep ?? ((ms) => new Promise((r) => setTimeout(r, ms)));
    this.random = opts.random ?? Math.random;
    this.now = opts.now ?? Date.now;
    this.timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
    this.appCheckToken = opts.appCheckToken;
    this.clientContext = opts.clientContext;
  }

  /**
   * 요청을 보내고 envelope을 푼다.
   *
   * 재시도는 5xx와 네트워크 오류에만 한다. 4xx는 즉시 던진다 —
   * 같은 요청을 다시 보내도 결과가 같기 때문이다.
   */
  async request<T = unknown>(opts: RequestOptions): Promise<T> {
    const url = this.buildUrl(opts.path, opts.query);
    const maxAttempts = opts.noRetry ? 1 : this.maxRetries + 1;

    let lastError: PlatformError | undefined;

    for (let attempt = 1; attempt <= maxAttempts; attempt++) {
      const outcome = await this.attempt<T>(url, opts);

      if (outcome.kind === "ok") {
        return outcome.value;
      }

      lastError = outcome.error;

      const isLast = attempt === maxAttempts;
      if (isLast || !isRetryableStatus(outcome.status)) {
        throw outcome.error;
      }

      const delay = nextDelayMs(attempt, outcome.retryAfter, this.random, this.now);
      await this.sleep(delay);
    }

    // 도달하지 않는다. 루프가 항상 반환하거나 던진다.
    throw lastError ?? new PlatformError("internal", "요청에 실패했어요", 0, true);
  }

  private async attempt<T>(
    url: string,
    opts: RequestOptions,
  ): Promise<
    | { kind: "ok"; value: T }
    | { kind: "err"; error: PlatformError; status: number; retryAfter?: string | null }
  > {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), this.timeoutMs);

    try {
      const response = await this.fetchImpl(url, {
        method: opts.method,
        headers: await raceAbort(this.buildHeaders(opts), controller.signal),
        body: opts.body === undefined ? null : JSON.stringify(opts.body),
        signal: controller.signal,
      });

      const retryAfter = response.headers.get("Retry-After");
      const parsed = await this.readEnvelope<T>(response);

      if (parsed.kind === "ok") {
        return parsed;
      }
      return { ...parsed, retryAfter };
    } catch (err) {
      // 네트워크 오류와 타임아웃이다. status 0으로 재시도 대상이 된다.
      const message = err instanceof Error ? err.message : "연결에 실패했어요";
      return {
        kind: "err",
        error: new PlatformError("network_error", message, 0, true),
        status: 0,
      };
    } finally {
      clearTimeout(timer);
    }
  }

  private async readEnvelope<T>(
    response: Response,
  ): Promise<
    { kind: "ok"; value: T } | { kind: "err"; error: PlatformError; status: number }
  > {
    let body: unknown;
    try {
      const text = await response.text();
      body = text === "" ? null : JSON.parse(text);
    } catch {
      body = null;
    }

    const env = parseEnvelope<T>(response.status, body);

    if (!env.valid) {
      return {
        kind: "err",
        error: new PlatformError(env.localCode, env.message, response.status, true),
        status: response.status,
      };
    }
    if (env.ok) {
      return { kind: "ok", value: env.result };
    }

    return {
      kind: "err",
      error: toPlatformError(env.error, response.status),
      status: response.status,
    };
  }

  private buildUrl(path: string, query?: RequestOptions["query"]): string {
    const url = new URL(this.baseUrl + path);
    for (const [key, value] of Object.entries(query ?? {})) {
      if (value !== undefined) {
        url.searchParams.set(key, String(value));
      }
    }
    return url.toString();
  }

  private async buildHeaders(opts: RequestOptions): Promise<Record<string, string>> {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      // 서버가 앱을 식별하는 헤더다. 레지스트리 조회의 키가 된다.
      "X-Seori-App": this.appId,
      // 어느 SDK 버전의 트래픽인지는 SDK가 스스로 안다. 앱 설정에 의존하지 않는다.
      "X-Seori-Sdk": `ts/${SDK_VERSION}`,
    };
    const context = this.clientContext?.();
    const appVersion = boundedHeaderValue(context?.appVersion);
    if (appVersion) {
      headers["X-Seori-AppVer"] = appVersion;
    }
    const runtime = boundedHeaderValue(context?.runtime);
    if (runtime) {
      headers["X-Seori-Runtime"] = runtime;
    }
    const appCheckToken = await this.appCheckToken?.();
    if (appCheckToken) {
      headers["X-Firebase-AppCheck"] = appCheckToken;
    }
    Object.assign(headers, opts.headers);
    if (opts.token) {
      headers["Authorization"] = `Bearer ${opts.token}`;
    }
    return headers;
  }
}

// 형식을 어긴 값은 보내지 않는다. 서버가 어차피 버리는 값이고, 잘라 보내면
// 관측에 없는 버전 문자열이 만들어진다.
function boundedHeaderValue(value: string | undefined): string | undefined {
  const trimmed = value?.trim();
  if (!trimmed || !CLIENT_CONTEXT_PATTERN.test(trimmed)) return undefined;
  return trimmed;
}

function raceAbort<T>(promise: Promise<T>, signal: AbortSignal): Promise<T> {
  const aborted = new Promise<never>((_, reject) => {
    const fail = () => reject(new Error("요청 시간이 초과됐어요"));
    if (signal.aborted) {
      fail();
      return;
    }
    signal.addEventListener("abort", fail, { once: true });
  });
  return Promise.race([promise, aborted]);
}

function toPlatformError(body: PlatformErrorBody, status: number): PlatformError {
  return new PlatformError(body.code, body.message, status, false);
}
