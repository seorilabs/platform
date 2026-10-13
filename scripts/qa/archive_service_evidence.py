#!/usr/bin/env python3
"""Store sanitized service evidence in the central verifier's draft archive."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import urllib.request
from urllib.parse import urlsplit
from service_e2e import passed_tests

class NoCredentialRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if urlsplit(newurl).scheme != "https":
            raise ValueError("insecure archive redirect")
        redirected = super().redirect_request(req, fp, code, msg, headers, newurl)
        if urlsplit(req.full_url).netloc != urlsplit(newurl).netloc:
            redirected.remove_header("Authorization")
        return redirected


REPOSITORY = "seorilabs/platform"
TAG = "development-evidence"


def request(path, token, body=None, binary=False):
    url = path if path.startswith("https://") else "https://api.github.com" + path
    headers = {"Authorization": "Bearer " + token, "Accept": "application/octet-stream" if binary else "application/vnd.github+json", "X-GitHub-Api-Version": "2026-03-10"}
    data = None
    if body is not None:
        data = body if isinstance(body, bytes) else json.dumps(body).encode()
        headers["Content-Type"] = "application/octet-stream" if isinstance(body, bytes) else "application/json"
    with urllib.request.build_opener(NoCredentialRedirect()).open(urllib.request.Request(url, headers=headers, data=data), timeout=45) as response:
        raw = response.read()
    return raw if binary else json.loads(raw)


def list_all(path, token):
    values = []
    for page in range(1, 100):
        batch = request(path + f"?per_page=100&page={page}", token)
        values.extend(batch)
        if len(batch) < 100:
            return values
    raise ValueError("archive pagination limit")


def store(directory):
    token = os.environ.get("GH_TOKEN") or subprocess.check_output(["gh", "auth", "token"], text=True).strip()
    files = sorted(directory.glob("development-evidence.*.service.before-build.json"))
    if len(files) != 1:
        raise ValueError("one canonical evidence file required")
    document = json.loads(files[0].read_text())
    sha = document["sourceSha"]
    if not re.fullmatch(r"[a-f0-9]{40}", sha) or document["repository"] != REPOSITORY:
        raise ValueError("evidence candidate mismatch")
    runtime = json.loads((directory / "runtime-result.json").read_text())
    digest = runtime["rawLogSha256"]
    if not re.fullmatch(r"[a-f0-9]{64}", digest):
        raise ValueError("invalid raw log fingerprint")
    # The canonical asset is uploaded last. Interrupted writes cannot unlock builds.
    assets = {f"runtime-result.{sha}.{digest}.json": (directory / "runtime-result.json").read_bytes(), f"test-events.{sha}.{digest}.json": (directory / "test-events.json").read_bytes(), files[0].name: files[0].read_bytes()}
    for raw in assets.values():
        if re.search(rb"-----BEGIN .*PRIVATE KEY|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+", raw):
            raise ValueError("secret-shaped data in archive")
    releases = list_all(f"/repos/{REPOSITORY}/releases", token)
    matching = [r for r in releases if r["tag_name"] == TAG]
    if any(not r["draft"] for r in matching) or len(matching) > 1:
        raise ValueError("ambiguous or public evidence archive")
    if not matching:
        try:
            release = request(f"/repos/{REPOSITORY}/releases", token, {"tag_name": TAG, "target_commitish": sha, "name": "서비스 개발 실행 근거", "body": "승인 설계별 전체 서버 실행 원장. SDK 정식 Release와 별도 보관. 합성 입력과 비밀값 없는 테스트 이벤트만 저장합니다.", "draft": True, "prerelease": False})
        except (TimeoutError, OSError):
            recovered = [r for r in list_all(f"/repos/{REPOSITORY}/releases", token) if r["tag_name"] == TAG and r["draft"]]
            if len(recovered) != 1:
                raise
            release = recovered[0]
    else:
        release = matching[0]
    existing = list_all(f"/repos/{REPOSITORY}/releases/{release['id']}/assets", token)
    canonical = [a for a in existing if a["name"] == files[0].name]
    if canonical:
        if len(canonical) != 1:
            raise ValueError("duplicate canonical evidence")
        previous = json.loads(request(canonical[0]["url"], token, binary=True))
        if previous["sourceSha"] != sha or previous["e2e"]["environment"]["configurationFingerprint"] != document["e2e"]["environment"]["configurationFingerprint"]:
            raise ValueError("existing evidence configuration differs; never overwrite")
        ref = previous["acceptance"][0]["evidence"][0]
        match = re.search(r"runtime-result\." + sha + r"\.([a-f0-9]{64})\.json", ref)
        if match is None:
            raise ValueError("canonical evidence lacks durable runtime reference")
        prior_hash = match.group(1)
        restored = {}
        for name in [f"runtime-result.{sha}.{prior_hash}.json", f"test-events.{sha}.{prior_hash}.json"]:
            found = [a for a in existing if a["name"] == name]
            if len(found) != 1:
                raise ValueError("incomplete canonical evidence archive")
            restored[name] = json.loads(request(found[0]["url"], token, binary=True))
        prior_runtime = restored[f"runtime-result.{sha}.{prior_hash}.json"]
        if prior_runtime["sourceSha"] != sha or prior_runtime["configurationFingerprint"] != previous["e2e"]["environment"]["configurationFingerprint"] or prior_runtime["completedAt"] != previous["e2e"]["completedAt"]:
            raise ValueError("archived runtime candidate mismatch")
        root = Path(__file__).resolve().parents[2]
        plan = json.loads((root / "docs/qa/service-e2e-plan.json").read_text())
        required = {t for s in plan["scenarios"] for t in s["requiredTests"]}
        passed_tests([json.dumps(event) for event in restored[f"test-events.{sha}.{prior_hash}.json"]], required)
        print("최초 후보 근거와 두 실행 자산 readback 확인; 중앙 검증이 유효기간을 다시 판정한다.")
        return
    for name, raw in assets.items():
        matches = [a for a in existing if a["name"] == name]
        if not matches:
            url = release["upload_url"].split("{")[0] + "?name=" + name
            try:
                request(url, token, raw)
            except (TimeoutError, OSError):
                # Unknown upload outcomes are read back before any next mutation.
                matches = [a for a in list_all(f"/repos/{REPOSITORY}/releases/{release['id']}/assets", token) if a["name"] == name]
                if len(matches) != 1:
                    raise
        matches = [a for a in list_all(f"/repos/{REPOSITORY}/releases/{release['id']}/assets", token) if a["name"] == name]
        if len(matches) != 1 or hashlib.sha256(request(matches[0]["url"], token, binary=True)).digest() != hashlib.sha256(raw).digest():
            raise ValueError("durable archive readback mismatch")
    print(f"보관 후 3개 자산 SHA-256 readback 일치: {sha}")


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--directory", type=Path, required=True)
    store(parser.parse_args().directory)
