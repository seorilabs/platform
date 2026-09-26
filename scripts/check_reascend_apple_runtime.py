#!/usr/bin/env python3
"""등록된 CI 배포 신원으로 공개 키 식별자와 Reascend SKU만 검증한다."""
import json
import os
import subprocess


def gcloud_json(*arguments):
    result = subprocess.run(["gcloud", *arguments, "--project", os.environ["PROJECT_ID"]],
                            capture_output=True, text=True)
    if result.returncode:
        # 공급자 오류의 원문/응답을 출력하지 않고 실패 종류만 남긴다.
        reason = next((code for code in ("PERMISSION_DENIED", "NOT_FOUND", "UNAUTHENTICATED", "RESOURCE_EXHAUSTED")
                       if code in result.stderr), "QUERY_FAILED")
        raise SystemExit(f"gcloud {arguments[0]} query failed: {reason} (exit {result.returncode})")
    return json.loads(result.stdout)


def main():
    service = gcloud_json("run", "services", "describe", "platform-iap", "--region", os.environ["REGION"], "--format=json")
    values = {entry["name"]: entry.get("value") for entry in service["spec"]["template"]["spec"]["containers"][0].get("env", [])}
    # 개인키 값이나 서비스 환경 전체를 출력하지 않는다.
    key_id = values.get("IAP_APPLE_KEY_ID")
    issuer = values.get("IAP_APPLE_ISSUER_ID")
    print(json.dumps({"appleKeyId": key_id, "appleIssuerId": issuer}))
    if key_id != "4N8V928LWA" or issuer != "69a6de86-a179-47e3-e053-5b8c7c11a4d1":
        raise SystemExit("등록된 공통 Apple IAP 키의 공개 identity와 운영 설정이 다릅니다.")
    catalog = gcloud_json("secrets", "versions", "access", "latest", "--secret", "iap-catalog")
    entries = catalog.get("apps", {}).get("reascend", {}).get("entitlements", {})
    for suffix in ("starter", "small", "medium", "large"):
        entitlement = "gems_" + suffix
        entry = entries.get(entitlement, {})
        expected = "com.seorilabs.reascend.gems." + suffix
        # 카탈로그는 비밀값이 없지만 지정 앱의 공개 상품 ID와 유형만 출력한다.
        print(json.dumps({"appId": "reascend", "entitlement": entitlement,
                          "appStoreProductId": entry.get("app_store"), "type": entry.get("type")}))
        if entry.get("app_store") != expected or entry.get("type") != "consumable":
            raise SystemExit("Reascend Apple 상품 매핑 또는 소모품 유형이 운영 카탈로그와 다릅니다.")


if __name__ == "__main__":
    main()
