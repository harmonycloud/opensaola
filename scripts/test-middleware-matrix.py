#!/usr/bin/env python3
"""Run package regressions in fresh Kind clusters. See docs/middleware-matrix.md."""
import argparse
from functools import lru_cache
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CASES = ("grafana", "qdrant", "nacos-2.5.1", "nacos-3.0.3", "kafka", "minio")


def missing_inputs(case, directory, env):
    required = ["package/metadata.yaml", "mid.json"]
    if case.startswith("nacos") or case == "kafka":
        required += ["mo.json", "dependencies.yaml"]
    missing = [str(directory / p) for p in required if not (directory / p).is_file()]
    probe = directory / "sdkprobe"
    if case.startswith("nacos") and os.path.lexists(probe) and not (probe.is_file() and os.access(probe, os.X_OK)):
        missing.append("executable sdkprobe")
    for tool in [env["SAOLA_BIN"], env["KIND_BIN"], "go", "kubectl", "docker"]:
        if not shutil.which(tool):
            missing.append(tool)
    return missing


@lru_cache(maxsize=1)
def build_sdk(output):
    return subprocess.run(["go", "build", "-mod=readonly", "-o", str(output), "."],
                          cwd=ROOT / "hack/nacos-sdk-probe",
                          env=dict(os.environ, GOWORK="off")).returncode


def run_case(case, directory, env, sdk_output):
    missing = missing_inputs(case, directory, env)
    if missing:
        return {"status": "blocked", "missing": missing}
    with tempfile.TemporaryDirectory(prefix="osa-matrix-") as tmp:
        progress = Path(tmp) / "phases.json"
        blocker = Path(tmp) / "blocker.json"
        env = dict(env, MATRIX_PROGRESS=str(progress), MATRIX_BLOCKER=str(blocker))
        if case.startswith("nacos"):
            probe = directory / "sdkprobe"
            if not probe.exists():
                if build_sdk(sdk_output) != 0:
                    return {"status": "failed", "phases": [], "error": "Nacos SDK probe build failed"}
                probe = sdk_output
            env["MATRIX_NACOS_SDK"] = str(probe)
        if case == "kafka":
            env.update(KAFKA_FIXTURES=str(directory), KAFKA_PACKAGE_DIR=str((directory / "package").resolve()))
            cmd = ["bash", "hack/test-field-workload.sh", "kafka"]
        else:
            cmd = ["bash", "hack/with-test-kind.sh", sys.executable,
                   "hack/middleware-matrix-case.py", case, str(directory)]
        status = subprocess.run(cmd, cwd=ROOT, env=env).returncode
        phases = json.loads(progress.read_text()) if progress.exists() else []
        if status == 76 and blocker.is_file():
            return {"status": "blocked", "evidence": json.loads(blocker.read_text()), "phases": phases}
        if status == 0:
            phases.append("cluster-cleanup")
        return {"status": "passed" if status == 0 else "failed", "phases": phases,
                "exit_code": status}


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--case", choices=CASES, action="append", dest="cases")
    ap.add_argument("--fixtures", type=Path, required=True)
    ap.add_argument("--report", type=Path)
    args = ap.parse_args()
    env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1", SAOLA_BIN=os.environ.get("SAOLA_BIN", "saola"),
               KIND_BIN=os.environ.get("KIND_BIN", "kind"))
    for key in ("SAOLA_BIN", "KIND_BIN"):
        exe = shutil.which(env[key])
        if exe:
            env[key] = str(Path(exe).resolve())
    results = {}
    def save_report():
        if args.report:
            args.report.parent.mkdir(parents=True, exist_ok=True)
            with tempfile.NamedTemporaryFile(dir=args.report.parent, mode="w", delete=False) as output:
                json.dump(results, output, indent=2)
                output.write("\n")
            os.replace(output.name, args.report)

    with tempfile.TemporaryDirectory(prefix="osa-matrix-tools-") as output_dir:
        for case in dict.fromkeys(args.cases or CASES):
            try:
                results[case] = run_case(case, args.fixtures.resolve() / case, env, Path(output_dir) / "sdkprobe")
            except KeyboardInterrupt:
                results[case] = {"status": "interrupted"}
                save_report()
                return 130
            except (OSError, ValueError) as err:
                results[case] = {"status": "failed", "error": str(err)}
            save_report()
            print(case, results[case]["status"], flush=True)
    text = json.dumps(results, indent=2)
    print(text)
    states = {r["status"] for r in results.values()}
    return 1 if "failed" in states else 2 if "blocked" in states else 0


if __name__ == "__main__":
    sys.exit(main())
