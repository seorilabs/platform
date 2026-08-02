#!/usr/bin/env bash
#
# 스펙 경로와 실제 라우트를 대조한다.
#
# openapi.yaml이 계약의 SoT다(ADR 0007). 그런데 실제로 갈라져 있었다 —
# 스펙에는 마켓별 verify 3개와 /iap/webhooks/{app-store,google-play}가
# 있었는데 구현은 /iap/verify 한 벌과 /iap/webhooks/{apple,play}였다.
#
# 웹훅 경로가 특히 위험하다. 스펙대로 App Store Connect에 등록하면
# 404가 나고 환불·취소 알림이 유실된다. 마켓은 우리 원장을 대신
# 지켜주지 않는다.
#
# 사람이 눈으로 대조하는 것에 기대지 않는다.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$here/.." && pwd)"

python3 - "$repo_root" <<'PY'
import re
import subprocess
import sys

root = sys.argv[1]

# 스펙 경로. servers.url이 .../v1 이므로 스펙은 /v1 없이 적는다.
spec = set()
with open(f"{root}/spec/openapi.yaml", encoding="utf-8") as f:
    for line in f:
        m = re.match(r"^  (/[A-Za-z0-9/{}._-]+):\s*$", line)
        if m:
            spec.add(m.group(1))

# 실제 등록 라우트. net/http 패턴 문자열에서 뽑는다.
out = subprocess.run(
    ["grep", "-rhoE", r'"(GET|POST|DELETE|PUT|PATCH) /v1/[A-Za-z0-9/{}._-]+"',
     f"{root}/server", "--include=*.go"],
    capture_output=True, text=True, check=False,
).stdout

real = set()
for line in out.splitlines():
    path = line.strip('"').split(" ", 1)[1]
    real.add(path[len("/v1"):])

# 주석 속 예시는 라우트가 아니다. httpx/envelope.go가 net/http 패턴
# 라우팅을 설명하며 이 경로를 인용한다.
real.discard("/inbox/{id}/claim")

missing = sorted(spec - real)
undocumented = sorted(real - spec)

if missing:
    print("스펙에 있는데 구현에 없다:", file=sys.stderr)
    for p in missing:
        print(f"  {p}", file=sys.stderr)
if undocumented:
    print("구현에 있는데 스펙에 없다:", file=sys.stderr)
    for p in undocumented:
        print(f"  {p}", file=sys.stderr)

if missing or undocumented:
    print(
        "\n계약이 갈라졌다. openapi.yaml이 SoT다 — 구현을 고치거나 스펙을 고쳐라.",
        file=sys.stderr,
    )
    raise SystemExit(1)

print(f"스펙과 라우트가 일치한다: {len(spec)}개")
PY
