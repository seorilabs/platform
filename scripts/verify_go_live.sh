#!/usr/bin/env bash
#
# 전환 전 마지막 세 항목을 검증한다.
#
# 이 셋은 외부 시스템에서 사람이 조작해야 해서 코드로 만들 수 없다.
# 조작을 마친 뒤 이 스크립트를 한 번 돌리면 전부 확인된다.
#
#   1. Play 활성 구매      — 기기에서 샌드박스 결제
#   2. Play RTDN 등록      — Play Console UI
#   3. AppsInToss 인증서   — 파트너 콘솔 발급
#
# 사용법
#
#   # 전부 확인
#   PLAY_ACTIVE_TOKEN="<기기에서 얻은 토큰>" scripts/verify_go_live.sh
#
#   # 일부만
#   scripts/verify_go_live.sh rtdn
#   scripts/verify_go_live.sh ait
#
# 자세한 절차는 docs/06-release/go-live-checklist.md에 있다.
set -uo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$here/.." && pwd)"

PROJECT="${GOOGLE_CLOUD_PROJECT:-seorilabs-platform}"
REGION="${PLATFORM_REGION:-asia-northeast3}"
PACKAGE="${IAP_PLAY_PACKAGE_NAME:-com.seorilabs.lizardtycoon}"

pass=0
fail=0
skip=0

ok()   { echo "  ✅ $*"; pass=$((pass+1)); }
no()   { echo "  ❌ $*" >&2; fail=$((fail+1)); }
warn() { echo "  ⏭  $*"; skip=$((skip+1)); }

# ---------------------------------------------------------------- 1. Play 활성 구매
#
# 완료하지 않은 구매의 토큰이 필요하다. Play API에 그 토큰을 얻는
# 경로가 없어서(voidedpurchases.list는 환불분만 준다) 기기 결제로만
# 만들 수 있다.
verify_play_purchase() {
  echo "== 1. Play 활성 구매 =="

  if [[ -z "${PLAY_ACTIVE_TOKEN:-}" ]]; then
    warn "PLAY_ACTIVE_TOKEN이 없다. 기기에서 샌드박스 결제 후 토큰을 넘겨라"
    return
  fi

  local creds="${GOOGLE_APPLICATION_CREDENTIALS:-$HOME/.config/seorilabs/play-store/seorilabs-play-publisher.json}"
  if [[ ! -f "$creds" ]]; then
    no "Play publisher 자격증명이 없다: $creds"
    return
  fi

  echo "  검증과 acknowledge를 실행한다..."
  (
    cd "$repo_root/server"
    GOOGLE_APPLICATION_CREDENTIALS="$creds" \
    IAP_PLAY_PACKAGE_NAME="$PACKAGE" \
    PLAY_REAL_PURCHASE_TOKEN="$PLAY_ACTIVE_TOKEN" \
      go test -tags=market ./internal/iap/providers/ \
        -run 'TestPlayReal|TestPlayAcknowledge' -v -timeout 120s
  ) && ok "Play 구매 검증과 acknowledge 통과" \
    || no "Play 검증 실패 — 위 로그를 확인해라"

  # acknowledge가 실제로 반영됐는지 다시 조회한다.
  # 호출이 성공해도 마켓 상태가 안 바뀌면 3일 뒤 자동 환불된다.
  echo "  acknowledge 반영을 재조회한다..."
  local token
  token="$(GOOGLE_APPLICATION_CREDENTIALS="$creds" \
    gcloud auth application-default print-access-token 2>/dev/null || true)"

  if [[ -z "$token" ]]; then
    warn "재조회용 토큰을 얻지 못했다. 위 test 결과로 판단해라"
    return
  fi

  local state
  state="$(curl -sS -H "Authorization: Bearer $token" \
    "https://androidpublisher.googleapis.com/androidpublisher/v3/applications/$PACKAGE/purchases/productsv2/tokens/$PLAY_ACTIVE_TOKEN" \
    2>/dev/null | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    print(d.get('acknowledgementState','?'))
except Exception:
    print('?')
" 2>/dev/null)"

  if [[ "$state" == *ACKNOWLEDGED* ]]; then
    ok "마켓이 ACKNOWLEDGED로 응답한다"
  else
    no "acknowledgementState=$state — 반영되지 않았다"
  fi
}

