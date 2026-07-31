# Knowledge

일반 지식과 재사용 가능한 학습 내용. 제품 상태의 원장이 아니다.

Obsidian으로 승격할 만한 내용은 승격하고, 여기에는 이 저장소 맥락에서만 의미 있는 것을 둔다.

## 하위

- [go/](go/) — **Go 관용구와 함정 학습 기록.** 이 저장소는 Go 학습이 목적 중 하나다

## 조사로 확인된 조직 사실

플랫폼 설계의 근거가 된 사실들. 시점은 2026-07-31.

### 앱별 Firebase 프로젝트는 완전히 분리되어 있다

16개 프로젝트가 각자 존재하고 공유 프로젝트가 없다. GA4 → BigQuery export도 앱별 데이터셋이며 통합 뷰가 없다. 통합 레이어는 `seorilabs-backoffice`가 매일 각 앱 BigQuery를 쿼리해 MySQL로 적재하는 방식이다.

### Firebase ID 토큰 서명키는 전 프로젝트 공통이다

`securetoken@system.gserviceaccount.com` x509 하나가 **모든 Firebase 프로젝트**의 ID 토큰을 서명한다. 프로젝트별 키가 아니다. 덕분에 **앱 16개의 자격증명을 하나도 보유하지 않고** 검증할 수 있고, 프로젝트 구분은 `aud`/`iss` claim으로 한다.

이 사실이 인증 설계를 크게 단순화했다.

### FCM은 SA 키 없이 보낼 수 있다

플랫폼 SA를 각 앱 프로젝트 IAM에 바인딩하면 ADC 토큰만으로 발송된다. `ga4-routine-ro` SA가 cross-project `bigquery.dataViewer`를 받는 것과 같은 패턴이다. 2단계에서 푸시를 만들 때 쓴다.

### GA4 Measurement Protocol의 한계

- **예약 이벤트명을 보낼 수 없다.** `session_start`, `in_app_purchase`, `ad_impression` 등 11개가 실제로 스킵되고 있다
- **인증이 없어 누구나 위조할 수 있다.** 그래서 결제 근거나 CS 증빙으로 쓸 수 없다
- **요청 IP로 geo를 판정한다.** 서버가 릴레이하면 모든 트래픽의 국가가 서버 리전으로 붙는다

마지막 항목 때문에 AIT만 서버 릴레이하고 네이티브·RN은 직접 전송을 유지한다.

### Firestore 무료 할당량은 `(default)` 데이터베이스에만 적용된다

named database를 만들면 첫 읽기부터 과금된다. staging을 별도 DB로 나누지 않고 컬렉션 prefix로 나누는 이유다.

### 실측 규모 — 2026-07 기준 28일

| 앱 | 평균 DAU | 평균 이벤트/일 |
|---|---|---|
| lucid-chess | 15 | 864 |
| happy-farm | 13 | **11,211** |
| crossword-puzzle | 11 | 571 |
| foam-party | 4 | 34 |
| match-picture-app | 1 | 9 |
| **합계** | **44** | **12,689** |

happy-farm이 유저당 862 이벤트/일로 다른 앱의 15~25배다. 게임 루프에서 남발 중이며 **별건으로 확인이 필요**하다.
