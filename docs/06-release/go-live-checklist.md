# 전환 체크리스트

lizard-tycoon을 Firebase Functions IAP에서 플랫폼으로 옮기는 절차.

**전환 스위치는 꺼진 채 커밋되어 있다.** 아래를 통과하기 전에 켜지 않는다.

## 현재 상태

구현은 전부 끝났고 검증도 코드로 할 수 있는 것은 전부 했다.
남은 셋은 외부 시스템에서 사람이 조작해야 한다.

**Apple·Play 두 마켓으로 먼저 전환한다.** AIT는 인증서를 받은 뒤 붙인다.

| 항목 | Apple | Play | AIT |
|---|---|---|---|
| 마켓 인증 | ✅ | ✅ | 보류 |
| 실제 구매 검증 | ✅ | ✅ | — |
| shadow 대조 (orderKey 일치) | ✅ | ✅ | — |
| 웹훅 (실데이터) | ✅ | ✅ | 해당 없음 |
| 완료 호출 경로 | ✅ | ✅ | 클라이언트 담당 |
| 완료 **반영** 확인 | ✅ | ✅ | — |
| RTDN 실연동 | 해당 없음 | ✅ | — |
| mTLS 배선 | — | — | ✅ |
| API 계약 문서 대조 | — | — | ✅ |

**Apple·Play 두 마켓은 전 항목을 통과했다.** AIT만 인증서를 기다린다.

Play 완료 반영은 실제 활성 구매로 확인했다.

```
providerOrderId       GPA.3380-7033-5994-35388
state                 active
acknowledgementState  ACKNOWLEDGED   ← 마켓에 다시 물어 확인
orderKey              42a8a5ae...dea02a98  ← 기존 Functions 원장과 일치
```

이 검증에서 **acknowledge 성공(204)을 실패로 처리하던 결함**을 찾았다.
환불된 구매로는 드러나지 않는 경로였다.

**RTDN은 실연동까지 확인했다.** Google이 직접 보낸 알림을
`platform-iap`이 받아 처리했다.

```
알림 반영  kind=one_time_product  state=revoked  known=false  → 200
```

`known=false`는 아직 shadow 단계라 우리 원장에 없는 주문이라는 뜻이다.
불변식 10대로 신규 지급 없이 tombstone만 남겼다.

검증 근거는 [market-verification.md](../07-qa/market-verification.md)에 있다.

---

## 1. Play 활성 구매 만들기 — 기기 필요

**왜 필요한가.** acknowledge가 실제로 반영되는지 확인해야 한다.
Play는 3일 안에 acknowledge하지 않으면 자동 환불한다. 이 경로가
막혀 있으면 유저는 산 물건을 잃고 우리는 매출을 잃는다.

**토큰은 `orders.get`으로 얻을 수 있다.** 예전에 이 API가 없다고
적어뒀는데 틀렸다. 구매가 일어난 뒤 orderId만 알면 토큰이 나온다.

```bash
curl -H "Authorization: Bearer $(gcloud auth application-default \
    print-access-token --scopes=https://www.googleapis.com/auth/androidpublisher)" \
  "https://androidpublisher.googleapis.com/androidpublisher/v3/applications/\
com.seorilabs.lizardtycoon/orders/<orderId>"
```

응답의 `purchaseToken`이 그것이다. orderId는 원장의 `providerOrderId`에
있다. **다만 구매 자체는 여전히 기기에서 해야 한다** — 아직 사지 않은
상품의 토큰을 만들어내는 API는 없다.

### 먼저 — sideload 빌드로는 구매할 수 없다

기기에 `adb install`로 올린 빌드는 상품 조회가 되지 않는다.
BillingClient 연결은 성공하고(`response=0`) 상품만 비어서 온다.

```
[iap] product query: response=0 details=0 unfetched_statuses=[4]
```

`4`는 `ITEM_UNAVAILABLE`이다. Play Console 쪽이 멀쩡해도 이렇게 된다 —
실제로 확인했을 때 상품은 둘 다 `ACTIVE` / `KR AVAILABLE` / 3,300원이었고
internal 트랙에 같은 versionCode가 `completed`로 올라가 있었다.

원인은 설치 경로다.

