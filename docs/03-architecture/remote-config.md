# RemoteConfig

## 왜 1단계에 있는가

공지·메시지함을 2단계로 미뤘으므로 **kill switch·강제 업데이트·점검 안내를 RC가 맡는다.**

원래 공지가 "마켓에 배포된 구버전 SDK에 도달할 수 있는 유일한 채널" 역할이었는데, RC가 그 자리를 대신한다. 그리고 **거의 공짜다** — 세션 응답에 얹으면 추가 왕복이 0이다.

## Firebase RC로는 안 되는 것

| | Firebase RC | 플랫폼 RC |
|---|---|---|
| AIT 런타임 | **안 됨** — happy-farm이 `remoteConfigRest.ts`를 자체 구현 | 됨 |
| Godot | **안 됨** — foam-party는 하드코딩, reascend는 자체 서버 | 됨 |
| 크로스앱 공통 설정 | 불가 — 프로젝트별 격리 | 가능 |
| 신뢰 경계 | 클라이언트 캐시. **위조 가능** | **IAP와 같은 서버 권위** |

현재 5개 앱이 각자 다른 방식을 쓰고 있고, 그중 둘은 사실상 동작하지 않는 상태다.

## API

```
GET /v1/config?appVersion=1.0.7&platform=android&locale=ko
  → ETag, Cache-Control: max-age=60

{
  "values": { "...": "앱별 임의 키-값" },
  "features": { "iap": true, "events": true },
  "sdk": { "status": "ok|deprecated|blocked", "message": "...", "updateUrl": "..." },
  "maintenance": { "active": false, "message": "...", "until": null },
  "minSupportedVersion": "1.0.0"
}
```

세션 응답에도 같은 내용이 병합되어 **부팅 시 왕복이 1회**로 끝난다. Godot의 `HTTPRequest`가 동시 1요청만 처리하므로 이게 중요하다.

## 타겟팅

3축만 지원한다.

| 축 | 예 |
|---|---|
| 플랫폼 | `android`, `ios`, `web`, `ait` |
| 앱 버전 | `min`, `max` — stable SemVer |
| 로케일 | `ko`, `en`, … |

버전 비교는 백오피스의 `stable-semver.ts`와 같은 규칙을 쓴다. 조직의 모든 릴리스가 stable SemVer 태그이므로 "v1.2.0 이상만"이 자연스럽게 표현된다.

**staged rollout은 1단계에 넣지 않는다.** 퍼센트 롤아웃은 재현이 어렵고 디버깅 비용이 크다.

## kill switch 3종

| 수준 | 위치 | 효과 |
|---|---|---|
| 앱 전체 정지 | 레지스트리 `status: paused` | 모든 플랫폼 호출 403 |
| **점검 모드** | RC `maintenance.active` | 클라이언트가 점검 안내 표시 |
| 기능 단위 | RC `features.*` | 해당 기능만 비활성 |
| SDK 차단 | RC `sdk.status = blocked` | 구버전 SDK가 이벤트만 남기고 정지 |

`sdk.status`가 **마켓 배포된 구버전을 서버에서 끄는 유일한 수단**이다. 이게 없으면 한번 배포된 SDK를 영원히 지원해야 한다.

점검 모드는 [BREAK-GLASS](../08-ops/BREAK-GLASS.md)로 백오피스 없이도 켤 수 있어야 한다.

## 캐시

- 서버: 인메모리 + `sync.RWMutex`. 값 변경 반영 **60초 이내**
- 클라이언트: ETag로 304 처리. 오프라인이면 마지막 값 + `stale: true`
- 캐시 히트 시 **Firestore read 0**이 검증 기준이다

## 값 관리

백오피스에서 편집하고 Firestore에 저장한다. 앱별 네임스페이스 + 크로스앱 공통 네임스페이스.

기본값은 **클라이언트 SDK에 하드코딩**해 둔다. 플랫폼이 죽어도 앱이 동작해야 한다. RC 조회 실패는 조용히 기본값으로 폴백하고 게임 진행을 절대 막지 않는다.
