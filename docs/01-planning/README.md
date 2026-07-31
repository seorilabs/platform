# Planning

## 1단계 범위

**identity · 지표 · RemoteConfig · IAP** — 소비성·비소비성, 3마켓.

## 2단계로 연기

공지 · 메시지함 · 푸시 · 구독형 IAP.

공지를 미뤘으므로 **kill switch·강제 업데이트·점검 안내는 RemoteConfig가 맡는다.** → `../03-architecture/remote-config.md`

## 왜 만드는가

20개 이상의 앱·게임에서 공통 역량이 제품마다 재구현되고 있다.

| 관심사 | 현황 |
|---|---|
| 인증 | 6벌 독립 구현. 6개 앱은 아예 없음 |
| **IAP** | **서버검증 3벌** — lizard-tycoon / memo-swipe / moonmate |
| 지표 | 전송 경로 7가지, GA4 MP 클라이언트 3벌. boolean 직렬화가 갈려 **동작 자체가 다름** |
| RemoteConfig | 5개 앱이 각자 다른 방식. AIT·Godot에서 Firebase RC가 안 됨 |

`starter-template-*`에는 런타임 코드가 0줄이라 신규 앱은 매번 0에서 시작한다.

## 단계

| 단계 | 내용 | 배우는 Go |
|---|---|---|
| D0 | 문서화 — Obsidian, repo 골격, ADR | — |
| P0 | 실측과 부트스트랩. **Apple JWS 방안 결정 최우선** | CLI 워밍업 |
| P1 | identity + 계약 | `net/http`, error, 인터페이스, `context` |
| P2 | 지표 + SDK 2벌 | goroutine, channel |
| P3 | RemoteConfig | RWMutex 캐시, ETag |
| P4 | IAP 코어 — 원장 | **Firestore 트랜잭션** |
| P5 | IAP 3마켓 provider | 포트 구현, 외부 HTTP, 암호학 |
| P6 | IAP 웹훅 + 재시도 워커 | lease, 백오프 |
| P7 | 백오피스 commerce 연결 | — |
| P8 | lizard-tycoon 통합 → **론칭** | — |
| P9 | 마감 + 템플릿 + 2번째 앱 | — |

단계 순서를 학습 곡선에 맞춰 배치했다. **가장 어려운 Firestore 트랜잭션이 P4**라 앞선 세 단계로 준비된 뒤에 만난다.

## 파일럿

**lizard-tycoon이 플랫폼 IAP로 론칭한다.**

미론칭 상태라 실매출·실사용자가 없고 Firestore 원장에 샌드박스 데이터뿐이다. **지금이 가장 싼 전환 시점**이고, 론칭 후 실결제 데이터를 안고 갈아끼우는 것이 훨씬 위험하다.

## 승인 상태

계획 승인 완료. 상세 실행 계획은 Obsidian `프로젝트/개인/공통 플랫폼/02 실행 계획`.