```bash
adb shell dumpsys package com.seorilabs.lizardtycoon | grep installer
# installerPackageName=null   ← Play Store를 거치지 않았다
```

**Play Store 내부 테스트 링크로 설치해야 한다.** 그래야
`installerPackageName=com.android.vending`이 되고 Billing이 상품을 준다.

### 그 다음 — 트랙의 빌드가 최신이어야 한다

Play Store로 설치하면 Billing은 살아나지만, 트랙에 올라간 빌드가
오래되면 이번엔 앱 쪽 IAP 어댑터가 fail-closed된다. 상점 버튼이
"가격 확인 중"이 아니라 **"준비 중"**이면 이쪽이다.

```gdscript
# main.gd — 어댑터가 없거나 is_available()이 false면 "준비 중"
func _premium_purchase_available() -> bool:
	return _purchase != null and _purchase.has_method("is_available") \
		and bool(_purchase.is_available())
```

릴리스 빌드는 로그를 내보내지 않아 어느 조건인지 특정할 수 없다.
대신 **서버 쪽에서 앱이 무엇을 호출했는지** 보면 절반은 가려진다.

```bash
gcloud logging read 'resource.labels.service_name="listiapentitlements"' \
  --project=lizard-tycoon --limit=10 --freshness=30m \
  --format="value(timestamp,httpRequest.status)"
```

200이 찍혀 있으면 인증·세션·Functions는 정상이고 BillingClient만
남는다. APK에서 config를 직접 뜯어 확인할 수도 있다.

```bash
adb pull "$(adb shell pm path <pkg> | grep assetPack | sed 's/package://')" assets.apk
unzip -p assets.apk assets/firebase/iap-client.android.config.json
```

`platform`, `app_check_mode`, `products`, `functions`가 다 있어야 한다.

### 가장 먼저 볼 것 — Play 계정 국가

상품은 판매 지역이 설정된 국가의 계정에만 내려간다. **계정 프로필
국가가 다르면 상품 조회가 통째로 비어서 온다.**

```
[iap] product query: response=0 details=0 unfetched_statuses=[4]
```

`response=0`이라 성공처럼 보이고 상품만 없어서, Play Console 설정이나
빌드를 의심하며 시간을 쓰게 된다. 실제로 그렇게 두 시간을 썼다.

**Play Store → 프로필 → 설정 → General → Account and device
preferences → Country and profiles**에서 확인한다.

```
United States  · SEO IL HWAN   ✓   ← 이 상태에서 KR 전용 상품은 안 온다
South Korea    · 서일환         ○
```

상품 쪽 판매 지역은 API로 본다.

```bash
curl ... ".../oneTimeProducts/sp_shootingstar_tokay"
# purchaseOptions[].regionalPricingAndAvailabilityConfigs[].regionCode
```

둘이 맞아야 가격이 뜬다. 계정 국가 변경은 결제수단·잔액·구독에
영향이 있고 재변경이 1년간 제한될 수 있어 **계정 소유자가 판단할
일이다.** 급하면 상품에 해당 국가를 추가하는 쪽이 되돌리기 쉽다.

### 여기까지 다 정상인데도 "가격 확인 중"이면

코드·설정 쪽에서 볼 수 있는 것은 전부 확인된 상태다. 실제로 아래를
전부 통과하고도 상품이 오지 않는 상황을 겪었다.

| 확인한 것 | 결과 |
|---|---|
| 상품 상태 (`oneTimeProducts` API) | `ACTIVE` · KR `AVAILABLE` · 3,300원 |
| 트랙 배포 | internal에 `completed` |
| 설치 경로 | `installerPackageName=com.android.vending` |
| APK 내 IAP config | `platform`·`products`·`functions` 전부 정상 |
| 앱→서버 호출 | `listIapEntitlements` 200 |
| 어댑터 | `is_available()` 통과 ("준비 중"이 아님) |

남은 것은 Play Console에서만 보이는 둘이다.

1. **설정 → 라이선스 테스트**에 결제할 계정이 등록돼 있는가.
   등록돼 있으면 실제 청구 없이 구매할 수 있다. 없어도 구매 자체는
   되지만 실제 결제가 발생한다.
