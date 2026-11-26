"""Run one original package lifecycle inside with-test-kind.sh."""
import base64
import contextlib
import hashlib
import json
import os
import re
import selectors
import shutil
import subprocess
import sys
import tempfile
import time
import uuid
from pathlib import Path
from urllib.error import HTTPError
from urllib.request import Request, urlopen
from urllib.parse import urlencode

from matrix_registry import check_pull_blocker


def run(*args, **kw):
    return subprocess.check_output(args, text=True, timeout=kw.pop("timeout", 360), **kw).strip()


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def wait(description, fn, seconds=420, check_images=False):
    end = time.monotonic() + seconds
    while time.monotonic() < end:
        obj = fn()
        if obj:
            return obj
        time.sleep(2)
    if check_images:
        check_pull_blocker(namespace, workload_pods())
    raise RuntimeError("Timed out waiting for " + description)


def get(kind, name):
    raw = run(*kubectl, "get", kind, name, "--ignore-not-found", "-o", "json")
    return json.loads(raw) if raw else {}


def available(kind, name):
    obj = get(kind, name)
    status = obj.get("status", {})
    return obj and obj["metadata"]["generation"] == status.get("observedGeneration") and status.get("state") == "Available"


def phase(name):
    phases.append(name)
    Path(os.environ["MATRIX_PROGRESS"]).write_text(json.dumps(phases))
    print("PASS", name, flush=True)


def workload_pods():
    workload = get(resource, workload_name)
    if not workload:
        return []
    require(workload["spec"]["replicas"] == 1, "The lifecycle fixture must use one replica")
    match = workload["spec"]["selector"]["matchLabels"]
    selector = ",".join(f"{k}={v}" for k, v in match.items())
    return json.loads(run(*kubectl, "get", "pods", "-l", selector, "-o", "json"))["items"]


def pod(previous=None):
    pods = [item for item in workload_pods() if not item["metadata"].get("deletionTimestamp")]
    if len(pods) != 1:
        return None
    for item in pods:
        meta = item["metadata"]
        if meta.get("deletionTimestamp") or meta["uid"] == previous:
            continue
        if any(c["type"] == "Ready" and c["status"] == "True" for c in item.get("status", {}).get("conditions", [])):
            return item


def claims(p):
    names = [v["persistentVolumeClaim"]["claimName"] for v in p["spec"]["volumes"] if "persistentVolumeClaim" in v]
    require(names, "Expected persistent storage")
    result = {}
    for name in names:
        claim = get("pvc", name)
        require(claim.get("status", {}).get("phase") == "Bound", "PVC is not Bound: " + name)
        result[name] = claim["metadata"]["uid"]
    return result


@contextlib.contextmanager
def forward(*ports):
    with subprocess.Popen([*kubectl, "port-forward", "--address=127.0.0.1",
                           "service/" + service, *ports], stdout=subprocess.PIPE) as proc:
        try:
            with selectors.DefaultSelector() as sel:
                sel.register(proc.stdout, selectors.EVENT_READ)
                require(sel.select(30), "Timed out starting Service port-forward")
                line = proc.stdout.readline().decode()
            match = re.search(r"127\.0\.0\.1:(\d+)", line)
            require(match, "Service port-forward failed")
            yield "http://127.0.0.1:" + match.group(1)
        finally:
            proc.terminate()


def http(base, path, method="GET", body=None, **headers):
    data = None if body is None else json.dumps(body).encode()
    with urlopen(Request(base + path, data, {"Content-Type": "application/json", **headers},
                         method=method), timeout=20) as resp:
        return json.load(resp)


