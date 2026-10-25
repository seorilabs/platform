#!/usr/bin/env python3
"""Wait for a successful, completed whole-service CI run of this exact candidate."""
import argparse
import json
import os
import re
import subprocess
import time

WORKFLOW = ".github/workflows/service-runtime.yml"


def successful_run(runs, sha):
    return next((run for run in runs if run.get("head_sha") == sha
                 and run.get("path") == WORKFLOW
                 and run.get("head_branch") == "main"
                 and run.get("event") in ("push", "workflow_dispatch")
                 and run.get("status") == "completed"
                 and run.get("conclusion") == "success"), None)


def wait(sha, timeout=1200):
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("invalid candidate SHA")
    deadline = time.monotonic() + timeout
    while True:
        raw = subprocess.check_output([
            "gh", "api", f"repos/seorilabs/platform/actions/workflows/service-runtime.yml/runs?head_sha={sha}&per_page=100"
        ])
        run = successful_run(json.loads(raw)["workflow_runs"], sha)
        if run:
            print(f"Completed whole service runtime: {run['html_url']}")
            if os.environ.get("GITHUB_OUTPUT"):
                with open(os.environ["GITHUB_OUTPUT"], "a") as output:
                    output.write(f"run_id={int(run['id'])}\nrun_attempt={int(run['run_attempt'])}\n")
            return
        if time.monotonic() >= deadline:
            raise TimeoutError("no completed successful whole service runtime for this candidate")
        time.sleep(min(10, max(0, deadline - time.monotonic())))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--sha", required=True)
    wait(parser.parse_args().sha)
