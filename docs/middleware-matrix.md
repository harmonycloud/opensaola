# Original package lifecycle matrix

Run this suite on the local delivery machine with Go, Python 3, Docker, Kind, kubectl and the matching Saola CLI. Each selected case runs sequentially in a separate disposable Kind cluster, with the candidate controller compiled from the checkout. Keep the fixture directory outside the repository.

```bash
export KIND_BIN=/path/to/kind SAOLA_BIN=/path/to/saola
python3 scripts/test-middleware-matrix.py --fixtures /path/to/fixtures --report /tmp/matrix.json
# Run a subset
python3 scripts/test-middleware-matrix.py --fixtures /path/to/fixtures --case qdrant --case minio
```

Each case directory is named after its case below. Supply the original package directory or a symlink named `package`, and a valid single-instance `mid.json` with an explicit namespace. Optional inputs include `mo.json`, `dependencies.yaml` and `images/*.tar` for offline image import. Use the original image names inside the archives. Export an archive for the node architecture or include every platform referenced by its OCI index. The case's workload and service default to the MID name, with a `-svc` service suffix for MinIO. To use a different workload or service name, place `{"workload":"…","service":"…"}` in `case.json` (Kafka follows its existing field-test contract).

| Case | Additional inputs | Business checks |
| --- | --- | --- |
| grafana | Standard 9.5.21 MID with dashboard persistence and admin password | Read empty Role rules, create/read a dashboard through the Service, replace the Pod, read and delete the dashboard |
| qdrant | Standard 1.20.0 MID with optional templates disabled | Write and search a vector through the Service, find it after Pod replacement, delete the collection and the original MID |
| nacos-2.5.1, nacos-3.0.3 | Matching original package, `mo.json`, `dependencies.yaml` providing the database, optional custom `sdkprobe` | Read the single replica and 5 GiB form defaults from the installed baseline, supply the required MID fields with their Kubernetes units, then verify the actual image, 5Gi StatefulSet log template and bound volume. Run the SDK through the Service before and after Pod replacement |
| kafka | `mo.json`, `dependencies.yaml` with the matching SASL-enabled ZooKeeper | Reuse the Kafka mount, withdrawal, SCRAM produce/consume and remount tests, then delete MID, MO and uninstall the package |
| minio | Standard single-instance MID with persistent storage; packaged image provides mc | Write an S3 object by Service name, read it after replacing the Pod, delete the MID and recreate it with the same name, then read the same object from the original claims and remove the object and bucket |

The Nacos SDK probe connects to localhost HTTP 28848 and gRPC 29848 forwarded to Service ports 8848/9848. The repository includes a probe pinned to nacos-sdk-go/v2 v2.3.5 in an independent Go module. The runner builds it once per invocation when a Nacos case has its other fixtures and supplies no custom executable. To provide an explicitly prebuilt probe, use an absolute fixture path:

```bash
(cd hack/nacos-sdk-probe && go build -mod=readonly -o "$FIXTURES/nacos-2.5.1/sdkprobe" .)
ln -s ../nacos-2.5.1/sdkprobe "$FIXTURES/nacos-3.0.3/sdkprobe"
```

The probe waits up to 90 seconds for its own gRPC connection using a read request before its first business write. The runner sets `NACOS_TEST_PASSWORD` from the MID and gives each invocation a fresh `NACOS_PROBE_DIR`. On success the probe must emit a JSON object with true values for `publishConfig`, `readConfig`, `deleteConfig`, `grpcRegister`, `grpcDiscovery` and `grpcDeregister`. Keep passwords in the private fixture directory.

The four single-instance workload types require a Ready replacement Pod with a different UID and unchanged Bound claim identities, then verify workload and service removal, retained claims and package uninstall. Kafka uses its broker-specific lifecycle. The report fingerprints the bytes read back from the installed package Secret. An active target Pod with a confirmed registry credential, DNS or connection failure, or a missing tool or fixture, reports `blocked` (exit 2). A failed assertion or cleanup reports `failed` (exit 1). Exit 0 requires every selected case to pass. The JSON report is updated after each case.
