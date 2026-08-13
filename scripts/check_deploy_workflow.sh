#!/usr/bin/env bash
#
# production 배포 workflow의 공개 IAM과 secret 경계를 정적으로 검사한다.
#
# 조직 DRS 때문에 platform-ads는 allUsers binding 대신 invoker IAM 검사를
# 꺼야 한다. 이 플래그가 다른 서비스로 번지거나, app-scoped IAP catalog가
# 새 이미지에 명시적으로 마운트되지 않으면 배포는 성공해도 런타임이 깨진다.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$here/.." && pwd)"

python3 - "$repo_root/.github/workflows/deploy.yml" <<'PY'
import sys
from pathlib import Path

path = Path(sys.argv[1])
text = path.read_text(encoding="utf-8")


def require(condition: bool, message: str) -> None:
    if not condition:
        print(message, file=sys.stderr)
        raise SystemExit(1)


ads_start = text.find("gcloud run deploy platform-ads")
require(ads_start >= 0, "platform-ads 배포 명령이 없다.")
ads_end = text.find("--quiet", ads_start)
require(ads_end >= 0, "platform-ads 배포 명령의 끝을 찾지 못했다.")
ads_block = text[ads_start:ads_end]

public_flag = "--no-invoker-iam-check"
require(public_flag in ads_block, "platform-ads가 invoker IAM 검사를 비활성화하지 않는다.")
require(text.count(public_flag) == 1, "invoker IAM 비활성화는 platform-ads 한 곳에만 있어야 한다.")

annotation = 'run.googleapis.com/invoker-iam-disabled'
require(annotation in text, "platform-ads 공개 호출 annotation readback이 없다.")
require('[ "$actual" = "true" ]' in text, "공개 호출 annotation 실패 gate가 없다.")

catalog_mount = "IAP_CATALOG_JSON=iap-catalog:latest"
require(
    text.count(catalog_mount) == 3,
    "IAP catalog latest는 IAP, Admin, worker 세 대상에만 명시적으로 마운트해야 한다.",
)
require("Assert IAP catalog secret migration" in text, "IAP catalog secret readback gate가 없다.")
require("display='<absent>'" in text, "IAP catalog 미마운트 상태를 <absent>로 표시해야 한다.")
require("Assert AppsInToss mTLS secret boundary" in text, "AIT mTLS secret readback gate가 없다.")
require(
    text.count("IAP_TOSS_CLIENT_CERT=ait-client-cert:latest") == 2,
    "AIT 인증서 secret은 IAP와 worker 두 대상에만 마운트해야 한다.",
)
require(
    text.count("IAP_TOSS_CLIENT_KEY=ait-client-key:latest") == 2,
    "AIT 개인키 secret은 IAP와 worker 두 대상에만 마운트해야 한다.",
)

print("production 배포 공개 IAM과 IAP catalog 경계가 일치한다.")
PY
