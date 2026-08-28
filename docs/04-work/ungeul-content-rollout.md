# 운글 private 콘텐츠 전환 운영 체크리스트

코드 계약은 ADR 0018과 `/v1/content/*`에 있다. 이 문서는 코드 merge와 외부 운영 상태를
섞지 않기 위한 실행 체크리스트다. 2026-08-18 현재 아래 외부 값은 저장소와 로컬 자격증명
카탈로그에서 확인되지 않았으므로 임의로 만들거나 레지스트리에 placeholder를 넣지 않는다.

## merge 전에 확정할 공개 식별자

- 운글 Firebase project ID
- 같은 프로젝트의 custom-token signer service account 이메일
- private 콘텐츠 GCS bucket 이름과 `ungeul` prefix
- Android/iOS Firebase 앱 등록 파일과 App Check provider 설정
- 콘텐츠용 AdMob rewarded placement와 reward key
- 소모성 열람권 entitlement ID와 마켓별 SKU

확정 뒤 `registry/apps/ungeul.json`에 다음 계약을 실제 값으로 등록한다.

- `features.content`, `features.firebase_custom_token_bridge`: `true`
- 광고/IAP을 실제로 사용할 때만 각각 `features.ads`, `features.iap`: `true`
- `require_app_check`: `true`
- `content.reading_daily_limit`: `10`
- `content.term_daily_limit`: `100`
- `content.bucket`, `content.prefix`
- `cors_origins`: Android 기본 `https://localhost`, iOS 기본 `capacitor://localhost`와 실제로
  배포하는 웹 origin만 명시
- 운영 준비가 끝난 권한만 `reward_key`, `ticket_entitlement_id`,
  `ticket_units_per_purchase`에 등록. 운글은 `season_entitlements`를 사용하지 않는다.

레지스트리 파일 추가만으로 런타임이 바뀌지 않는다. `cmd/regsync` dry-run과 실제 sync를
분리하고, sync 뒤 Firestore readback을 확인한다.

## IAM과 배포 gate

- 운글 저장소의 GitHub Environment `content-staging`과 `content-production`에 승인자를
  설정하고, 게시 workflow는 `main` ref에서만 실행한다.
- bucket과 객체에 public ACL을 두지 않는다. signed URL도 만들지 않는다.
- `platform-api@seorilabs-platform.iam.gserviceaccount.com`에는 대상 bucket 읽기만 부여한다.
- 운글 게시용 WIF service account에는 해당 bucket의
  `staging/ungeul`·`production/ungeul` prefix 쓰기만 부여한다.
- Platform staging 배포와 registry sync가 먼저다.
- 운글 workflow로 staging immutable 릴리스를 게시한 뒤 `active.json` generation readback과
  Platform `/v1/content/version` SHA 일치를 확인한다.
- production은 Platform 승인 배포, production 릴리스 게시, 코퍼스 없는 앱 산출물 순서다.

## 실패 폐쇄 검증

- 세션 없음, App Check 없음/위조, 다른 앱 세션은 각각 거부한다.
- 손상된 active/manifest/content 전환은 직전 정상본을 계속 제공한다.
- 신규 명식 11번째와 사전 101번째는 429, 같은 readingKey 재조회는 허용한다.
- AdMob은 `server_verified` claim만 열고, claim 재사용은 다른 deepKey에 바인딩되지 않는다.
- 같은 명식의 `flow:{year}`는 광고 보상 1회 또는 열람권 1단위로 세운과 12개월 월운을
  함께 열며 두 섹션을 따로 차감하지 않는다.
- 열람권은 같은 request key 재시작에 중복 차감하지 않고, 해제 기록에 묶인 구매 source의
  환불·소유권 이전을 다음 온라인 조회에서 반영한다.
- 앱 실기기에서 401 갱신, 403 잠금, 429 안내, 7일/5개 캐시와 버전 폐기를 확인한다.

GCS 생성, IAM 변경, registry sync, Cloud Run 배포, Firebase/AdMob/IAP 콘솔 변경과 실기기
검증은 이 PR의 코드 구현과 별도 상태다.
