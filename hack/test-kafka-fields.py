"""Exercise a Kafka package's broker mount and protocol in the isolated Kind test."""
import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid


def run(*args, stdin=None, timeout=120):
    return subprocess.run(args, input=stdin, text=True, stdout=subprocess.PIPE,
                          timeout=timeout, check=True).stdout


def wait_for(description, read):
    deadline = time.monotonic() + int(os.environ.get("KAFKA_READY_SECONDS", "900"))
    while time.monotonic() < deadline:
        result = read()
        if result:
            print(description, flush=True)
            return result
        time.sleep(3)
    raise RuntimeError("Timed out waiting for " + description)


def get(ns, kind, name):
    raw = run("kubectl", "-n", ns, "get", kind, name, "--ignore-not-found", "-o", "json")
    return json.loads(raw) if raw.strip() else {}


def ready_broker(ns, name, mounted):
    pod = get(ns, "pod", name + "-broker-0")
    if pod.get("metadata", {}).get("deletionTimestamp"):
        return
    if not any(c["type"] == "Ready" and c["status"] == "True"
               for c in pod.get("status", {}).get("conditions", [])):
        return
    broker = next((c for c in pod["spec"]["containers"] if c["name"] == "broker"), {})
    paths = [m["mountPath"] for m in broker.get("volumeMounts", []) if m["name"] == "log"]
    volumes = [v for v in pod["spec"].get("volumes", []) if v["name"] == "log"]
    if mounted:
        matches = paths == ["/root/logs"] and len(volumes) == 1 and "emptyDir" in volumes[0]
    else:
        matches = not paths and not volumes
    if matches:
        mid = get(ns, "mid", name)
        if available(mid):
            return pod


def available(obj):
    status = obj.get("status", {})
    return bool(obj and obj["metadata"]["generation"] == status.get("observedGeneration")
                and status.get("state") == "Available")


def verify_writable(exec_cmd):
    run(*exec_cmd, "sh", "-ec", """
      awk '$2 == "/root/logs" {found=1} END {exit !found}' /proc/mounts
      f=$(mktemp /root/logs/test.XXXXXX); trap 'rm -f "$f"' EXIT
      echo test > "$f"; test "$(cat "$f")" = test
    """)


def protocol(exec_cmd, pod, name, topic, operation):
    init = next(c for c in pod["spec"]["initContainers"] if c["name"] == "init")
    env = {v["name"]: v.get("value") for v in init["env"]}
    if not env.get("BrokerUser") or not env.get("BrokerPwd"):
        raise ValueError("Expected SCRAM credentials in the broker's init container")
    client = ("security.protocol=SASL_PLAINTEXT\nsasl.mechanism=SCRAM-SHA-256\n"
              "sasl.jaas.config=org.apache.kafka.common.security.scram.ScramLoginModule required "
              f"username={json.dumps(env['BrokerUser'])} password={json.dumps(env['BrokerPwd'])};\n")
    # The credentials travel through stdin only. This path is unique to the
    # disposable test and permissions are set before writing the file.
    config = run(*exec_cmd, "sh", "-ec", 'umask 077; f=$(mktemp); cat > "$f"; echo "$f"',
                 stdin=client).strip()
    commands = {
        "produce": """
            ./kafka-topics.sh --bootstrap-server "$1" --command-config "$2" --create --topic "$3" --partitions 1 --replication-factor 1
            printf '%s\n' "$3" | ./kafka-console-producer.sh --bootstrap-server "$1" --producer.config "$2" --producer-property acks=all --topic "$3"
        """,
        "consume": """
            ./kafka-console-consumer.sh --bootstrap-server "$1" --consumer.config "$2" --topic "$3" --from-beginning --max-messages 1 --timeout-ms 30000
            ./kafka-topics.sh --bootstrap-server "$1" --command-config "$2" --delete --topic "$3"
        """,
    }
    try:
        output = run(*exec_cmd, "bash", "-ec", """
          unset KAFKA_OPTS JMX_PORT
          export KAFKA_HEAP_OPTS='-Xms64m -Xmx256m'
          cd /opt/kafka/bin
        """ + commands[operation], "_", name + "-headless-svc:9092", config, topic, timeout=240)
        if operation == "consume" and topic not in output.splitlines():
            raise RuntimeError("Consumer did not return the message written before rollout")
    finally:
        run(*exec_cmd, "rm", "-f", config)


