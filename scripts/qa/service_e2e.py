#!/usr/bin/env python3
"""Own a loopback emulator, execute service journeys and emit sanitized evidence."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import time
from datetime import datetime, timezone

ROOT = Path(__file__).resolve().parents[2]
EMULATOR_SHA256 = "9b6498b7f62714d67f48f59b3818883cd682dbcd46b9f59511de81c97bb5166c"
PACKAGES = ["./cmd/platform", "./internal/iap", "./internal/iap/verify", "./internal/content", "./internal/events", "./internal/operational", "./internal/identity", "./internal/ads", "./internal/iap/ledger"]


def stamp():
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def read_json(path):
    return json.loads(path.read_text())


def validate_plan(inventory, plan, routes):
    features = [f["id"] for f in inventory["features"]]
    if not features or len(features) != len(set(features)):
        raise ValueError("duplicate or missing feature")
    covered = {f for s in plan["scenarios"] for f in s["featureIds"]}
    if covered != set(features) or any(not s["requiredTests"] for s in plan["scenarios"]):
        raise ValueError("unmapped implemented feature")
    actual = [r["path"] for r in inventory["apiRoutes"]]
    if sorted(actual) != sorted(routes) or len(actual) != len(set(actual)):
        raise ValueError("OpenAPI route inventory mismatch")
    if any(r["featureId"] not in features for r in inventory["apiRoutes"]):
        raise ValueError("unknown route owner")


def passed_tests(lines, required):
    passed = set()
    counts = {"pass": 0, "fail": 0, "skip": 0}
    safe = []
    for line in lines:
        event = json.loads(line)  # Any unstructured compiler/runtime output fails closed.
        safe.append({k: event[k] for k in ("Time", "Action", "Package", "Test", "Elapsed") if k in event})
        if event["Action"] in ("skip", "fail"):
            raise ValueError("failed or skipped runtime execution")
        if event["Action"] == "pass" and "Test" in event:
            counts["pass"] += 1
            passed.add(event["Test"])
    if not required <= passed:
        raise ValueError("required runtime scenarios did not execute: " + ",".join(sorted(required - passed)))
    return safe, counts


def preserve_failure(raw, output, sha, exit_code):
    events = []
    unstructured = 0
    for line in raw.read_text(errors="replace").splitlines():
        try:
            event = json.loads(line)
            if not isinstance(event, dict) or "Action" not in event:
                raise ValueError("not a test event")
        except ValueError:
            unstructured += 1
            continue
        events.append({k: event[k] for k in ("Time", "Action", "Package", "Test", "Elapsed") if k in event})
    result = {"sourceSha": sha, "status": "failed", "exitCode": exit_code,
              "failed": [e for e in events if e["Action"] in ("fail", "build-fail")],
              "unstructuredLines": unstructured, "rawLogSha256": hashlib.sha256(raw.read_bytes()).hexdigest()}
    (output / "test-events.json").write_text(json.dumps(events, indent=2) + "\n")
    (output / "failure-result.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))  # Only test names, packages, status and digest; no Output.


def run_tests(private, output, sha, env, raw):
    try:
        run = subprocess.run(["go", "test", "-race", "-p=1", "-tags=integration", "-timeout=240s", "-count=1", "-json", *PACKAGES], cwd=ROOT / "server", env=env, stdout=raw, stderr=subprocess.STDOUT, timeout=600)
    except subprocess.TimeoutExpired:
        raw.flush()
        preserve_failure(private / "go-raw.jsonl", output, sha, 124)
        raise
    if run.returncode != 0:
        raw.flush()
        preserve_failure(private / "go-raw.jsonl", output, sha, run.returncode)
        raise ValueError(f"runtime exit {run.returncode}; private diagnostics: {private}")


def fingerprint():
    files = [ROOT / "docs/qa/feature-inventory.json", ROOT / "docs/qa/service-e2e-plan.json", ROOT / ".github/workflows/deploy.yml", ROOT / "server/go.mod", ROOT / "server/go.sum"]
    files += sorted((ROOT / "registry/apps").glob("*.json"))
    manifest = {"profile": "service-runtime-tcp-firestore-synthetic-v1", "emulatorSha256": EMULATOR_SHA256,
                "packages": PACKAGES, "files": {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in files}}
    return hashlib.sha256(json.dumps(manifest, sort_keys=True, separators=(",", ":")).encode()).hexdigest(), manifest


def execute(jar, output):
    if hashlib.sha256(jar.read_bytes()).hexdigest() != EMULATOR_SHA256:
        raise ValueError("untrusted emulator digest")
    subprocess.run(["git", "diff", "--exit-code", "HEAD", "--"], cwd=ROOT, check=True, stdout=subprocess.DEVNULL)
    sha = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    inventory, plan = read_json(ROOT / "docs/qa/feature-inventory.json"), read_json(ROOT / "docs/qa/service-e2e-plan.json")
    routes = re.findall(r"^  (/[^\s:][^\n]*):$", (ROOT / "spec/openapi.yaml").read_text(), re.M)
    validate_plan(inventory, plan, routes)
    planned = stamp()
    subprocess.run(["python3", "-m", "unittest", "discover", "-s", "scripts/qa"], cwd=ROOT, check=True)
    subprocess.run(["bash", "scripts/check_deploy_workflow.sh"], cwd=ROOT, check=True)
    output.mkdir(parents=True, exist_ok=False)  # Never overwrite previous evidence.
    private = Path(tempfile.mkdtemp(prefix="platform-service-raw-"))
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        port = listener.getsockname()[1]
    env = dict(os.environ)
    env.update(GOMAXPROCS="2", FIRESTORE_EMULATOR_HOST=f"127.0.0.1:{port}", GOOGLE_CLOUD_PROJECT="demo-platform-service-e2e", GCLOUD_PROJECT="demo-platform-service-e2e")
    # The emulator owns all of this disposable database. No production identity is inherited.
    for key in ("GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "FIREBASE_TOKEN"):
        env.pop(key, None)
    process = None
    with (private / "emulator.log").open("w") as emulator_log, (private / "go-raw.jsonl").open("w") as raw:
        try:
            process = subprocess.Popen(["java", "-Xmx512m", "-XX:ActiveProcessorCount=2", "-jar", str(jar), "--host", "127.0.0.1", "--port", str(port), "--project_id", "demo-platform-service-e2e"], stdout=emulator_log, stderr=subprocess.STDOUT, env=env)
            deadline = time.monotonic() + 120
            while True:
                if process.poll() is not None or time.monotonic() >= deadline:
                    raise ValueError(f"emulator failed to start; private diagnostics: {private}")
                try:
                    with socket.create_connection(("127.0.0.1", port), timeout=0.3):
                        break
                except OSError:
                    time.sleep(0.1)
            started = stamp()
            run_tests(private, output, sha, env, raw)
        finally:
            if process is not None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)
    required = {t for s in plan["scenarios"] for t in s["requiredTests"]}
    safe, counts = passed_tests((private / "go-raw.jsonl").read_text().splitlines(), required)
    completed = stamp()
    fp, config = fingerprint()
    raw_hash = hashlib.sha256((private / "go-raw.jsonl").read_bytes()).hexdigest()
    (output / "test-events.json").write_text(json.dumps(safe, indent=2) + "\n")
    context = {"sourceSha": sha, "plannedAt": planned, "startedAt": started, "completedAt": completed,
               "configurationFingerprint": fp, "configuration": config, "counts": counts, "boundaries": plan["boundaries"],
               "rawLogSha256": raw_hash, "isolation": "fresh owned loopback Firestore emulator; demo-platform-service-e2e plus test-only projects/prefixes; no production credentials"}
    (output / "runtime-result.json").write_text(json.dumps(context, ensure_ascii=False, indent=2) + "\n")
    evidence_base = os.environ.get("SERVICE_E2E_EVIDENCE_URL", str(output))
    result_ref = f"{evidence_base}; development-evidence draft asset runtime-result.{sha}.{raw_hash}.json"
    scenarios = [{"id": s["id"], "status": "passed", "featureIds": s["featureIds"], "evidence": [result_ref + "#" + t for t in s["requiredTests"]]} for s in plan["scenarios"]]
    acceptance = [{"id": ac, "status": "passed", "evidence": [result_ref]} for ac in plan["acceptance"]]
    tdd = [{"acceptanceId": "AC-DELETE-CODE", "mode": "test-first", "redEvidence": "source f58999a runtime-ninth 500/403: " + json.dumps(plan["redEvidence"], sort_keys=True), "greenEvidence": result_ref + "#TestServiceRuntimeDeletionRequestWorkerRetryAndReceipt"}]
    tdd += [{"acceptanceId": ac, "mode": "alternative", "reason": "Approved verification tooling and release gate; initial missing archive is preserved. Product API uses test-first separately.", "replacementEvidence": result_ref + "; scripts/qa/test_service_e2e.py and scripts/check_deploy_workflow.sh"} for ac in plan["acceptance"] if ac != "AC-DELETE-CODE"]
    evidence = {"schemaVersion": 1, "repository": "seorilabs/platform", "sourceSha": sha, "target": "service", "productKind": "service", "implementer": "service-implementation", "design": plan["design"], "features": inventory["features"], "acceptance": acceptance, "tdd": tdd, "iterations": [], "additionalIterationApproval": None,
                "e2e": {"phase": "before-build", "sourceSha": sha, "plannedAt": planned, "startedAt": started, "completedAt": completed, "environment": {"kind": "service-runtime", "configurationFingerprint": fp, "isolatedData": context["isolation"]}, "scenarios": scenarios}}
    (output / f"development-evidence.{sha}.service.before-build.json").write_text(json.dumps(evidence, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"sourceSha": sha, "output": str(output), "configurationFingerprint": fp, "passed": counts["pass"]}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--emulator-jar", type=Path)
    parser.add_argument("--output", type=Path)
    parser.add_argument("--fingerprint", action="store_true")
    args = parser.parse_args()
    if args.fingerprint:
        print(fingerprint()[0])
    elif args.emulator_jar is None or args.output is None:
        parser.error("emulator jar and unique output path required")
    else:
        execute(args.emulator_jar.resolve(), args.output.resolve())
