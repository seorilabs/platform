# BREAK-GLASS

백오피스가 죽었을 때 플랫폼을 직접 조작하는 긴급 절차.

## 언제 쓰는가

`backoffice.vzyx.xyz`에 접근할 수 없는데 즉시 조작이 필요할 때. 원인은 보통 셋 중 하나다.

- RPI k8s 장애 — 노드 다운, 정전, ISP 장애
- 백오피스 배포 실패로 pod가 안 뜸
- **GitHub OAuth 장애로 로그인 불가**

## 전제 — 평시에 준비해둘 것

**이 절차는 GitHub에 의존하지 않는다.** GitHub 장애가 백오피스를 못 쓰게 만드는 원인 중 하나이기 때문이다.

```bash
# 사람 계정에 run.invoker를 평시에 부여해둔다. 장애 중에 IAM을 바꿀 수는 없다.
gcloud run services add-iam-policy-binding platform-admin \
  --region=asia-northeast3 \
  --member="user:ih@toss.im" \
  --role="roles/run.invoker" \
  --project=seorilabs-platform
```

`확정 필요` — `platform-admin`의 실제 URL을 P0 배포 후 여기 적는다.

## 절차

### 1. 점검 모드 켜기

가장 자주 쓸 절차다. RemoteConfig의 `maintenance`를 켜면 모든 클라이언트가 점검 안내를 띄운다.

```bash
TOKEN=$(gcloud auth print-identity-token --audiences=https://platform-admin-XXXX.run.app)

curl -sS -XPOST https://platform-admin-XXXX.run.app/admin/emergency/maintenance \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Actor: magicsih" \
  -H "X-Request-Id: $(uuidgen)" \
  -d '{"appId":"lizard-tycoon","minutes":30}'
```

**이 엔드포인트는 본문 텍스트를 받지 않는다.** 앱과 시간만 받고 8개 언어 문구는 서버에 하드코딩된 정적 템플릿이다. **장애 중에 자유 텍스트 입력이나 외부 LLM 호출에 의존하면 안 된다.**

### 2. 점검 모드 끄기

```bash
curl -sS -XDELETE https://platform-admin-XXXX.run.app/admin/emergency/maintenance \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Actor: magicsih" \
  -H "X-Request-Id: $(uuidgen)" \
  -d '{"appId":"lizard-tycoon"}'
```

### 3. 앱 일시 정지 — kill switch

특정 앱의 모든 플랫폼 호출을 403으로 막는다. 결제 사고나 남용이 의심될 때.

```bash
curl -sS -XPOST https://platform-admin-XXXX.run.app/admin/emergency/pause \
  -H "Authorization: Bearer $TOKEN" \
  -H "X-Actor: magicsih" \
  -H "X-Request-Id: $(uuidgen)" \
  -d '{"appId":"lizard-tycoon","reason":"결제 검증 이상"}'
```

**주의**: 이러면 진행 중인 결제도 막힌다. 이미 마켓에서 과금된 구매가 지급되지 않고 pending으로 남는다. 정지 해제 후 클라이언트의 pending proof 복구가 처리하지만, **정지 시간이 길수록 CS가 늘어난다.**

### 4. 상태 확인

```bash
# 서비스가 살아 있는가
curl -sS https://platform-api-XXXX.run.app/health/live

# 특정 유저의 entitlement — support code로 조회
curl -sS "https://platform-admin-XXXX.run.app/admin/users/LT-8F3K2Q9M/entitlements" \
  -H "Authorization: Bearer $TOKEN"
```

### 5. 원장 직접 조회

API도 못 쓸 때. `cmd/fs`는 이 저장소의 조회 전용 CLI다.

```bash
go run ./cmd/fs get "iap_users/pu_01JXYZ/entitlements/sp_galaxy_gecko"
go run ./cmd/fs ls  "processed_orders" --limit=20
```

집계가 필요하면 BigQuery로 간다.

```bash
bq query --nouse_legacy_sql --maximum_bytes_billed=2000000000 \
'SELECT action, COUNT(*) FROM `seorilabs-platform.platform.audit`
 WHERE ts >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 1 HOUR)
 GROUP BY 1 ORDER BY 2 DESC'
```

## 하지 말 것

- **entitlement를 직접 지급하지 않는다.** `cmd/fs`에 쓰기 명령이 없는 게 의도다. 지급은 백오피스가 복구된 뒤 감사 원장을 남기며 한다
- **Firestore 콘솔에서 문서를 직접 편집하지 않는다.** projection 재계산이 안 돼 원장이 어긋난다
- **원장 문서를 삭제하지 않는다.** 어떤 상황에서도

## 사후

백오피스 복구 후 반드시 확인한다.

1. break-glass로 만든 변경이 백오피스 화면에 보이는가
2. `platform.audit`에 `X-Actor`와 함께 기록됐는가
3. 점검 모드를 껐는가

## 리허설

**P9에서 실제로 한 번 실행해 성공을 확인한다.** 문서만 있고 실행해본 적 없는 런북은 장애 중에 작동하지 않는다.

```bash
kubectl -n platform scale deploy/backoffice --replicas=0
# → 게임에서 결제·RC·이벤트가 정상인지 확인
# → 위 절차로 점검 모드 on/off
kubectl -n platform scale deploy/backoffice --replicas=1
```
