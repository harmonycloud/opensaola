#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
resolver="${script_dir}/resolve-saola-cli-release.sh"
lock_helper="${script_dir}/saola-cli-lock.sh"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

assert_rejected() {
  local description="$1"
  shift

  if "$@" >"${tmp_dir}/rejected.stdout" 2>"${tmp_dir}/rejected.stderr"; then
    fail "accepted ${description}"
  fi
}

source_repo="${tmp_dir}/saola-cli-source"
release_dir="${tmp_dir}/release"
releases_json="${tmp_dir}/releases.json"
tag_refs="${tmp_dir}/tag-refs.txt"
mkdir -p "${source_repo}" "${release_dir}"

git -C "${source_repo}" init -q
git -C "${source_repo}" config user.name 'Release Resolver Test'
git -C "${source_repo}" config user.email 'release-resolver@example.invalid'

cat >"${source_repo}/Makefile" <<'EOF'
.PHONY: release-build release-checksums

release-build:
	@test -z "$${GITHUB_OUTPUT+x}"
	@test -z "$${GITHUB_ENV+x}"
	@test -z "$${GITHUB_PATH+x}"
	@test -z "$${GITHUB_STEP_SUMMARY+x}"
	@mkdir -p dist
	@printf 'version=%s\ncommit=%s\nepoch=%s\narch=amd64\nsource=%s\n' \
		'$(VERSION)' '$(GIT_COMMIT)' '$(SOURCE_DATE_EPOCH)' "$$(cat release-payload.txt)" \
		>dist/saola-linux-amd64
	@printf 'version=%s\ncommit=%s\nepoch=%s\narch=arm64\nsource=%s\n' \
		'$(VERSION)' '$(GIT_COMMIT)' '$(SOURCE_DATE_EPOCH)' "$$(cat release-payload.txt)" \
		>dist/saola-linux-arm64

release-checksums: release-build
	@cd dist && shasum -a 256 saola-linux-amd64 saola-linux-arm64 >SHA256SUMS
EOF
printf 'release-1.0.0\n' >"${source_repo}/release-payload.txt"
git -C "${source_repo}" add Makefile release-payload.txt
GIT_AUTHOR_DATE='2025-01-01T00:00:00Z' GIT_COMMITTER_DATE='2025-01-01T00:00:00Z' \
  git -C "${source_repo}" commit -q -m 'release 1.0.0'
GIT_COMMITTER_DATE='2025-01-01T00:00:00Z' git -C "${source_repo}" tag -a v1.0.0 -m v1.0.0
old_commit="$(git -C "${source_repo}" rev-parse HEAD)"

printf 'release-1.10.0\n' >"${source_repo}/release-payload.txt"
git -C "${source_repo}" add release-payload.txt
GIT_AUTHOR_DATE='2025-01-02T03:04:05Z' GIT_COMMITTER_DATE='2025-01-02T03:04:05Z' \
  git -C "${source_repo}" commit -q -m 'release 1.10.0'
GIT_COMMITTER_DATE='2025-01-02T03:04:05Z' git -C "${source_repo}" tag -a v1.10.0 -m v1.10.0
release_commit="$(git -C "${source_repo}" rev-parse HEAD)"
release_epoch="$(git -C "${source_repo}" show -s --format=%ct "${release_commit}")"
GIT_COMMITTER_DATE='2025-01-02T03:04:05Z' \
  git -C "${source_repo}" tag -a v9007199254740993.0.0 -m v9007199254740993.0.0
GIT_COMMITTER_DATE='2025-01-02T03:04:05Z' \
  git -C "${source_repo}" tag -a v9007199254740992.0.0 -m v9007199254740992.0.0
git -C "${source_repo}" show-ref --dereference --tags >"${tag_refs}"

cat >"${releases_json}" <<'EOF'
[
  {"tag_name":"v1.0.0","draft":false,"prerelease":false},
  {"tag_name":"v1.10.0","draft":false,"prerelease":false},
  {"tag_name":"v2.0.0-rc.1","draft":false,"prerelease":true},
  {"tag_name":"v9.0.0","draft":true,"prerelease":false},
  {"tag_name":"not-semver","draft":false,"prerelease":false}
]
EOF

