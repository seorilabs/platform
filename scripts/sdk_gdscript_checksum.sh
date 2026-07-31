#!/usr/bin/env bash
#
# GDScript SDK 체크섬.
#
# GDScript에는 패키지 매니저가 없어서 소비자 저장소가 파일을 복사해 간다.
# 복사본은 조용히 갈라진다 — foam-party와 lucid-chess가 같은 262줄 파일을
# 갖고 있으면서 md5가 다른 상태로 지냈다. 어느 쪽이 최신인지 아무도 몰랐다.
#
# 그래서 체크섬을 만들어 두고 소비자가 대조한다.
#
#   scripts/sdk_gdscript_checksum.sh          체크섬 출력
#   scripts/sdk_gdscript_checksum.sh --write  CHECKSUM 파일 갱신
#   scripts/sdk_gdscript_checksum.sh --check  CHECKSUM 파일과 대조
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$here/.." && pwd)"
addon_dir="$repo_root/sdk-gdscript/addons/seorilabs_platform"
checksum_file="$repo_root/sdk-gdscript/CHECKSUM"

if [[ ! -d "$addon_dir" ]]; then
  echo "애드온 디렉토리가 없다: $addon_dir" >&2
  exit 1
fi

# 파일 이름과 내용을 함께 해싱한다.
#
# 내용만 해싱하면 파일 이름이 바뀌어도 같은 값이 나온다.
# tools/는 검증용이라 소비자가 가져가지 않으므로 제외한다.
compute() {
  (
    cd "$addon_dir"
    find . -name "*.gd" -not -path "./tools/*" -print0 \
      | LC_ALL=C sort -z \
      | while IFS= read -r -d '' file; do
          printf '%s ' "$file"
          shasum -a 256 "$file" | awk '{print $1}'
        done \
      | shasum -a 256 \
      | awk '{print $1}'
  )
}

actual="$(compute)"

case "${1:-}" in
  --write)
    printf '%s\n' "$actual" > "$checksum_file"
    echo "CHECKSUM 갱신: $actual"
    ;;

  --check)
    if [[ ! -f "$checksum_file" ]]; then
      echo "CHECKSUM 파일이 없다. --write로 만들어라." >&2
      exit 1
    fi

    expected="$(tr -d '[:space:]' < "$checksum_file")"
    if [[ "$actual" != "$expected" ]]; then
      echo "체크섬이 다르다." >&2
      echo "  기록: $expected" >&2
      echo "  실제: $actual" >&2
      echo "SDK를 고쳤으면 --write로 갱신하고 함께 커밋해라." >&2
      exit 1
    fi
    echo "체크섬 일치: $actual"
    ;;

  *)
    printf '%s\n' "$actual"
    ;;
esac