def update_parameters(ns, name, params):
    patch = [{"op": "replace", "path": "/spec/parameters", "value": params}]
    run("kubectl", "-n", ns, "patch", "mid", name, "--type=json", "--patch-file=/dev/stdin",
        stdin=json.dumps(patch))


def main():
    ctx = run("kubectl", "config", "current-context").strip()
    if not ctx.startswith("kind-opensaola-intent-kafka-"):
        raise RuntimeError("Kafka test requires its dedicated Kind cluster")
    fixtures = Path(os.environ["KAFKA_FIXTURES"])
    mid = json.loads((fixtures / "mid.json").read_text())
    mo = json.loads((fixtures / "mo.json").read_text())
    ns, name = mid["metadata"]["namespace"], mid["metadata"]["name"]
    if mo["metadata"]["namespace"] != ns:
        raise ValueError("The MO and MID fixtures must use the same test namespace")
    saola = os.environ.get("SAOLA_BIN", "saola")
    cli = [saola, "--kubeconfig", os.environ["KUBECONFIG"], "--context", ctx]
    phase = sys.argv[1]
    checkpoint = Path(os.environ["KUBECONFIG"]).with_name("checkpoint.json")
    exec_cmd = ["kubectl", "-n", ns, "exec", "-i", name + "-broker-0", "-c", "broker", "--"]
    if phase == "prepare":
        run("kubectl", "create", "namespace", ns)
        package_ns = "middleware-operator"
        if ns != package_ns:
            run("kubectl", "create", "namespace", package_ns)
        run("kubectl", "apply", "-n", ns, "-f", str(fixtures / "dependencies.yaml"))
        run(*cli, "validate", os.environ["KAFKA_PACKAGE_DIR"])
        run(*cli, "install", os.environ["KAFKA_PACKAGE_DIR"], "--pkg-namespace", package_ns, "--wait", "3m", timeout=210)
        run(*cli, "create", "-f", str(fixtures / "mo.json"))
        wait_for("Kafka operator available", lambda: available(get(ns, "mo", mo["metadata"]["name"])))

        params = mid["spec"].setdefault("parameters", {})
        component = copy.deepcopy(params.get("statefulsetComponent", {}))
        podspec = component.setdefault("broker", {}).setdefault("spec", {}).setdefault("template", {}).setdefault("spec", {})
        volumes = podspec.setdefault("volumes", [])
        containers = podspec.setdefault("containers", [])
        broker = next((c for c in containers if c["name"] == "broker"), None)
        if broker is None:
            broker = {"name": "broker"}
            containers.append(broker)
        mounts = broker.setdefault("volumeMounts", [])
        if any(v["name"] == "log" for v in volumes + mounts):
            raise ValueError("The base MID must leave the test volume 'log' undeclared")
        volumes.append({"name": "log", "emptyDir": {}})
        mounts.append({"name": "log", "mountPath": "/root/logs"})
        params["statefulsetComponent"] = component
        # saola create needs a file. Use the private test directory.
        mounted_file = Path(os.environ["KUBECONFIG"]).with_name("mounted-mid.json")
        mounted_file.write_text(json.dumps(mid))
        run(*cli, "create", "-f", str(mounted_file))
        before = wait_for("Ready broker with declared volume", lambda: ready_broker(ns, name, True))
        verify_writable(exec_cmd)
        topic = "field-test-" + uuid.uuid4().hex
        protocol(exec_cmd, before, name, topic, "produce")
        cr = get(ns, "kafkacluster", name)
        history = cr["metadata"]["annotations"]["middleware.cn/resource-intent"]
        record = get(ns, "secret", history)
        meta = record["metadata"]
        immutable = record.get("immutable") is True
        if os.environ.get("KAFKA_PREVIOUS_MANAGER"):
            if not immutable and meta.get("labels", {}).get("middleware.cn/field-history") != "v1":
                raise RuntimeError("Unrecognized history format on the previous controller")
            if meta.get("annotations", {}).get("middleware.cn/resource-intent-operation"):
                raise RuntimeError("Expected confirmed history before switching controllers")
            print(f"Previous writer confirmed {'immutable' if immutable else 'ledger v1'} "
                  f"history {meta['uid']} at resourceVersion {meta['resourceVersion']}", flush=True)
        checkpoint.write_text(json.dumps({"topic": topic, "podUID": before["metadata"]["uid"],
            "crUID": cr["metadata"]["uid"], "record": history, "immutable": immutable,
            "recordUID": meta["uid"], "recordRV": meta["resourceVersion"],
            "midUID": get(ns, "mid", name)["metadata"]["uid"]}))
        print("Created a topic and produced a message on the ready broker", flush=True)
        return
    if phase != "withdraw":
        raise ValueError("Expected prepare or withdraw")
    original = mid["spec"].get("parameters", {})
    saved = json.loads(checkpoint.read_text())
    if os.environ.get("KAFKA_PREVIOUS_MANAGER"):
        # A normal MID update first lets the new writer record continued
        # declarations using current list topology before their later removal.
        obj = get(ns, "mid", name)
        args = obj["spec"]["parameters"]
        args["statefulsetComponent"]["broker"]["spec"]["template"].setdefault("metadata", {}).setdefault("annotations", {})["field-test-revision"] = "upgrade"
        update_parameters(ns, name, args)
        def confirmed_upgrade():
            cr = get(ns, "kafkacluster", name)
            if cr.get("metadata", {}).get("uid") != saved["crUID"]:
                raise RuntimeError("Kafka CR was replaced during the controller switch")
            template = cr["spec"]["statefulsetComponent"]["broker"]["spec"]["template"]
            if template.get("metadata", {}).get("annotations", {}).get("field-test-revision") != "upgrade":
                return
            name_on_cr = cr["metadata"]["annotations"]["middleware.cn/resource-intent"]
            meta = get(ns, "secret", name_on_cr).get("metadata", {})
            if (meta.get("labels", {}).get("middleware.cn/field-history") != "v1"
                    or meta.get("annotations", {}).get("middleware.cn/resource-intent-operation")
                    or not available(get(ns, "mid", name))):
                return
            if saved["immutable"]:
                if name_on_cr != saved["record"] and meta["uid"] != saved["recordUID"]:
                    return meta
            elif (name_on_cr == saved["record"] and meta["uid"] == saved["recordUID"]
                  and meta["resourceVersion"] != saved["recordRV"]):
                return meta
        meta = wait_for("New writer confirmed the updated declaration for the same Kafka CR", confirmed_upgrade)
        print(f"Candidate writer confirmed ledger v1 history {meta['uid']} "
              f"at resourceVersion {meta['resourceVersion']}", flush=True)
    update_parameters(ns, name, original)
    after = wait_for("Ready broker after mount withdrawal", lambda: ready_broker(ns, name, False))
    if saved["podUID"] == after["metadata"]["uid"]:
        raise RuntimeError("Expected the broker to roll after withdrawal")
    run(*exec_cmd, "sh", "-ec", """awk '$2 == "/root/logs" {bad=1} END {exit bad}' /proc/mounts""")
    protocol(exec_cmd, after, name, saved["topic"], "consume")
    mounted_file = Path(os.environ["KUBECONFIG"]).with_name("mounted-mid.json")
    update_parameters(ns, name, json.loads(mounted_file.read_text())["spec"]["parameters"])
    remounted = wait_for("Existing Ready Kafka accepted the MID volume and mount",
                         lambda: ready_broker(ns, name, True))
    if remounted["metadata"]["uid"] == after["metadata"]["uid"]:
        raise RuntimeError("Expected the broker to roll after adding the mount")
    verify_writable(exec_cmd)
    if (get(ns, "mid", name)["metadata"]["uid"] != saved["midUID"]
            or get(ns, "kafkacluster", name)["metadata"]["uid"] != saved["crUID"]):
        raise RuntimeError("MID or primary Kafka CR was replaced")
    print(f"Same MID {saved['midUID']} and Kafka CR {saved['crUID']}; "
          f"Ready Pod UIDs {saved['podUID']} -> {after['metadata']['uid']} -> {remounted['metadata']['uid']}")
    print("Kafka mount read/write, unmount and SASL produce/consume passed")


if __name__ == "__main__":
    main()