env -u GITHUB_OUTPUT -u GITHUB_ENV -u GITHUB_PATH -u GITHUB_STEP_SUMMARY \
  make -s -C "${source_repo}" VERSION=v1.10.0 GIT_COMMIT="${release_commit}" \
  SOURCE_DATE_EPOCH="${release_epoch}" release-build release-checksums
cp "${source_repo}/dist/SHA256SUMS" \
  "${source_repo}/dist/saola-linux-amd64" \
  "${source_repo}/dist/saola-linux-arm64" \
  "${release_dir}/"

resolver_env=(
  "SAOLA_CLI_RELEASES_JSON_FILE=${releases_json}"
  "SAOLA_CLI_TAG_REFS_FILE=${tag_refs}"
  "SAOLA_CLI_RELEASE_DIR=${release_dir}"
  "SAOLA_CLI_SOURCE_DIR=${source_repo}"
  "GITHUB_OUTPUT=${tmp_dir}/must-not-write-output"
  "GITHUB_ENV=${tmp_dir}/must-not-write-env"
  "GITHUB_PATH=${tmp_dir}/must-not-write-path"
  "GITHUB_STEP_SUMMARY=${tmp_dir}/must-not-write-summary"
)

run_resolver() {
  env "${resolver_env[@]}" "${resolver}" "$@"
}

run_resolver resolve-latest "${tmp_dir}/resolved.lock"
"${lock_helper}" validate "${tmp_dir}/resolved.lock"
[[ "$("${lock_helper}" get "${tmp_dir}/resolved.lock" version)" = v1.10.0 ]] || fail 'did not select the numeric maximum stable release'
[[ "$("${lock_helper}" get "${tmp_dir}/resolved.lock" channel)" = stable ]] || fail 'resolved lock is not stable'
run_resolver verify-lock "${tmp_dir}/resolved.lock"

cat >"${tmp_dir}/expected.lock" <<EOF
repository=harmonycloud/saola-cli
version=v1.10.0
commit=${release_commit}
channel=stable
source_date_epoch=${release_epoch}
EOF
cmp -s "${tmp_dir}/expected.lock" "${tmp_dir}/resolved.lock" || fail 'lock fields or order are not deterministic'

large_version='v9007199254740993.0.0'
smaller_float_equal_version='v9007199254740992.0.0'
large_releases_json="${tmp_dir}/large-releases.json"
large_release_dir="${tmp_dir}/large-release"
mkdir -p "${large_release_dir}"
cat >"${large_releases_json}" <<EOF
[
  {"tag_name":"${large_version}","draft":false,"prerelease":false},
  {"tag_name":"${smaller_float_equal_version}","draft":false,"prerelease":false}
]
EOF
env -u GITHUB_OUTPUT -u GITHUB_ENV -u GITHUB_PATH -u GITHUB_STEP_SUMMARY \
  make -s -C "${source_repo}" VERSION="${large_version}" GIT_COMMIT="${release_commit}" \
  SOURCE_DATE_EPOCH="${release_epoch}" release-build release-checksums
cp "${source_repo}/dist/SHA256SUMS" \
  "${source_repo}/dist/saola-linux-amd64" \
  "${source_repo}/dist/saola-linux-arm64" \
  "${large_release_dir}/"
resolver_env[0]="SAOLA_CLI_RELEASES_JSON_FILE=${large_releases_json}"
resolver_env[2]="SAOLA_CLI_RELEASE_DIR=${large_release_dir}"
run_resolver resolve-latest "${tmp_dir}/large-resolved.lock"
[[ "$("${lock_helper}" get "${tmp_dir}/large-resolved.lock" version)" = "${large_version}" ]] || fail 'precision loss selected a smaller stable release'
if grep -Fq 'tonumber' "${resolver}"; then
  fail 'release version ordering must not depend on jq tonumber precision'
