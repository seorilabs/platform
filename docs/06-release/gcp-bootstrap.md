# GCP 부트스트랩 런북

`seorilabs-platform` 프로젝트를 처음 세운 실제 절차와 **겪은 함정**. 다음에 인프라 프로젝트를 만들 때 재사용한다.

실행 시점 2026-07-31. 전부 provisioner SA로 무인 실행했다.

## 확보한 값

| 항목 | 값 |
|---|---|
| 프로젝트 ID | `seorilabs-platform` |
| 프로젝트 번호 | `306278488979` |
| 조직 | `seorilabs.com` — `965953762431` |
| 과금 계정 | `01179A-37A44C-447C1D` — **통화 KRW** |
| 리전 | `asia-northeast3` |
| provisioner SA | `seorilabs-firebase-automation@seorilabs-gws.iam.gserviceaccount.com` |

```bash
export CLOUDSDK_CONFIG=$HOME/.config/seorilabs/gcloud-provisioner
export CLOUDSDK_CORE_PROJECT=seorilabs-platform
```

## 절차

### 1. 프로젝트 생성과 과금 연결

```bash
gcloud projects create seorilabs-platform \
  --organization=965953762431 \
  --name="Seorilabs Platform"

gcloud billing projects link seorilabs-platform \
  --billing-account=01179A-37A44C-447C1D
```

> **함정**: display name은 ASCII만 쓴다. 한글을 넣으면 실패한다.

### 2. Billing budget — 과금 연결 직후 즉시

**이걸 먼저 한다.** 나머지를 다 하고 나중에 하면 그 사이 사고가 청구서로 온다.

```bash
gcloud billing budgets create \
  --billing-account=01179A-37A44C-447C1D \
  --display-name="seorilabs-platform" \
  --budget-amount=70000KRW \
  --threshold-rule=percent=0.4 \
  --threshold-rule=percent=1.0 \
  --filter-projects=projects/306278488979 \
  --billing-project=seorilabs-platform
```

> **함정 1 — 통화**: 이 조직의 과금 계정은 **KRW**다. `--budget-amount=50USD`는 `INVALID_ARGUMENT`로 실패한다. 계획서의 $20/$50를 원화로 환산해 70,000 KRW 총액에 40%·100% 임계로 잡았다.
>
> ```bash
> gcloud billing accounts describe 01179A-37A44C-447C1D --format=yaml | grep currencyCode
> ```
>
> **함정 2 — quota project**: `--billing-project`을 주지 않으면 gcloud가 엉뚱한 프로젝트(여기서는 `seorilabs-cyclepair-prod`)를 quota project로 잡고 `SERVICE_DISABLED`로 실패한다.

### 3. API 활성화

```bash
gcloud services enable \
  run.googleapis.com firestore.googleapis.com \
  bigquery.googleapis.com bigquerystorage.googleapis.com \
  cloudtasks.googleapis.com secretmanager.googleapis.com \
  artifactregistry.googleapis.com cloudbuild.googleapis.com \
  monitoring.googleapis.com logging.googleapis.com \
  billingbudgets.googleapis.com iamcredentials.googleapis.com \
  pubsub.googleapis.com androidpublisher.googleapis.com \
  --project=seorilabs-platform
```

`firestore.googleapis.com`을 켜면 `firebaserules.googleapis.com`과 `datastore.googleapis.com`이 의존성으로 딸려온다. **이건 Firebase 프로젝트 등록과 다르다.** ADR 0002 참고.

### 4. Firestore — ADR 0002·0003의 실측 지점

```bash
gcloud firestore databases create \
  --location=asia-northeast3 \
  --type=firestore-native \
  --project=seorilabs-platform

gcloud firestore databases update \
  --database='(default)' --delete-protection \
  --project=seorilabs-platform
```

**결과가 두 ADR을 검증했다.**

| 확인 사항 | 결과 |
|---|---|
| Firebase 등록 없이 생성 | **성공** — GCP API만으로 됨 |
| `freeTier` | **`true`** ← ADR 0003의 무료 티어 근거 |
| `locationId` | `asia-northeast3` |
| 이름 | `projects/seorilabs-platform/databases/(default)` |
| `concurrencyMode` | `PESSIMISTIC` — IAP 트랜잭션에 적합 |

**삭제 보호를 반드시 켠다.** 결제 원장이 들어갈 DB다. 기본값은 비활성이다.

> **미결**: `pointInTimeRecoveryEnablement`가 `DISABLED`다. 결제 원장 복구에 유용하지만 저장 비용이 붙는다. 실데이터가 쌓이기 시작할 때 재검토한다.

### 5. BigQuery

```bash
for ds in platform platform_stg; do
  bq --project_id=seorilabs-platform mk \
     --location=asia-northeast3 --dataset "$ds"
done
```

Firestore와 **같은 리전**이어야 한다. 교차 리전 조인이 불가하다.

### 6. 서비스 계정과 IAM

