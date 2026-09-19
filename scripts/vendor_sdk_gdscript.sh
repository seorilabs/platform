#!/usr/bin/env bash
# Platform GDScript SDK를 소비자 Godot 프로젝트에 checksum 고정 vendoring한다.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
source_dir="$repo_root/sdk-gdscript/addons/seorilabs_platform"
target_dir=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --target)
      target_dir="${2:-}"
      shift 2
      ;;
    *)
      echo "알 수 없는 인자: $1" >&2
      exit 2
      ;;
  esac
done

if [[ -z "$target_dir" || "$target_dir" != */addons/seorilabs_platform ]]; then
  echo "--target은 Godot 프로젝트의 addons/seorilabs_platform 경로여야 한다." >&2
  exit 2
fi

target_parent="$(dirname "$target_dir")"
mkdir -p "$target_parent"
target_dir="$(cd "$target_parent" && pwd)/$(basename "$target_dir")"
mkdir -p "$target_dir"

# 이 helper는 미발행 checkout 검증용이다. dirty SDK를 commit provenance로 가장하지 않고,
# branch가 움직여도 같은 내용을 뜻하도록 exact source SHA만 기록한다.
if [[ -n "$(git -C "$repo_root" status --porcelain --untracked-files=all -- sdk-gdscript)" ]]; then
  echo "dirty sdk-gdscript checkout은 vendoring할 수 없다." >&2
  exit 1
fi
source_sha="$(git -C "$repo_root" rev-parse HEAD)"
if [[ ! "$source_sha" =~ ^[0-9a-f]{40}$ ]]; then
  echo "Platform source SHA를 확인할 수 없다." >&2
  exit 1
fi

# vendored SDK는 수정하지 않는 계약이다. 제거된 upstream GDScript가 소비자에
# 남아 죽은 구현이 되는 것을 막기 위해 SDK의 .gd만 정리한다.
find "$target_dir" -type f -name '*.gd' -delete

while IFS= read -r -d '' source_file; do
  relative="${source_file#"$source_dir"/}"
  destination="$target_dir/$relative"
  mkdir -p "$(dirname "$destination")"
  cp "$source_file" "$destination"
done < <(find "$source_dir" -type f -not -path '*/tools/*' -print0)

cp "$repo_root/sdk-gdscript/VERSION" "$target_dir/VERSION"
cp "$repo_root/sdk-gdscript/CHECKSUM" "$target_dir/CHECKSUM"
printf 'https://github.com/seorilabs/platform/tree/%s/sdk-gdscript\n' "$source_sha" \
  > "$target_dir/SOURCE"

echo "Platform GDScript SDK $(tr -d '[:space:]' < "$target_dir/VERSION") vendored: $target_dir"