2. **앱이 "unreviewed" 상태인가.** 스토어 등록정보·콘텐츠 등급 등이
   미완성이면 Play가 인앱 상품을 앱에 매핑하지 않는 경우가 있다.

릴리스를 방금 공개했다면 **전파를 기다리는 것도 방법이다.** Play가
앱-상품 매핑을 갱신하는 데 몇 시간이 걸린다.

앱을 지우고 다시 받으면 저장 데이터가 날아가므로 먼저 백업한다.

```bash
adb shell run-as com.seorilabs.lizardtycoon \
  cat files/save.json > save_backup.json
```

### 절차

1. lizard-tycoon을 Play 내부 테스트 트랙에 올린다
2. 라이선스 테스터 계정으로 **Play Store 내부 테스트 링크를 통해** 설치한다
3. `sp_galaxy_gecko`를 구매한다
4. **acknowledge하지 않은 상태로** 토큰을 확보한다
   - 앱이 자동으로 acknowledge하면 검증 대상이 사라진다
   - `seori-platform.config.json`의 `enabled`가 `false`인 빌드로
     구매하면 기존 Functions가 처리하므로, 그 전에 토큰을 로그로 뽑는다
5. 토큰을 넘겨주면 아래를 확인한다

```bash
export PLAY_REAL_PURCHASE_TOKEN="<토큰>"
go test -tags=market ./internal/iap/providers/ -run TestPlayReal -v
go test -tags=market ./internal/iap/providers/ -run TestPlayAcknowledge -v
```

기대: `state: active`, `completion: google_acknowledge`,
acknowledge 후 재조회 시 `acknowledgementState: ACKNOWLEDGED`

---

## 2. RTDN 실연동 — Play Console 필요

**왜 필요한가.** 지금 검증은 우리가 Pub/Sub에 직접 넣은 것이다.
Google이 실제로 보내는지는 확인되지 않았다.

**왜 코드로 못 하는가.** RTDN topic 설정 API가 discovery에 없다.
Play Console UI 전용이다.

### 절차

Play Console → 앱 → **수익 창출 설정** → 실시간 개발자 알림

```
projects/seorilabs-platform/topics/play-iap-rtdn
```

topic은 이미 만들어져 있고 push subscription도 걸려 있다.

> **주의**: 등록하면 기존 Firebase Functions로 가던 RTDN이 여기로
> 온다. 전환 시점에 하는 것이 맞고, 그전에 하면 기존 시스템이
> 환불 알림을 못 받는다.

### 등록한 순간부터 기존 Functions는 RTDN을 못 받는다

**이건 전환 스위치와 무관하다.** RTDN 수신처는 Play Console 설정이고,
앱이 어느 서버를 부르는지(`seori-platform.config.json`의 `enabled`)와
별개로 정해진다.

우리 topic을 등록해 두고 `enabled: false`로 두면 이렇게 갈린다.

```
환불/구매 알림  →  seorilabs-platform  (우리 플랫폼이 받아 처리)
앱의 검증 요청  →  lizard-tycoon Functions  (기존 원장을 본다)
```

기존 원장은 환불을 영영 모른다. 유저 화면에는 회수된 상품이 계속
"보유 중"으로 남는다. 실제로 겪었다 — 게코 2건을 환불했는데 우리
플랫폼은 4건을 받았고 lizard-tycoon Functions는 0건이었다.

전환 전까지는 누락된 알림을 기존 topic으로 중계해야 한다.

```bash
# 환불된 주문의 purchaseToken을 얻어
curl ... ".../purchases/voidedpurchases?maxResults=10"

# 기존 topic으로 같은 형식의 알림을 넣는다
#   productType: 2 (일회성), refundType: 1 (전액)
curl -X POST -d @relay.json \
  "https://pubsub.googleapis.com/v1/projects/lizard-tycoon/topics/play-iap-rtdn:publish"
```

**전환 순서를 정할 때 이 점을 고려한다.** RTDN 등록을 앱 전환보다
먼저 하면 그 사이 환불이 기존 원장에 반영되지 않는다. 미론칭이라
지금은 감당할 수 있지만, 실사용자가 있으면 순서를 뒤집거나 중계를
자동화해야 한다.

### 콘솔 등록만으로는 오지 않는다 — org policy

