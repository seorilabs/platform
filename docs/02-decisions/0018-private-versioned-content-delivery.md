# ADR 0018: 보호 콘텐츠는 private GCS 릴리스와 Platform 선택 API로 전달한다

- 상태: Accepted
- 날짜: 2026-08-18

## 맥락

운글의 해석 코퍼스 1,061건은 앱 번들에 들어가면 APK, AAB, IPA, Web 자산에서 그대로
복구할 수 있다. 기존 바이너리에 들어간 원문은 서버에서 회수할 수 없고, signed URL이나
전체 manifest를 클라이언트에 주는 방식도 다운로드 경로만 바꿀 뿐 전체 원문 노출을 막지
못한다.

계산 코어는 출생 정보를 기기 밖으로 보내지 않는 현재 경계를 유지해야 한다. 반면 콘텐츠
전달은 사용자 인증, App Check, 요청 한도와 심화 권한을 함께 판정해야 한다. Platform이
코퍼스 원본까지 소유하면 앱 콘텐츠의 작성·검수·릴리스 책임이 공통 인프라 저장소로 섞인다.

## 결정

1. 코퍼스 source of truth와 게시 workflow는 운글 저장소가 소유한다.
2. immutable 릴리스는 private GCS의
   `{staging|production}/ungeul/releases/{contentVersion}` 아래에 둔다. `active.json`만
   generation-match 조건부 쓰기로 전환한다. public ACL과 signed URL은 사용하지 않는다.
3. `contentVersion`은 릴리스 `content.json` 원본 바이트의 SHA-256이다. Platform은
   `active.json`, `manifest.json`, `content.json`의 버전·스키마·SHA·개수·ID 중복·빈 문구·
   `free|deep` 분류를 모두 검증한다.
4. Platform은 앱 레지스트리의 bucket/prefix/한도를 읽고 런타임 계정의 읽기 권한으로만
   릴리스를 가져온다. 정상본은 5분 캐시한다. 새 활성본이 손상됐으면 거부하고 직전 정상본을
   계속 제공한다.
5. 공개 API는 버전 조회, 파생 명식 해설 선택, 사전 단일 항목 조회만 제공한다. 원본
   manifest, 전체 ID 목록, 임의 article ID 배열 조회 API는 만들지 않는다.
6. 콘텐츠 API는 Platform session과 Firebase App Check를 모두 요구한다. 세션의 app과
   `X-Seori-App`이 다르면 거부한다.
7. 클라이언트는 사주팔자와 콘텐츠 선택에 필요한 파생 사실만 보낸다. 생년월일,
   출생시각, 장소, 프로필명은 보내지 않는다. selector는 일주-주제 고정 조합, 허용 enum,
   축별 개수, 중복, 명식 지지에 없는 관계 쌍 등 서버에서 다시 확인할 수 있는 불변식을
   검증한 뒤 좌표를 만든다.
8. 출생 원정보가 없으면 대운 시작 시점처럼 서버가 독립 재계산할 수 없는 파생값이 있다.
   이 값의 출처까지 증명한다고 주장하지 않는다. 구조·조합 검증과 사용자별 신규 명식
   10건/일 제한으로 대량 열거를 통제한다. 같은 `readingKey` 재조회는 새 명식 한도에서
   제외한다. 사전은 100건/일이다.
9. 심화 본문은 서버 권한만 신뢰한다. AdMob은 `server_verified` claim을
   `readingKey/deepKey`에 한 번만 바인딩한다. 열람권은 IAP 원장의 활성 source별 사용량에서
   단위를 멱등·원자적으로 차감하고 잠금 해제도 그 source 해시에 묶는다. 매 조회에서 source가
   아직 해당 사용자의 활성 구매인지 다시 확인하므로 환불·소유권 이전을 반영하고, 환불된 구매의
   사용량이 새 구매를 깎지 않는다. 범용 시즌 패스 기능은 레지스트리의 연도별 entitlement를
   매 요청 확인하지만 운글은 사용하지 않는다(ADR 0019). 운영 설정이나 검증 근거가 없으면
   잠긴 상태를 유지한다.
10. 앱 캐시는 파생 사실의 해시만 키로 쓰고 최근 5개, 7일로 제한한다. 콘텐츠 버전이
    바뀌면 폐기한다. 이 때문에 환불·권한 회수가 이미 저장된 해설에 반영되는 최장 지연은
    7일이다.

## 릴리스 형식

`active.json`:

```json
{"schemaVersion":1,"contentVersion":"sha256-<64 hex>"}
```

`releases/{contentVersion}/manifest.json`:

```json
{
  "schemaVersion": 1,
  "contentVersion": "sha256-<64 hex>",
  "contentSha256": "<64 hex>",
  "itemCount": 1061
}
```

같은 디렉터리의 `content.json`은 `schemaVersion`과 `items`를 가진다. 각 item은 최소
`id`, `text`, `access: free|deep`, `contexts: reading|term|internal[]`를 가진다. `internal`은
릴리스에는 보존하지만 공개 selector와 사전 API 어느 쪽에서도 전달하지 않는 좌표다.
`term` 문맥은 `free` 항목에만 허용한다. `deep` 본문을 리딩 권한과 무관한 단일 사전
경로로 우회 조회할 수 없도록 심화 항목은 `reading` 문맥에만 둔다.

## 결과

- 콘텐츠 문구만 바뀌면 앱 심사 없이 immutable 릴리스와 `active.json` 전환으로 반영할 수
  있다.
- 스키마나 selector 좌표 규칙 변경은 Platform과 앱의 호환 배포가 먼저 필요하다.
- GCS, Firebase, App Check, AdMob, IAP 콘솔과 IAM은 코드 merge만으로 활성화되지 않는다.
  별도 승인·배포·실기기 검증을 완료하기 전에는 운영 준비 상태로 보지 않는다.