```bash
for sa in platform-api platform-iap platform-ingest platform-admin \
          platform-worker backoffice-admin platform-deployer; do
  gcloud iam service-accounts create "$sa" --project=seorilabs-platform
done
```

부여한 역할:

| SA | 역할 |
|---|---|
| `platform-api` | `datastore.user`, `bigquery.dataEditor` |
| `platform-iap` | `datastore.user`, `bigquery.dataEditor`, **`secretmanager.secretAccessor`** |
| `platform-ingest` | `bigquery.dataEditor`, `bigquery.jobUser` |
| `platform-admin` | `datastore.user`, `bigquery.dataEditor` |
| `platform-worker` | `datastore.user` |
| `platform-deployer` | `run.admin`, `artifactregistry.writer`, `iam.serviceAccountUser` |
| **`backoffice-admin`** | **프로젝트 역할 없음** |

> **`backoffice-admin`에 프로젝트 역할을 주지 않는 것이 ADR 0001의 핵심이다.** 서비스별 `run.invoker`만 준다. RPI가 침해돼도 얻는 건 "Admin API 호출 가능"뿐이다.
>
> **`secretmanager.secretAccessor`는 `platform-iap`에만** 준다. 마켓 자격증명 격리(R3).

### 7. Artifact Registry

```bash
gcloud artifacts repositories create platform \
  --repository-format=docker --location=asia-northeast3 \
  --project=seorilabs-platform
```

생성에 2분 정도 걸린다.

### 8. Cloud Build 권한 — 신규 프로젝트의 함정

첫 `gcloud builds submit`이 이렇게 실패한다.

```
306278488979-compute@developer.gserviceaccount.com does not have
storage.objects.get access to ... _cloudbuild/objects/source/...
```

Compute Engine 기본 SA가 소스 버킷을 못 읽는다. 부여한다.

```bash
gcloud projects add-iam-policy-binding seorilabs-platform \
  --member="serviceAccount:306278488979-compute@developer.gserviceaccount.com" \
  --role="roles/cloudbuild.builds.builder" --condition=None

gcloud projects add-iam-policy-binding seorilabs-platform \
  --member="serviceAccount:306278488979-compute@developer.gserviceaccount.com" \
  --role="roles/artifactregistry.writer" --condition=None
```

> 장기적으로는 Cloud Build를 쓰지 않는다. **ARC 러너에서 Go 크로스컴파일**로 빌드한다. 이 권한은 P0 스모크용이다.

### 9. Cloud Run 배포 — DRS 정책 함정

```bash
gcloud run deploy platform-api \
  --image=asia-northeast3-docker.pkg.dev/seorilabs-platform/platform/platform:TAG \
  --region=asia-northeast3 \
  --service-account=platform-api@seorilabs-platform.iam.gserviceaccount.com \
  --set-env-vars=PLATFORM_ROLE=api \
  --min-instances=0 --max-instances=5 --cpu-boost \
  --project=seorilabs-platform
```

`--allow-unauthenticated`를 주면 이렇게 경고가 난다.

```
Setting IAM policy failed, try "gcloud beta run services add-iam-policy-binding
  --member=allUsers --role=roles/run.invoker platform-api"
```

**조직 org policy가 `allUsers` 바인딩을 막는다.**

```bash
gcloud org-policies describe constraints/iam.allowedPolicyMemberDomains \
  --organization=965953762431
# → allowedValues: [C02f93h8p]   seorilabs 디렉토리만 허용
```

lizard-tycoon이 이미 겪은 문제다. **해법은 invoker IAM 검사 자체를 끄는 것**이다.

```bash
gcloud run services update platform-api \
  --region=asia-northeast3 --no-invoker-iam-check \
  --project=seorilabs-platform
```

> **⚠️ `platform-admin`에는 절대 이 플래그를 쓰지 않는다.** admin은 `--no-allow-unauthenticated`로 두어 Cloud Run 인프라가 애플리케이션 코드 진입 전에 거부하게 한다. 이게 라우팅 버그로 admin이 노출되는 사고를 구조적으로 막는 장치다.

## 배포된 서비스

| 서비스 | URL | invoker |
|---|---|---|
| `platform-api` | `https://platform-api-306278488979.asia-northeast3.run.app` | **공개** — `--no-invoker-iam-check` |
| `platform-iap` | `확정 필요` — P5 | 공개 예정 |
| `platform-ingest` | `확정 필요` — P2 | 공개 예정 |
| `platform-admin` | `확정 필요` — P7 | **private 유지** |

## 아직 하지 않은 것

- **WIF** — CI를 붙이는 P1에서 한다. org var `GOOGLE_WORKLOAD_IDENTITY_PROVIDER` 재사용
- Cloud Monitoring 알림 정책 — Telegram 채널 연결 포함
- Firestore 인덱스 — P4에서 원장 스키마와 함께
- Secret Manager 시크릿 — P5에서 마켓 자격증명과 함께