def protocol(p, after, work, remove=False):
    if case.startswith("nacos"):
        with forward("28848:8848", "29848:9848") as url, tempfile.TemporaryDirectory(dir=work) as sdkdir:
            def login_ready():
                data = urlencode({"username": "nacos", "password": params["auth"]["password"]}).encode()
                try:
                    with urlopen(Request(url + "/nacos/v1/auth/login", data=data), timeout=10) as response:
                        return json.load(response).get("accessToken")
                except (OSError, ValueError):
                    return False
            wait("Nacos Service login", login_ready, seconds=120)
            out = run(os.environ.get("MATRIX_NACOS_SDK", str(fixtures / "sdkprobe")), env=dict(os.environ, NACOS_PROBE_DIR=sdkdir, NACOS_TEST_PASSWORD=params["auth"]["password"]))
            result = json.loads(out)
            require(all(result.get(k) is True for k in ("publishConfig", "readConfig", "deleteConfig",
                        "grpcRegister", "grpcDiscovery", "grpcDeregister")), "Nacos SDK operations incomplete")
        return
    if case == "minio":
        with forward(":9000") as url:
            def s3_ready():
                try:
                    with urlopen(url + "/minio/health/cluster", timeout=6) as response:
                        return response.status == 200
                except OSError:
                    return False
            wait("MinIO S3 write quorum", s3_ready, seconds=150)
        cmd = """set -eu
d=$(mktemp -d)
trap 'rm -rf "$d"' EXIT
mc --config-dir "$d" alias set qa "$1" "$MINIO_ACCESS_KEY" "$MINIO_SECRET_KEY" >/dev/null
if [ "$3" = seed ]; then
  mc --config-dir "$d" mb "qa/$2"
  printf %s "$2" | mc --config-dir "$d" pipe "qa/$2/proof" >/dev/null
fi
test "$(mc --config-dir "$d" cat "qa/$2/proof")" = "$2"
if [ "$3" = remove ]; then mc --config-dir "$d" rm "qa/$2/proof"; mc --config-dir "$d" rb "qa/$2"; fi"""
        run(*kubectl, "exec", p["metadata"]["name"], "--", "sh", "-ec", cmd, "_",
            f"http://{service}:9000", key, "remove" if remove else "verify" if after else "seed")
        return
    port = 6333 if case == "qdrant" else get("service", service)["spec"]["ports"][0]["port"]
    with forward(f":{port}") as url:
        if case == "qdrant":
            headers = {"api-key": params.get("apiKey", "")}
            path = "/collections/" + key
            if not after:
                http(url, path, "PUT", {"vectors": {"size": 2, "distance": "Cosine"}}, **headers)
                http(url, path + "/points?wait=true", "PUT", {"points": [{"id": 42, "vector": [1, 0]}]}, **headers)
            hits = http(url, path + "/points/search", "POST", {"vector": [1, 0], "limit": 1}, **headers)
            require(hits["result"][0]["id"] == 42, "Qdrant point missing")
        else:
            headers = {"Authorization": "Basic " + base64.b64encode(("admin:" + params["adminPassword"]).encode()).decode()}
            path = "/api/dashboards/uid/" + key
            if not after:
                http(url, "/api/dashboards/db", "POST", {"dashboard": {"uid": key, "title": key, "panels": []}}, **headers)
            require(http(url, path, **headers)["dashboard"]["title"] == key, "Grafana dashboard missing")
        if after:
            http(url, path, "DELETE", **headers)
            try:
                http(url, path, **headers)
            except HTTPError as err:
                require(err.code == 404, "Expected HTTP 404 after deletion")
            else:
                raise RuntimeError("Deleted business object is still readable")


