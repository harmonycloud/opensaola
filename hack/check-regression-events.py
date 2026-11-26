"""Stream Go test output and check that mandatory regression cases actually passed."""
import json
import sys

# A public behavior can require both an accepted and a rejected API response.
CONFIG = "internal/service/middlewareconfiguration"
API = "TestConfigurationAdoptionAPI/"
REQUIRED = {
    "StatefulSet PVC capacity": (CONFIG, [
        API + "StatefulSetFirstPublish/apiserver_defaults_do_not_change_declared_storage",
        API + "StatefulSetFirstPublish/admission_rewrites_declared_storage",
    ]),
    "LimitRange withdrawal and explicit values": (CONFIG, [
        API + "AtomicDefaults/release_limits_to_apiserver_defaults",
        API + "AtomicDefaults/limitrange_explicit_request_rewritten",
    ]),
    "Configuration partial write recovery": (CONFIG, [
        API + "PartialInventory/MID/restart_recover",
        API + "PartialInventory/MO/restart_recover",
    ]),
    "optional Configuration deletion": (CONFIG, [
        "TestDeleteConfigurationWithNoRecordedTarget/enabled_but_only_empty_documents",
        "TestDeleteConfigurationWithNoRecordedTarget/empty_mapping_is_content",
    ]),
    "RBAC list defaulting": ("internal/k8s", [
        "TestDeclaredNativeOmitEmptyProjection/empty_associative_list_accepts_present_null",
        "TestDeclaredNativeOmitEmptyProjection/present_null_cannot_supply_a_declared_list_member",
    ]),
    "action steps validation": ("internal/service/middlewareactionbaseline", [
        "TestActionBaselineCheck_OmittedSteps", "TestActionBaselineCheck_ExplicitEmptySteps",
    ]),
    "pause and merge": ("internal/controller", ["TestMiddlewarePauseMergeStateMachine"]),
    "package version ownership": ("internal/service/middlewarepackage", [
        "TestDeletePackageResourceOwnership/current_owner",
        "TestDeletePackageResourceOwnership/already_moved_to_B",
    ]),
    "retained resources": (CONFIG, [
        "TestOwnerDeleteDetachesOnlyKnownOrphan/legacy_target_without_inventory",
        "TestOwnerDeleteDetachesOnlyKnownOrphan/legacy_target_with_conflicting_owner_identity",
    ]),
    "HTTP action receipts": ("internal/controller", [
        "TestActionHTTPReceipts/299", "TestActionHTTPReceipts/300", "TestActionHTTPReceipts/503",
    ]),
    "watcher lifetime": ("internal/service/watcher", [
        "TestCustomResourceWatcher_ConcurrentMembership",
    ]),
}


def main():
    missing = {("github.com/harmonycloud/opensaola/" + package, test): behavior
               for behavior, (package, tests) in REQUIRED.items() for test in tests}
    for line in sys.stdin:
        event = json.loads(line)
        if "Output" in event:
            sys.stdout.write(event["Output"])
        if event["Action"] == "pass":
            missing.pop((event.get("Package"), event.get("Test")), None)
    for (package, test), behavior in missing.items():
        print(f"Missing PASS for {behavior}: {package}/{test}", file=sys.stderr)
    if missing:
        return 1
    print(f"Verified all named assertions for {len(REQUIRED)} historical behaviors.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
