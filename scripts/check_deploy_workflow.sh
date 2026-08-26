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

python3 - "$repo_root/.github/workflows/deploy.yml" "$repo_root/deploy/rpi/presence-edge.yaml" <<'PY'
import sys
from pathlib import Path

path = Path(sys.argv[1])
text = path.read_text(encoding="utf-8")
presence_manifest = Path(sys.argv[2]).read_text(encoding="utf-8")


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


def command_block(marker: str) -> str:
    start = text.find(marker)
    require(start >= 0, f"배포 명령이 없다: {marker}")
    end = text.find("--quiet", start)
    require(end >= 0, f"배포 명령의 끝을 찾지 못했다: {marker}")
    return text[start:end]


session_secret = "PLATFORM_SESSION_SECRET=platform-session-secret:latest"
for target in ("platform-api", "platform-iap", "platform-ingest", "platform-ads"):
    block = command_block(f"gcloud run deploy {target}")
    require(session_secret in block, f"{target}에 Platform 세션 secret이 없다.")

admin_block = command_block("gcloud run deploy platform-admin")
require(session_secret not in admin_block, "platform-admin에 세션 secret을 마운트하면 안 된다.")
require(
    '--remove-secrets="PLATFORM_SESSION_SECRET"' in admin_block,
    "platform-admin의 기존 세션 secret을 제거해야 한다.",
)
require(
    text.count(session_secret) == 4,
    "Platform 세션 secret은 API, IAP, Ingest, Ads 네 대상에만 마운트해야 한다.",
)
ingest_block = command_block("gcloud run deploy platform-ingest")
require(
    '--service-account="$INGEST_RUNTIME_SA"' in ingest_block,
    "platform-ingest runtime service account가 명시되지 않았다.",
)
require("Assert session secret boundary" in text, "세션 secret readback gate가 없다.")

presence_url = "PLATFORM_PRESENCE_EDGE_URL=${PLATFORM_PRESENCE_EDGE_URL}"
presence_secret = "PLATFORM_PRESENCE_PRIVATE_KEY=platform-presence-private-key:latest"
require(presence_url in ingest_block, "platform-ingest에 Presence Edge URL이 없다.")
require(presence_secret in ingest_block, "platform-ingest에 Presence 서명키가 없다.")
require(text.count(presence_url) == 1, "Presence Edge URL은 ingest 한 곳에만 마운트해야 한다.")
require(text.count(presence_secret) == 1, "Presence 서명키는 ingest 한 곳에만 마운트해야 한다.")
require("Assert Presence secret boundary" in text, "Presence URL/secret readback gate가 없다.")
require("runAsNonRoot: true" in presence_manifest, "Presence Edge가 non-root 실행을 강제하지 않는다.")
require("runAsUser: 65532" in presence_manifest, "Presence Edge의 숫자 non-root UID가 없다.")
require("runAsGroup: 65532" in presence_manifest, "Presence Edge의 숫자 non-root GID가 없다.")


operational_url = "BACKOFFICE_OPERATIONAL_EVENTS_URL=${BACKOFFICE_OPERATIONAL_EVENTS_URL}"
operational_secret = (
    "BACKOFFICE_OPERATIONAL_EVENTS_SECRET=backoffice-operational-events-secret:latest"
)
for target in ("platform-api", "platform-iap", "platform-ads"):
    block = command_block(f"gcloud run deploy {target}")
    require(operational_url in block, f"{target}에 Backoffice 운영 URL이 없다.")
    require(operational_secret in block, f"{target}에 Backoffice 운영 secret이 없다.")

worker_block = command_block("gcloud run jobs update platform-worker")
require(session_secret not in worker_block, "platform-worker에 세션 secret을 마운트하면 안 된다.")
require(
    '--remove-secrets="PLATFORM_SESSION_SECRET"' in worker_block,
    "platform-worker의 기존 세션 secret을 제거해야 한다.",
)
require(operational_url in worker_block, "platform-worker에 Backoffice 운영 URL이 없다.")
require(operational_secret in worker_block, "platform-worker에 Backoffice 운영 secret이 없다.")

for target in ("platform-ingest", "platform-admin"):
    block = command_block(f"gcloud run deploy {target}")
    require(operational_url not in block, f"{target}에 운영 URL을 마운트하면 안 된다.")
    require(operational_secret not in block, f"{target}에 운영 secret을 마운트하면 안 된다.")

require(
    text.count(operational_secret) == 4,
    "Backoffice 운영 secret은 API, IAP, Ads, worker 네 대상에만 마운트해야 한다.",
)
require(
    "Assert Backoffice operational event boundary" in text,
    "Backoffice 운영 이벤트 배포 후 readback gate가 없다.",
)

print("production 배포 공개 IAM, IAP catalog, 운영 이벤트 경계가 일치한다.")
PY