def lifecycle(work):
    for ns in {namespace, "middleware-operator"}:
        if not run("kubectl", "get", "ns", ns, "--ignore-not-found", "-o", "name"):
            run("kubectl", "create", "ns", ns)
    for archive in sorted((fixtures / "images").glob("*.tar")):
        with archive.open("rb") as inp:
            subprocess.run(["docker", "exec", "-i", cluster + "-control-plane", "ctr", "-n", "k8s.io",
                            "images", "import", "--local", "--all-platforms", "-"], stdin=inp, check=True, timeout=300)
    dependencies = fixtures / "dependencies.yaml"
    if dependencies.exists():
        run(*kubectl, "apply", "-f", dependencies)
    cli = os.environ["SAOLA_BIN"]
    original = (fixtures / "package").resolve()
    run(cli, "validate", original)
    run(cli, "build", original, "-o", work / "original.pkg")
    run(cli, "install", original, "--wait", "5m")
    phase("install")
    mo = fixtures / "mo.json"
    if mo.exists():
        run(cli, "create", "-f", mo)
        mo_name = json.loads(mo.read_text())["metadata"]["name"]
        wait("MO Available", lambda: available("mo", mo_name))
    manifest = fixtures / "mid.json"
    if case.startswith("nacos"):
        baseline = get("mb", mid["spec"]["baseline"])
        schema = baseline["spec"]["necessary"]["resource"]["nacos"]
        def default_value(entry):
            return json.loads(entry)["default"] if isinstance(entry, str) else entry["default"]
        capacity = str(default_value(schema["filelogVolume"]["size"])) + "Gi"
        replicas = default_value(schema["replicas"])
        require(capacity == "5Gi" and replicas == 1, "Unexpected Nacos package defaults")
        params["resource"]["nacos"]["filelogVolume"]["size"] = capacity
        params["resource"]["nacos"]["replicas"] = replicas
        manifest = work / "default-nacos.json"
        manifest.write_text(json.dumps(mid))
        phase("original-baseline-defaults-1-replica-5Gi")
    run(cli, "create", "-f", manifest)
    wait("MID Available at current generation", lambda: available("mid", instance))
    meta = get("mid", instance)["metadata"]
    package = meta["labels"]["middleware.cn/packagename"]
    installed = run("kubectl", "-n", "middleware-operator", "get", "secret", package, "-o", "jsonpath={.data.package}")
    require(installed, "Installed package archive is missing")
    phase("installed-package-sha256=" + hashlib.sha256(base64.b64decode(installed, validate=True)).hexdigest())
    phase("publish")
    before = wait("first ready Pod", pod, check_images=True)
    volumes = claims(before)
    if case == "qdrant":
        require(not mid["spec"].get("configurations"), "Expected Qdrant baseline configuration defaults")
        baseline = get("mb", mid["spec"]["baseline"])
        references = {ref["name"] for ref in baseline["spec"]["configurations"]}
        require({"qdrant-secret", "qdrant-ingress", "qdrant-pdb", "qdrant-servicemonitor"} <= references,
                "Baseline no longer references the four historical optional configurations")
        for kind, name in [("secret", instance + "-apikey"), ("ingress", instance), ("pdb", instance)]:
            require(not get(kind, name), "Expected optional Qdrant " + kind + " to be absent")
        if "servicemonitors.monitoring.coreos.com" in run("kubectl", "api-resources", "--verbs=get", "-o", "name").splitlines():
            require(not get("servicemonitor", instance), "Expected optional Qdrant ServiceMonitor to be absent")
        phase("four-optional-configurations-absent")
    if case == "grafana":
        role = get("role", instance)
        require(role and not role.get("rules"), "Expected empty native Grafana Role rules")
    if case.startswith("nacos"):
        sts = get(resource, workload_name)["spec"]
        require(sts["replicas"] == 1 and any(v["metadata"]["name"] == "nacos-logs" and
                v["spec"]["resources"]["requests"]["storage"] == "5Gi" for v in sts["volumeClaimTemplates"]),
                "Expected one Nacos replica and 5Gi log template")
        name = next(v["persistentVolumeClaim"]["claimName"] for v in before["spec"]["volumes"] if v["name"] == "nacos-logs")
        require(get("pvc", name)["status"]["capacity"]["storage"] == "5Gi", "Nacos log PVC capacity changed")
        image = next(c["image"] for c in before["spec"]["containers"] if c["name"] == "nacos")
        require(image.rsplit(":", 1)[-1].lstrip("v") == case[6:], "Unexpected Nacos image version")
    phase("workload-ready-pvc-bound")
    protocol(before, False, work)
    phase("business-before-restart")
    run(*kubectl, "delete", "pod", before["metadata"]["name"], "--wait=false")
    after = wait("replacement ready Pod", lambda: pod(before["metadata"]["uid"]), check_images=True)
    require(claims(after) == volumes, "PVC identity changed")
    protocol(after, True, work)
    phase("business-after-restart")
    def remove_instance(current):
        run(cli, "delete", "middleware", instance, "-n", namespace, "--wait", "5m")
        for kind, name in ((resource, workload_name), ("service", service), ("pod", current["metadata"]["name"])):
            wait("deleted " + kind, lambda: not get(kind, name))
        for name, uid in volumes.items():
            require(get("pvc", name).get("metadata", {}).get("uid") == uid, "Expected retained PVC " + name)

    remove_instance(after)
    if case == "minio":
        run(cli, "create", "-f", fixtures / "mid.json")
        wait("recreated MID Available", lambda: available("mid", instance))
        require(get("mid", instance)["metadata"]["uid"] != meta["uid"], "Expected a new MID identity")
        recreated = wait("ready Pod after same-name recreation", lambda: pod(after["metadata"]["uid"]), check_images=True)
        require(claims(recreated) == volumes, "Recreated MID did not reuse the original claims")
        protocol(recreated, True, work, remove=True)
        phase("same-name-mid-original-pvc-object-read")
        remove_instance(recreated)
    if mo.exists():
        run(cli, "delete", "operator", mo_name, "-n", namespace, "--wait", "5m")
    run(cli, "uninstall", package, "--wait", "5m")
    require(not get("middlewarepackage", package), "Package remains after uninstall")
    phase("delete-retain-pvc-uninstall")


if __name__ == "__main__":
    case, directory = sys.argv[1:]
    fixtures = Path(directory)
    cluster = os.environ["KIND_CLUSTER"]
    require(cluster.startswith("opensaola-e2e-") and run("kubectl", "config", "current-context") == "kind-" + cluster,
            "Expected fresh matrix Kind context")
    mid = json.loads((fixtures / "mid.json").read_text())
    namespace, instance = mid["metadata"]["namespace"], mid["metadata"]["name"]
    kubectl = ["kubectl", "-n", namespace]
    params = mid["spec"].get("necessary", {})
    options = json.loads((fixtures / "case.json").read_text()) if (fixtures / "case.json").exists() else {}
    workload_name = options.get("workload", instance)
    service = options.get("service", instance + "-svc" if case == "minio" else instance)
    resource = "deployment" if case == "grafana" else "statefulset"
    key = "osa" + uuid.uuid4().hex[:16]
    phases = []
    with tempfile.TemporaryDirectory(prefix="osa-business-") as tmp:
        work = Path(tmp)
        run("kubectl", "apply", "--server-side", "-f", "config/crd/bases")
        run("kubectl", "wait", "--for=condition=Established", "--timeout=90s", "-f", "config/crd/bases")
        run("go", "build", "-o", work / "manager", "./cmd")
        (work / "pkg/config").mkdir(parents=True)
        shutil.copy("pkg/config/config.yaml", work / "pkg/config/config.yaml")
        with (work / "manager.log").open("w") as log, subprocess.Popen(
                [work / "manager", "--leader-elect=false", "--metrics-bind-address=0", "--health-probe-bind-address=0"],
                cwd=work, env={k:v for k,v in os.environ.items() if k != "opensaola_env"}, stdout=log, stderr=log) as manager:
            try:
                lifecycle(work)
            finally:
                manager.terminate()
