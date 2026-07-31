# Work

작업 로그와 backlog.

## 현재 단계

**D0 — 문서화.** 코드 0줄.

## 진행

- [x] Obsidian 노트 3건 — `프로젝트/개인/공통 플랫폼/`
- [x] 저장소 골격
- [ ] ADR 0001~0008
- [ ] `spec/openapi.yaml` 초안
- [ ] GitHub private repo push

## 다음 단계 진입 조건

P0 착수 전에 D0가 전부 끝나야 한다. 특히 **ADR 0006(Go 채택)과 0008(원장 소유자 키)은 P4 이후를 좌우**하므로 코드 전에 확정한다.

## P0에서 답을 얻어야 하는 것

`../07-qa/README.md`의 실측 항목 7가지. 그중 **Apple JWS 검증 Go 방안**이 최우선이며, 실패 시 Apple만 기존 Cloud Functions를 유지하는 하이브리드로 간다.
