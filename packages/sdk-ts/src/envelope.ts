/**
 * 서버 응답 envelope 해석.
 *
 * 계약은 spec/conformance/envelope.json이 정본이다.
 *
 * 가장 중요한 규칙은 **미지 필드를 무시한다**는 것이다.
 * lizard-tycoon의 기존 IAP 클라이언트는 응답 키 개수까지 일치를 요구해서
 * 서버가 필드를 하나 추가하면 구버전이 깨졌다. 마켓에 배포된 앱은
 * 2~3년 살아남으므로 그 방식으로는 `/v1`을 영구히 유지할 수 없다.
 */

/** 로컬에서 판정한 오류 코드. 서버가 준 것이 아니다. */
export const LOCAL_RESPONSE_INVALID = "iap_response_invalid";

/** 서버가 준 오류. */
export interface PlatformErrorBody {
  code: string;
  message: string;
}

export type EnvelopeResult<T> =
  | { valid: true; ok: true; result: T }
  | { valid: true; ok: false; error: PlatformErrorBody }
  | { valid: false; localCode: string; message: string };

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/**
 * HTTP 상태와 본문으로 envelope을 해석한다.
 *
 * 상태 코드와 `ok`가 어긋나면 무효로 본다. 둘 중 하나가 거짓말을 하는
 * 상황이라 어느 쪽을 믿을지 정할 수 없다. 지급 여부가 걸린 응답에서
 * 추측으로 진행하면 안 된다.
 */
export function parseEnvelope<T = unknown>(
  httpStatus: number,
  body: unknown,
): EnvelopeResult<T> {
  if (!isRecord(body)) {
    return invalid("응답 본문이 올바르지 않아요");
  }
  if (typeof body.ok !== "boolean") {
    return invalid("응답 형식이 올바르지 않아요");
  }

  const httpOk = httpStatus >= 200 && httpStatus < 300;
  if (httpOk !== body.ok) {
    return invalid("응답 상태가 일치하지 않아요");
  }

  if (body.ok) {
    // result가 없어도 된다. 본문 없는 성공 응답이 있다.
    return { valid: true, ok: true, result: (body.result ?? {}) as T };
  }

  const error = body.error;
  if (!isRecord(error) || typeof error.code !== "string" || error.code === "") {
    return invalid("오류 응답 형식이 올바르지 않아요");
  }

  return {
    valid: true,
    ok: false,
    error: {
      code: error.code,
      message: typeof error.message === "string" ? error.message : "",
    },
  };
}

function invalid(message: string): EnvelopeResult<never> {
  return { valid: false, localCode: LOCAL_RESPONSE_INVALID, message };
}
