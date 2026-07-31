/**
 * 이벤트 파라미터 정규화.
 *
 * 계약은 spec/conformance/param-normalization.json이 정본이고
 * GDScript SDK와 **바이트 단위로 같은 출력**을 내야 한다.
 * 조직에서 boolean 직렬화가 `1/0`, `"true"/"false"`, 미처리로 갈려
 * 같은 이벤트가 앱마다 다르게 쌓이고 있었다.
 */

/** 정규화를 통과한 값. */
export type ParamValue = string | number;

/** 파라미터 개수 상한. GA4 규격을 따른다. */
export const MAX_PARAMS = 25;

/** 파라미터 이름 길이 상한. */
export const MAX_KEY_LENGTH = 40;

/** 문자열 값 길이 상한. */
export const MAX_STRING_LENGTH = 100;

/**
 * 개인정보로 판정해 버리는 키 이름.
 *
 * 플랫폼은 PII를 저장하지 않는다. 서버에서도 거르지만 SDK에서 먼저
 * 버려서 네트워크에 실리지 않게 한다. 실수로 넣은 값이 로그나
 * 프록시에 남는 것을 막는다.
 */
export const PII_KEYS: readonly string[] = [
  "email",
  "e_mail",
  "mail",
  "phone",
  "phone_number",
  "tel",
  "mobile",
  "name",
  "full_name",
  "first_name",
  "last_name",
  "real_name",
  "address",
  "addr",
  "zipcode",
  "postal_code",
  "birth",
  "birthday",
  "birthdate",
  "ssn",
  "passport",
  "card_number",
  "credit_card",
  "ip",
  "ip_address",
];

const PII_SET = new Set(PII_KEYS);

/**
 * 파라미터를 정규화한다.
 *
 * 버리는 것과 변환하는 것을 구분한다. 값을 조용히 문자열로 바꾸지
 * 않는 것이 중요하다 — 객체를 stringify하면 PII가 통째로 실려 나갈 수 있다.
 */
export function normalizeParams(
  input: Readonly<Record<string, unknown>>,
): Record<string, ParamValue> {
  const kept: Array<[string, ParamValue]> = [];

  for (const key of Object.keys(input)) {
    if (!isAllowedKey(key)) {
      continue;
    }
    const value = normalizeValue(input[key]);
    if (value === undefined) {
      continue;
    }
    kept.push([key, value]);
  }

  // 상한은 버릴 것을 다 버린 뒤에 적용한다.
  // 먼저 자르면 버려질 값이 자리를 차지해 멀쩡한 값이 밀려난다.
  if (kept.length > MAX_PARAMS) {
    // 키 오름차순으로 앞의 것을 남긴다. 삽입 순서를 쓰면
    // 언어마다 결과가 달라져 GDScript와 어긋난다.
    kept.sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
    kept.length = MAX_PARAMS;
  }

  const out: Record<string, ParamValue> = {};
  for (const [key, value] of kept) {
    out[key] = value;
  }
  return out;
}

function isAllowedKey(key: string): boolean {
  if (key.length === 0 || key.length > MAX_KEY_LENGTH) {
    return false;
  }
  return !PII_SET.has(key.toLowerCase());
}

/**
 * 값 하나를 정규화한다. `undefined`면 파라미터를 버린다는 뜻이다.
 */
function normalizeValue(value: unknown): ParamValue | undefined {
  switch (typeof value) {
    case "boolean":
      // 1/0으로 통일한다. happy-farm의 toScalar 규약을 채택했다.
      return value ? 1 : 0;

    case "number":
      // NaN과 Infinity는 JSON으로 직렬화할 수 없다.
      // 버리지 않고 0으로 두는 이유는 "값이 있었다"는 사실 자체가
      // 신호이기 때문이다.
      return Number.isFinite(value) ? value : 0;

    case "string":
      return value.slice(0, MAX_STRING_LENGTH);

    default:
      // null, undefined, 객체, 배열, 함수, symbol이 여기 온다.
      // 객체를 문자열로 바꾸지 않는다. 조용한 stringify는
      // 의도치 않은 정보를 통째로 실어 보낸다.
      return undefined;
  }
}
