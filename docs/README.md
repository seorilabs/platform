# Docs Source Of Truth

이 폴더는 Seorilabs 공통 플랫폼의 실행 원장이다.

Obsidian은 지식베이스와 재사용 가능한 운영 노하우를 보조로 관리한다. 기획, 의사결정, 작업, 운영 절차는 이 `docs/`를 우선한다.

## 폴더

```text
01-planning/      # 범위, 단계 계획, 승인 상태
02-decisions/     # ADR, 장기 의사결정
03-architecture/  # 아키텍처, identity, 이벤트, IAP, RemoteConfig
04-work/          # 작업 로그, backlog
05-markets/       # 마켓 연동 원장 - Play/App Store/AppsInToss 자격증명과 설정
06-release/       # 배포 절차, 환경 분리
07-qa/            # 검증 체크리스트, 테스트 전략
08-ops/           # 운영 런북, BREAK-GLASS, 관측, 비용
09-knowledge/     # 일반 지식. go/ 아래에 Go 관용구 학습 기록
```

## 이 저장소의 다른 원장

`docs/`가 유일한 원장은 아니다. 성격이 다른 두 가지가 더 있다.

| 원장 | 대상 | 위치 |
|---|---|---|
| **API 계약** | 요청·응답 스키마, 엔드포인트 | `spec/openapi.yaml` |
| **앱 레지스트리** | 앱별 Firebase 프로젝트, 기능 플래그, 이벤트 allowlist | `registry/apps/*.json` |

Go 타입과 TS 타입은 `spec/openapi.yaml`에서 생성된 **산출물**이지 계약이 아니다. 계약을 바꿀 때는 openapi.yaml을 먼저 고친다.

## 문서 규칙

- 확인된 사실과 추정은 분리한다.
- 모르는 값은 `확정 필요`로 남긴다. 임의로 채우지 않는다.
- 콘솔에서 변경한 값은 관련 문서와 config example에 반영한다.
- **되돌리기 어려운 결정은 반드시 ADR로 남긴다.** 리전, 저장소 선택, 언어, 계약 형식, 원장 소유자 키가 여기 해당한다.
