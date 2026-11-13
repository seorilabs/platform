# My Little Parasite 우편 권리 연결

사용자가 승인한 게임 설계는 [my-little-parasite의 ac95d68 잔여 출시 설계](https://github.com/seorilabs/my-little-parasite/blob/ac95d68d7995bc602fcc310e160edff7c8fd1e0f/docs/design/2026-11-13-launch-completion/design.md)다. Android는 배너만 사용하고 구매를 채택하지 않는다. 이 문서는 그 범위의 서버 구현과 미반영 상태를 기록하며 새로운 운영 배포 승인을 주장하지 않는다.

`iap:false`, `inbox:true` 앱은 서버가 지급한 영구 권리를 재로그인 후 읽을 수 있어야 한다. 기존 GET `/v1/iap/entitlements`는 구매 기능만 허용해 이 경우도 403이었다. 조회만 우편 채택 앱에 허용하고, 결제 검증·마켓 계정 참조·영수증 검증·판매는 계속 금지한다. 우편 앱에는 앱별 원장을 명시적으로 구성하고 누락 시 공유 기본 원장으로 우회하지 않는다. production과 sandbox를 분리한다.

검증은 결함을 재현한 단위 검사부터 시작했다. 앱·사용자·환경 격리, 회수 반영, 읽기로 지급하지 않음, 구매 금지, 원장 누락 차단과 실제 bootstrap 조합을 검사했다. `go test ./...`와 `go vet ./internal/iap/verify ./cmd/platform` 통과는 개발 환경 증거이며 실제 Firestore·SDK 성공을 뜻하지 않는다. PR의 확인된 서비스 런타임 CI가 전체 서비스 회귀를 담당한다.

레지스트리 파일은 앱 전용 Firebase와 공개 OAuth audience, GA4 속성 564191533만 참조한다. 무료 우편의 허용 권리 세 개를 선언하고 `markets:[]`, `iap:false`를 유지한다. AdMob은 앱이 직접 사용하므로 공통 `features.ads`는 false다. 고정된 분석 이벤트 이름만 허용하며 본문·추억·이름을 보내지 않는다. GA4 Web measurement ID나 MP secret을 선언하지 않아 중복 중계·누락 secret 전송을 켜지 않는다.

## 실제 공급자 상태와 적용 경계

2026-11-13 UTC API 확인: Firebase 익명 인증 enabled, Firestore `(default)`는 asia-northeast3의 native database, Analytics 속성 564191533은 이 앱의 Android·iOS 두 앱과 연결됐다. 공개 config는 앱 레지스트리 미반영으로 403 `app_unknown`이었다. `platform-auth@my-little-parasite.iam.gserviceaccount.com`은 아직 존재하지 않는다. 이 파일의 active는 적용할 상태이며 실제 반영 완료가 아니다.

운영 적용 전에 정확한 배포 이미지와 아래 최소 앱 범위를 검토한다.

- 앱 프로젝트의 키 없는 `platform-auth` service account 생성. 장기 private key는 만들지 않는다.
- API 런타임 SA에는 이 SA 리소스의 `roles/iam.serviceAccountTokenCreator`만 부여한다. 다른 앱이나 프로젝트 전체 Token Creator를 부여하지 않는다.
- 실제 deletion worker runtime SA를 읽어 확인한 뒤 같은 SA 리소스에 impersonation 권한을 부여한다. 앱 SA는 이 앱 프로젝트의 Firebase Auth 삭제·Firestore 정리와 이 GA4 속성의 사용자 삭제에 필요한 권한만 가진다. BigQuery 삭제 권한도 실제 export 범위에 한정해 확인한다. 런타임 identity를 추정하지 않는다.
- 등록 파일 집합 전체 검증 후, 이 앱 JSON 하나만 담은 격리 디렉터리로 `regsync`를 실행한다. 다른 사용자 작업의 앱 등록을 함께 올리지 않는다.
- 새 서버 이미지에는 우편 전용 원장을 boot에서 만드는 코드가 있으므로 registry TTL만 기다리는 것으로 대체하지 않는다. 새 production 검토 승인이 필요하다.

실제 사용자 삭제·우편 발행·보상 지급·기기 실행은 포함하지 않는다. 반영 후 공개 config와 무권한 요청 거부를 읽어 확인하고, 실제 계정 복원·수령은 승인된 테스트 identity와 범위에서 별도 실행한다. 실패하면 앱의 로컬 돌봄과 저장을 유지하고 서버 성공으로 기록하지 않는다.
