#!/usr/bin/env bash
#
# GDScript SDK 검증.
#
# probe를 헤드리스로 돌리고 SCRIPT ERROR와 ERROR가 없는지 본다.
#
# exit code만 보면 놓친다. Godot은 스크립트 런타임 오류가 나도
# 실행을 계속하므로, probe가 중간에 끊긴 채 "통과"를 출력하고
# 0으로 끝날 수 있다. 실제로 개발 중에 그 상황을 겪었다.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$here/.." && pwd)"
project_dir="$repo_root/sdk-gdscript"

godot_bin="${GODOT_BIN:-godot}"

bash "$repo_root/scripts/sdk_gdscript_checksum.sh" --check
release_version="$(tr -d '[:space:]' < "$project_dir/VERSION")"
internal_version="$(
  sed -n 's/^const SDK_VERSION := "\([^"]*\)"$/\1/p' \
    "$project_dir/addons/seorilabs_platform/platform_client.gd"
)"
if [[ -z "$internal_version" || "$internal_version" != "$release_version" ]]; then
  echo "GDScript SDK VERSION 불일치: release=$release_version internal=$internal_version" >&2
  exit 1
fi
echo "GDScript SDK VERSION 일치: $release_version"

if ! command -v "$godot_bin" >/dev/null 2>&1; then
  echo "godot을 찾을 수 없다. GODOT_BIN으로 경로를 넘겨라." >&2
  exit 1
fi

run_probe() {
  local script="$1"
  shift

  local log
  log="$(mktemp)"

  local status=0
  "$godot_bin" --headless --path "$project_dir" --script "$script" "$@" \
    >"$log" 2>&1 || status=$?

  cat "$log"

  # SCRIPT ERROR는 exit code에 반영되지 않는다. 직접 잡는다.
  if grep -q "SCRIPT ERROR" "$log"; then
    echo "SCRIPT ERROR가 있다: $script" >&2
    rm -f "$log"
    return 1
  fi

  if grep -q "ERROR:" "$log"; then
    echo "ERROR가 있다: $script" >&2
    rm -f "$log"
    return 1
  fi

  rm -f "$log"
  return "$status"
}

echo "== conformance =="
run_probe addons/seorilabs_platform/tools/conformance_probe.gd \
  -- --conformance-dir "$repo_root/spec/conformance"

echo
echo "== smoke =="
run_probe addons/seorilabs_platform/tools/smoke_probe.gd

echo
echo "== adapters =="
run_probe addons/seorilabs_platform/tools/adapter_probe.gd

echo
echo "GDScript SDK 검증 통과"