Play Console에 topic을 적어도 **Google이 그 topic에 publish할 권한이
없으면 알림은 한 건도 오지 않는다.** 콘솔은 이 실패를 알려주지 않는다.

```bash
gcloud pubsub topics add-iam-policy-binding play-iap-rtdn \
  --project=seorilabs-platform \
  --member="serviceAccount:google-play-developer-notifications@system.gserviceaccount.com" \
  --role="roles/pubsub.publisher"
```

이 부여가 조직 정책에 막힌다.

```
constraints/iam.allowedPolicyMemberDomains
  is not in permitted organization
```

디렉토리 `C02f93h8p`(seorilabs.com)만 허용하는데 Google 소유 시스템
계정은 거기 속하지 않는다. 프로젝트 수준에서 예외를 둬야 한다.

```bash
cat > /tmp/policy.yaml <<'EOF'
name: projects/seorilabs-platform/policies/iam.allowedPolicyMemberDomains
spec:
  inheritFromParent: false
  rules:
  - allowAll: true
EOF
gcloud org-policies set-policy /tmp/policy.yaml --project=seorilabs-platform
```

기존 `lizard-tycoon` 프로젝트 topic에는 이 권한이 있다. 정책이
적용되기 전에 설정됐기 때문이고, 그래서 지금까지 동작했다.

**등록 여부는 topic IAM으로 확인한다.** 비어 있으면 콘솔에 무엇을
적었든 알림은 오지 않는다.

```bash
gcloud pubsub topics get-iam-policy play-iap-rtdn --project=seorilabs-platform
```

등록 후 "테스트 알림 보내기"를 누르면 확인할 수 있다.

```bash
gcloud logging read 'resource.labels.service_name="platform-iap"
  AND httpRequest.requestUrl:"webhooks/play"' \
  --project=seorilabs-platform --limit=5 --freshness=5m \
  --format="value(httpRequest.status,jsonPayload.msg)"
```

기대: `200`, `알림 반영`

---

## 3. AppsInToss mTLS 인증서 — 뒤로 미룬다

**Apple·Play 두 마켓으로 먼저 전환하기로 했다.** AIT는 인증서를
받은 뒤 붙인다.

이 상태에서 AIT 결제는 전부 이렇게 거부된다.

```go
v, ok := s.verifiers[proof.Platform]
if !ok {
    return Outcome{}, platformerr.Newf(platformerr.CodePlatformUnavailable,
        "%s 결제는 아직 준비 중이에요", proof.Platform)
}
```

`platform_unavailable` → HTTP 503. 나머지 두 마켓은 영향받지 않는다.
lizard-tycoon은 현재 AIT 빌드가 없어 실사용자 영향이 없다.

검증할 때 이 부재를 대기가 아니라 의도로 다루려면 플래그를 준다.

```bash
AIT_DEFERRED=1 scripts/verify_go_live.sh
```

cert와 key 중 **한쪽만 있으면 이 모드에서도 실패로 잡는다.** 반쪽만
올라간 상태로 배포하면 부팅에서 터진다.

인증서를 받으면 아래 절차로 붙인다.

## 3-1. 인증서를 받은 뒤 — 파트너 콘솔 필요

**왜 필요한가.** AIT는 mTLS로 인증한다. 인증서가 없으면 검증기가
조립되지 않고 AIT 결제만 `platform_unavailable`로 거부된다.

**배선은 이미 검증했다.** 자체 서명 CA로 실제 TLS 핸드셰이크를
통과시켰고, 인증서 없는 연결·다른 CA·서버 미검증이 전부 거부되는
것까지 확인했다. **인증서를 받는 즉시 동작한다.**

### 절차

1. AppsInToss 파트너 콘솔에서 클라이언트 인증서를 발급받는다
2. Secret Manager에 올린다

```bash
gcloud secrets create ait-client-cert --project=seorilabs-platform \
  --data-file=client.crt
gcloud secrets create ait-client-key --project=seorilabs-platform \
  --data-file=client.key

for s in ait-client-cert ait-client-key; do
  gcloud secrets add-iam-policy-binding $s --project=seorilabs-platform \
    --member="serviceAccount:platform-iap@seorilabs-platform.iam.gserviceaccount.com" \
    --role="roles/secretmanager.secretAccessor"
done
```

3. `platform-iap`에 붙인다

