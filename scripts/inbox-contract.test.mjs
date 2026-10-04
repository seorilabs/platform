import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { test } from 'node:test';
const spec = await readFile(new URL('../spec/openapi.yaml', import.meta.url), 'utf8');
function operation(path) {
  const start = spec.indexOf(`  ${path}:\n`);
  assert.notEqual(start, -1);
  const end = spec.indexOf('\n  /', start + 1);
  return spec.slice(start, end < 0 ? undefined : end);
}
test('타 사용자와 미존재 우편의 422는 읽음·수령 계약에 모두 선언한다', () => {
  for (const path of ['/inbox/{id}/read', '/inbox/{id}/claim']) {
    assert.match(operation(path), /'422':\s+\$ref: '#\/components\/responses\/Unprocessable'/u);
  }
});
test('운영 발행의 replay 충돌·허용 보상·요청 제한·내부 실패를 계약에 선언한다', () => {
  const op = operation('/admin/apps/{appId}/inbox');
  for (const code of ['409', '422', '429', '500']) assert.match(op, new RegExp(`'${code}':`));
});
