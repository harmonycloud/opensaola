"""Classify an image pull only when kubelet reports the underlying cause."""
import json
import os
import re
import subprocess
from pathlib import Path


def classify(message):
    if re.search(r"manifest unknown|manifest[^;]*not found|no matching manifest|404 Not Found", message, re.I):
        return None
    patterns = {
        "registry-authentication": r"unauthorized|authentication required|no basic auth credentials|insufficient.scope|403 forbidden|401 unauthorized",
        "registry-network": r"no such host|i/o timeout|TLS handshake timeout|connection refused|network is unreachable|context deadline exceeded",
    }
    return next((reason for reason, p in patterns.items() if re.search(p, message, re.I)), None)


def check_pull_blocker(namespace, pods):
    for pod in filter(None, pods):
        status = pod.get("status", {})
        containers = status.get("initContainerStatuses", []) + status.get("containerStatuses", [])
        for container in containers:
            waiting = container.get("state", {}).get("waiting", {})
            if waiting.get("reason") not in ("ErrImagePull", "ImagePullBackOff"):
                continue
            messages = [waiting.get("message", "")]
            uid = pod["metadata"]["uid"]
            events = subprocess.run(["kubectl", "-n", namespace, "get", "events", "--field-selector",
                                     "involvedObject.uid=" + uid + ",reason=Failed", "-o", "json"],
                                    capture_output=True, text=True, timeout=20)
            if not events.returncode:
                messages += [e.get("message", "") for e in json.loads(events.stdout)["items"]
                             if container["image"] in e.get("message", "") and "pull" in e.get("message", "").lower()]
            for message in messages:
                reason = classify(message)
                if reason:
                    evidence = {"cause": reason, "pod": pod["metadata"]["name"], "container": container["name"]}
                    if "MATRIX_BLOCKER" in os.environ:
                        Path(os.environ["MATRIX_BLOCKER"]).write_text(json.dumps(evidence))
                    print("BLOCKED", evidence, flush=True)
                    raise SystemExit(76)