```bash
gcloud run services update platform-iap \
  --project=seorilabs-platform --region=asia-northeast3 \
  --update-secrets="IAP_TOSS_CLIENT_CERT=ait-client-cert:latest,IAP_TOSS_CLIENT_KEY=ait-client-key:latest"
```

부팅 로그에 `결제 준비 완료 markets=[... apps_in_toss]`가 나오면 된다.

---

## 자동 검증

셋을 조작한 뒤 한 번에 확인한다.

```bash
PLAY_ACTIVE_TOKEN="<기기에서 얻은 토큰>" scripts/verify_go_live.sh

# 일부만
scripts/verify_go_live.sh rtdn
scripts/verify_go_live.sh ait
```

| exit | 뜻 |
|---|---|
| 0 | 전부 통과. 전환으로 넘어간다 |
| 1 | 실패한 항목이 있다 |
| 2 | 아직 조작하지 않은 항목이 있다 |

Play 항목은 acknowledge **호출 성공**만 보지 않고 마켓에 다시 물어
`ACKNOWLEDGED`로 바뀌었는지 확인한다. 호출이 성공해도 반영되지
않으면 3일 뒤 자동 환불된다.

## 4. 전환 — 위 셋이 끝난 뒤

### 4.0 배포는 GitHub Actions가 한다

수동 배포는 대상을 빠뜨린다. 실제로 두 번 겪었다 — 네 서비스가 `p3`와
`p2`로 갈렸던 적이 있고, 2026-08-02에는 서비스 넷이 `p15`인데
`platform-worker`만 `p9f`로 22시간 뒤처져 있었다. 그 사이 `ledger.go`와
`verify/service.go`가 바뀌었다.

배포 대상은 다섯이고 **전부 같은 이미지를 써야 한다.**

```
platform-api  platform-iap  platform-ingest  platform-admin   (Cloud Run 서비스)
platform-worker                                               (Cloud Run Job)
```

`.github/workflows/deploy.yml`이 이미지를 한 번만 만들어 commit SHA
태그로 다섯 대상에 올리고, 올린 뒤 실제로 같은 태그가 됐는지 다시
확인한다. 어긋나면 실패한다.

| 단계 | 트리거 |
|---|---|
| 빌드·push | main 병합 시 자동 |
| 배포 | Actions에서 **Deploy 워크플로를 사람이 실행** |

실결제 원장이라 배포 직전에 사람이 한 번 끊는다. 병합만으로는 배포되지
않는다.

승인 게이트를 GitHub environment로 걸지 않은 이유는 요금제다. private
repo에서 required reviewer는 이 요금제로 쓸 수 없고 API가 422로 거부한다.
그래서 배포 job을 `workflow_dispatch`에서만 돌게 해서 같은 효과를 낸다.

빌드 job은 같은 commit의 이미지가 이미 있으면 건너뛴다. 병합 직후
실행하면 빌드를 기다리지 않고 배포로 바로 들어간다.

### 배포 실행

```
Actions → Deploy → Run workflow → Branch: main
```

### 러너

배포는 GitHub-hosted 러너에서 돈다. 짧게 끝나고 x64라 amd64가
네이티브라 크로스컴파일도 필요 없다. ARC는 최대 3대라 짧은 작업이
자리를 차지하면 다른 작업을 밀어낸다. 조직 기본값인 ARC arm64 라우팅을
여기서만 의도적으로 벗어난다.

GitHub Actions에 자동 fallback은 없다. 쿼타가 차면 명시적으로 넘긴다.

| 상황 | 방법 |
|---|---|
| 일회성 | Run workflow의 `runner` 드롭다운에서 `arc` |
| 지속 | 저장소 변수 `PLATFORM_DEPLOY_RUNNER=arc` |

둘 중 하나라도 `arc`면 ARC로 간다. 쿼타가 풀리면 변수를 지운다.

ARC로 넘어가면 빌드는 `seorilabs-rpi-arm64-dind`(Docker 필요), 배포는
`seorilabs-rpi-arm64`(gcloud만 사용)로 간다. arm64에서도 Dockerfile이
`FROM --platform=$BUILDPLATFORM`이라 QEMU 없이 amd64로 크로스컴파일한다.

