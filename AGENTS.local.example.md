# Local Agent Overrides (예시)

이 파일을 `AGENTS.local.md`로 복사해 개인·환경별 설정을 담는다. **커밋하지 않는다.**

## GCP

```
PROJECT_ID=seorilabs-platform
REGION=asia-northeast3
PROVISIONER_BILLING_ID=확정 필요
ORG_ID=확정 필요
```

## Cloud Run URL

P0 배포 후 채운다. BREAK-GLASS 절차에도 필요하다.

```
PLATFORM_API_URL=확정 필요
PLATFORM_IAP_URL=확정 필요
PLATFORM_ADMIN_URL=확정 필요
```

## 로컬 개발

```
# Firestore 에뮬레이터
FIRESTORE_EMULATOR_HOST=localhost:8080
GOOGLE_CLOUD_PROJECT=demo-platform

# gcloud 격리 config — 사용자 기본 ADC를 오염시키지 않는다
CLOUDSDK_CONFIG=$HOME/.config/seorilabs/gcloud-provisioner
```

## 마켓 샌드박스

```
APPLE_SANDBOX_TESTER=확정 필요
PLAY_LICENSE_TESTER=확정 필요
```

## 주의

- **키 파일 경로만 적고 키 값 자체는 적지 않는다.**
- Seorilabs 로컬 자격증명의 source of truth는 `~/.config/seorilabs`다.