# ---------------------------------------------------------------- 2. RTDN 실연동
#
# Play Console UI에서만 등록할 수 있다. androidpublisher API에
# 설정 경로가 없다는 것을 discovery로 확인했다.
verify_rtdn() {
  echo "== 2. Play RTDN 실연동 =="

  local sub="play-iap-rtdn-push"

  if ! gcloud pubsub subscriptions describe "$sub" --project="$PROJECT" >/dev/null 2>&1; then
    no "push subscription이 없다: $sub"
    return
  fi
  ok "push subscription 존재"

  local endpoint
  endpoint="$(gcloud pubsub subscriptions describe "$sub" --project="$PROJECT" \
    --format='value(pushConfig.pushEndpoint)' 2>/dev/null)"
  if [[ "$endpoint" == *"/v1/iap/webhooks/play" ]]; then
    ok "push 엔드포인트: $endpoint"
  else
    no "push 엔드포인트가 이상하다: $endpoint"
  fi

  # Play Console에서 "테스트 알림 보내기"를 누른 뒤 실행하면
  # Google이 직접 보낸 메시지가 로그에 남는다.
  echo "  최근 10분 웹훅 수신 기록을 본다..."
  local hits
  hits="$(gcloud logging read \
    "resource.labels.service_name=\"platform-iap\" AND httpRequest.requestUrl:\"webhooks/play\"" \
    --project="$PROJECT" --limit=20 --freshness=10m \
    --format="value(httpRequest.status)" 2>/dev/null | grep -c . || true)"

  if [[ "${hits:-0}" -gt 0 ]]; then
    ok "웹훅 수신 ${hits}건"
    local bad
    bad="$(gcloud logging read \
      "resource.labels.service_name=\"platform-iap\" AND httpRequest.requestUrl:\"webhooks/play\" AND httpRequest.status>=400" \
      --project="$PROJECT" --limit=10 --freshness=10m \
      --format="value(httpRequest.status)" 2>/dev/null | grep -c . || true)"
    if [[ "${bad:-0}" -eq 0 ]]; then
      ok "4xx·5xx 없음"
    else
      no "실패 응답 ${bad}건 — Pub/Sub이 재전송 중일 수 있다"
    fi
  else
    warn "최근 수신이 없다. Play Console에서 테스트 알림을 보낸 뒤 다시 실행해라"
  fi
}

# ---------------------------------------------------------------- 3. AIT 인증서
#
# 파트너 콘솔에서 발급받아 Secret Manager에 올려야 한다.
# mTLS 배선 자체는 이미 검증했다.
verify_ait() {
  echo "== 3. AppsInToss 인증서 =="

  local missing=0
  for s in ait-client-cert ait-client-key; do
    if gcloud secrets describe "$s" --project="$PROJECT" >/dev/null 2>&1; then
      ok "Secret 존재: $s"
    else
      warn "Secret이 없다: $s"
      missing=1
    fi
  done

  if [[ "$missing" -eq 1 ]]; then
    warn "인증서를 발급받아 Secret Manager에 올려라 (체크리스트 3장)"
    return
  fi

  # 검증기가 실제로 조립됐는지 부팅 로그로 확인한다.
  echo "  platform-iap이 AIT를 조립했는지 본다..."
  local markets
  markets="$(gcloud logging read \
    "resource.labels.service_name=\"platform-iap\" AND jsonPayload.msg:\"결제 준비 완료\"" \
    --project="$PROJECT" --limit=1 --freshness=30m \
    --format="value(jsonPayload.markets)" 2>/dev/null)"

  if [[ "$markets" == *apps_in_toss* ]]; then
    ok "AIT 검증기 조립됨 (markets=$markets)"
  else
    no "AIT가 조립되지 않았다 (markets=$markets). Secret을 서비스에 붙였는지 확인해라"
  fi
}

# ---------------------------------------------------------------- 실행

target="${1:-all}"

case "$target" in
  play)  verify_play_purchase ;;
  rtdn)  verify_rtdn ;;
  ait)   verify_ait ;;
  all)
    verify_play_purchase; echo
    verify_rtdn; echo
    verify_ait
    ;;
  *)
    echo "사용법: $0 [all|play|rtdn|ait]" >&2
    exit 2
    ;;
esac

echo
echo "통과 $pass · 실패 $fail · 대기 $skip"

if [[ "$fail" -gt 0 ]]; then
  echo "실패한 항목을 먼저 해결해라." >&2
  exit 1
fi
if [[ "$skip" -gt 0 ]]; then
  echo "대기 중인 항목이 있다. 전환 전에 마쳐야 한다." >&2
  exit 2
fi

echo "전환 준비 완료. docs/06-release/go-live-checklist.md 4장으로 간다."