Go 체크(`checks-go.yml`)는 ARC에 그대로 둔다.

배포 상태는 이렇게 본다.

```bash
gcloud run services list --project=seorilabs-platform \
  --region=asia-northeast3 \
  --format="table(metadata.name, spec.template.spec.containers[0].image.basename())"

gcloud run jobs list --project=seorilabs-platform \
  --region=asia-northeast3 \
  --format="table(metadata.name, spec.template.spec.template.spec.containers[0].image.basename())"
```

**워커를 빠뜨리지 않는다.** 서비스만 보는 명령으로는 드리프트가 보이지
않는다. 위 두 명령을 항상 같이 본다.

### 4.1 서비스 배포

production 환경으로 올린다. **`platform-api`와 `platform-iap`의
`PLATFORM_FS_PREFIX`가 같아야 한다.** 다르면 서로 다른 컬렉션을 보고,
점검 모드를 켜도 클라이언트에 아무 변화가 없다. 리허설에서 실제로 겪었다.

```bash
gcloud run services describe platform-api --region=asia-northeast3 \
  --format="value(spec.template.spec.containers[0].env)" | grep -i prefix
gcloud run services describe platform-iap --region=asia-northeast3 \
  --format="value(spec.template.spec.containers[0].env)" | grep -i prefix
```

`IAP_LEDGER_ENVIRONMENT`도 `production`으로 바꾼다. Apple 환경과
어긋나면 부팅이 실패한다 — 그게 안전장치다.

### 4.2 앱 설정

`lizard-tycoon/firebase/seori-platform.config.json`

```json
{
  "enabled": true,
  "baseUrl": "https://platform-api-....run.app",
  "appId": "lizard-tycoon"
}
```

### 4.3 shadow 기간

**기존 Firebase Functions를 지우지 않는다.** 일정 기간 두 경로가
같은 결과를 내는지 대조한다. orderKey 일치는 이미 확인했지만
운영 중 데이터로 다시 본다.

```bash
# 플랫폼 원장
go run ./cmd/fs ls "processed_orders" --limit=20

# 기존 원장
GOOGLE_CLOUD_PROJECT=lizard-tycoon \
  go run ./cmd/fs ls "iap_environments/production/processed_orders" --limit=20
```

### 4.4 롤백

문제가 생기면 `enabled`를 `false`로 되돌리고 재배포한다.
코드를 되돌릴 필요가 없다.

---

## 배포 후 확인

```bash
ADMIN_URL="$(gcloud run services describe platform-admin \
  --region=asia-northeast3 --format='value(status.url)')"
TOKEN="$(gcloud auth print-identity-token --audiences="$ADMIN_URL")"

curl -sS -H "Authorization: Bearer $TOKEN" "$ADMIN_URL/v1/admin/health"
```

`deadLetterCount`가 0이 아니면 마켓에 완료를 알리지 못한 주문이 있다.

### 워커

`platform-worker` Job이 5분마다 완료 outbox를 처리한다.
**이게 멈추면 Play가 3일 뒤부터 자동 환불을 시작한다.**

```bash
gcloud scheduler jobs describe platform-worker-5m \
  --project=seorilabs-platform --location=asia-northeast3 \
  --format="value(state,status.code,lastAttemptTime)"

gcloud run jobs executions list --job=platform-worker \
  --project=seorilabs-platform --region=asia-northeast3 --limit=5 \
  --format="table(metadata.name, status.succeededCount, status.failedCount)"
```

`state=ENABLED`이고 실행이 계속 성공해야 한다. 로그에서 처리량을 본다.

```
완료 재시도 종료  claimed=0 completed=0 failed=0
```

Job은 `RunOnce`로 한 번 돌고 끝난다. 남은 항목은 다음 실행이 집으므로
한 번에 다 처리되지 않아도 정상이다. Firebase의 `onSchedule`과 달리
단일 실행이 인프라로 보장되지 않지만, lease 기반 claim이 있어
동시에 여러 개가 돌아도 중복 완료는 나지 않는다.

## 관련

- [BREAK-GLASS](../08-ops/BREAK-GLASS.md) — 백오피스 없이 조작하기
- [마켓 검증 상태](../07-qa/market-verification.md) — 무엇을 어떻게 확인했나