fi
resolver_env[0]="SAOLA_CLI_RELEASES_JSON_FILE=${releases_json}"
resolver_env[2]="SAOLA_CLI_RELEASE_DIR=${release_dir}"

no_eligible_json="${tmp_dir}/no-eligible.json"
cat >"${no_eligible_json}" <<'EOF'
[
  {"tag_name":"v2.0.0-rc.1","draft":false,"prerelease":true},
  {"tag_name":"v9.0.0","draft":true,"prerelease":false},
  {"tag_name":"not-semver","draft":false,"prerelease":false}
]
EOF
resolver_env[0]="SAOLA_CLI_RELEASES_JSON_FILE=${no_eligible_json}"
assert_rejected 'Release fixture without an eligible final tag' run_resolver resolve-latest "${tmp_dir}/no-eligible.lock"
resolver_env[0]="SAOLA_CLI_RELEASES_JSON_FILE=${releases_json}"

mismatched_refs="${tmp_dir}/mismatched-tag-refs.txt"
sed "s#^${release_commit} refs/tags/v1.10.0\^{}#${old_commit} refs/tags/v1.10.0^{}#" \
  "${tag_refs}" >"${mismatched_refs}"
resolver_env[1]="SAOLA_CLI_TAG_REFS_FILE=${mismatched_refs}"
assert_rejected 'tag whose peeled commit does not match the lock' run_resolver verify-lock "${tmp_dir}/resolved.lock"
resolver_env[1]="SAOLA_CLI_TAG_REFS_FILE=${tag_refs}"

missing_sums_dir="${tmp_dir}/release-missing-sums"
cp -R "${release_dir}" "${missing_sums_dir}"
rm "${missing_sums_dir}/SHA256SUMS"
resolver_env[2]="SAOLA_CLI_RELEASE_DIR=${missing_sums_dir}"
assert_rejected 'Release without SHA256SUMS' run_resolver verify-lock "${tmp_dir}/resolved.lock"

missing_arch_dir="${tmp_dir}/release-missing-arch"
cp -R "${release_dir}" "${missing_arch_dir}"
rm "${missing_arch_dir}/saola-linux-arm64"
resolver_env[2]="SAOLA_CLI_RELEASE_DIR=${missing_arch_dir}"
assert_rejected 'Release without one architecture asset' run_resolver verify-lock "${tmp_dir}/resolved.lock"

published_mismatch_dir="${tmp_dir}/release-published-mismatch"
cp -R "${release_dir}" "${published_mismatch_dir}"
printf 'tampered\n' >>"${published_mismatch_dir}/saola-linux-amd64"
resolver_env[2]="SAOLA_CLI_RELEASE_DIR=${published_mismatch_dir}"
assert_rejected 'published asset with a checksum mismatch' run_resolver verify-lock "${tmp_dir}/resolved.lock"

rebuilt_mismatch_dir="${tmp_dir}/release-rebuilt-mismatch"
cp -R "${release_dir}" "${rebuilt_mismatch_dir}"
printf 'different but self-consistent\n' >>"${rebuilt_mismatch_dir}/saola-linux-arm64"
(
  cd "${rebuilt_mismatch_dir}"
  shasum -a 256 saola-linux-amd64 saola-linux-arm64 >SHA256SUMS
)
resolver_env[2]="SAOLA_CLI_RELEASE_DIR=${rebuilt_mismatch_dir}"
assert_rejected 'published checksums that do not match the exact rebuild' run_resolver verify-lock "${tmp_dir}/resolved.lock"
resolver_env[2]="SAOLA_CLI_RELEASE_DIR=${release_dir}"

invalid_lock="${tmp_dir}/invalid.lock"
sed 's/^channel=stable$/channel=dev/' "${tmp_dir}/resolved.lock" >"${invalid_lock}"
assert_rejected 'invalid lock' run_resolver verify-lock "${invalid_lock}"

for command_file in must-not-write-output must-not-write-env must-not-write-path must-not-write-summary; do
  [[ ! -e "${tmp_dir}/${command_file}" ]] || fail "external make wrote ${command_file}"
done

printf 'PASS: saola-cli release resolver\n'
