#!/usr/bin/env bash

set -euo pipefail

repo_root="$(git -C "$(dirname "${BASH_SOURCE[0]}")/.." rev-parse --show-toplevel)"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/opensaola-manager-version.XXXXXX")"
trap 'rm -rf "${tmp_dir}"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_equal() {
  local want="$1"
  local got="$2"
  local message="$3"

  [[ "${got}" == "${want}" ]] || fail "${message}: got '${got}', want '${want}'"
}

make_value() {
  local working_dir="$1"
  local variable="$2"
  shift 2

  printf 'include %s\n.PHONY: print-manager-version-contract\nprint-manager-version-contract:\n\t@printf "%%s\\n" "$(%s)"\n' \
    "${repo_root}/Makefile" "${variable}" | \
    make -s -C "${working_dir}" -f - "$@" print-manager-version-contract
}

git_repo="${tmp_dir}/git-repo"
mkdir -p "${git_repo}"
git -C "${git_repo}" init -q --initial-branch=feature/manager-metadata
git -C "${git_repo}" config user.name 'OpenSaola Test'
git -C "${git_repo}" config user.email 'opensaola-test@example.invalid'
printf '%s\n' fixture >"${git_repo}/fixture.txt"
git -C "${git_repo}" add fixture.txt
GIT_AUTHOR_DATE='2025-01-02T03:04:05Z' \
  GIT_COMMITTER_DATE='2025-01-02T03:04:05Z' \
  git -C "${git_repo}" commit -q -m fixture

commit="$(git -C "${git_repo}" rev-parse HEAD)"
commit_date="$(git -C "${git_repo}" show -s --format=%cI HEAD)"

assert_equal 'feature/manager-metadata' "$(make_value "${git_repo}" MANAGER_VERSION)" \
  'branch build did not use the branch name'
assert_equal "${commit}" "$(make_value "${git_repo}" MANAGER_GIT_COMMIT)" \
  'branch build did not use the full commit'
assert_equal "${commit_date}" "$(make_value "${git_repo}" MANAGER_BUILD_DATE)" \
  'branch build did not use the commit timestamp'

git -C "${git_repo}" tag v1.2.3
assert_equal 'v1.2.3' "$(make_value "${git_repo}" MANAGER_VERSION)" \
  'tag build did not use the exact release tag'

git -C "${git_repo}" tag -d v1.2.3 >/dev/null
git -C "${git_repo}" tag v1garbage
assert_equal 'feature/manager-metadata' "$(make_value "${git_repo}" MANAGER_VERSION)" \
  'invalid release tag was not treated as a branch build'
git -C "${git_repo}" tag -d v1garbage >/dev/null
git -C "${git_repo}" checkout -q --detach
assert_equal "sha-${commit:0:12}" "$(make_value "${git_repo}" MANAGER_VERSION)" \
  'detached build did not use the short commit identity'

no_git_dir="${tmp_dir}/no-git"
mkdir -p "${no_git_dir}"
assert_equal 'dev' "$(make_value "${no_git_dir}" MANAGER_VERSION)" \
  'non-Git build did not fall back to dev'
assert_equal 'unknown' "$(make_value "${no_git_dir}" MANAGER_GIT_COMMIT)" \
  'non-Git build did not fall back to an unknown commit'
assert_equal 'unknown' "$(make_value "${no_git_dir}" MANAGER_BUILD_DATE)" \
  'non-Git build did not fall back to an unknown build date'

override_commit='0123456789abcdef0123456789abcdef01234567'
override_date='2025-06-07T08:09:10Z'
assert_equal 'v9.8.7' "$(make_value "${no_git_dir}" MANAGER_VERSION MANAGER_VERSION=v9.8.7)" \
  'MANAGER_VERSION is not overridable'
assert_equal "${override_commit}" \
  "$(make_value "${no_git_dir}" MANAGER_GIT_COMMIT MANAGER_GIT_COMMIT="${override_commit}")" \
  'MANAGER_GIT_COMMIT is not overridable'
assert_equal "${override_date}" \
  "$(make_value "${no_git_dir}" MANAGER_BUILD_DATE MANAGER_BUILD_DATE="${override_date}")" \
  'MANAGER_BUILD_DATE is not overridable'

ldflags="$(make_value "${no_git_dir}" MANAGER_LDFLAGS \
  MANAGER_VERSION=v9.8.7 MANAGER_GIT_COMMIT="${override_commit}" MANAGER_BUILD_DATE="${override_date}")"
[[ "${ldflags}" == *'-X github.com/harmonycloud/opensaola/internal/version.Version=v9.8.7'* ]] || \
  fail 'MANAGER_LDFLAGS is missing Version'
[[ "${ldflags}" == *"-X github.com/harmonycloud/opensaola/internal/version.GitCommit=${override_commit}"* ]] || \
  fail 'MANAGER_LDFLAGS is missing GitCommit'
[[ "${ldflags}" == *"-X github.com/harmonycloud/opensaola/internal/version.BuildDate=${override_date}"* ]] || \
  fail 'MANAGER_LDFLAGS is missing BuildDate'

build_args="$(make_value "${no_git_dir}" MANAGER_BUILD_ARGS \
  MANAGER_VERSION=v9.8.7 MANAGER_GIT_COMMIT="${override_commit}" MANAGER_BUILD_DATE="${override_date}")"
[[ "${build_args}" == *'--build-arg VERSION=v9.8.7'* ]] || fail 'MANAGER_BUILD_ARGS is missing VERSION'
[[ "${build_args}" == *"--build-arg GIT_COMMIT=${override_commit}"* ]] || fail 'MANAGER_BUILD_ARGS is missing GIT_COMMIT'
[[ "${build_args}" == *"--build-arg BUILD_DATE=${override_date}"* ]] || fail 'MANAGER_BUILD_ARGS is missing BUILD_DATE'

build_dry_run="$(make -n -C "${repo_root}" build \
  MANAGER_VERSION=v9.8.7 MANAGER_GIT_COMMIT="${override_commit}" MANAGER_BUILD_DATE="${override_date}")"
[[ "${build_dry_run}" == *'go build'*'-ldflags'* ]] || fail 'make build does not pass manager linker flags'
[[ "${build_dry_run}" == *'internal/version.Version=v9.8.7'* ]] || fail 'make build does not inject Version'
[[ "${build_dry_run}" == *"internal/version.GitCommit=${override_commit}"* ]] || fail 'make build does not inject GitCommit'
[[ "${build_dry_run}" == *"internal/version.BuildDate=${override_date}"* ]] || fail 'make build does not inject BuildDate'

grep -Fq -- '$(MANAGER_BUILD_ARGS)' "${repo_root}/Makefile" || fail 'Docker targets do not use MANAGER_BUILD_ARGS'
[[ "$(grep -Fc -- '$(MANAGER_BUILD_ARGS)' "${repo_root}/Makefile")" -ge 2 ]] || \
  fail 'docker-build and docker-buildx do not both use MANAGER_BUILD_ARGS'

dockerfile="${repo_root}/Dockerfile"
grep -Fq 'ARG VERSION=dev' "${dockerfile}" || fail 'Dockerfile is missing the VERSION fallback'
grep -Fq 'ARG GIT_COMMIT=unknown' "${dockerfile}" || fail 'Dockerfile is missing the GIT_COMMIT fallback'
grep -Fq 'ARG BUILD_DATE=unknown' "${dockerfile}" || fail 'Dockerfile is missing the BUILD_DATE fallback'
grep -Fq -- '-X github.com/harmonycloud/opensaola/internal/version.Version=${VERSION}' "${dockerfile}" || \
  fail 'Dockerfile does not inject Version through the full package path'
grep -Fq -- '-X github.com/harmonycloud/opensaola/internal/version.GitCommit=${GIT_COMMIT}' "${dockerfile}" || \
  fail 'Dockerfile does not inject GitCommit through the full package path'
grep -Fq -- '-X github.com/harmonycloud/opensaola/internal/version.BuildDate=${BUILD_DATE}' "${dockerfile}" || \
  fail 'Dockerfile does not inject BuildDate through the full package path'
if grep -Eq -- '-X[[:space:]]+main\.(version|gitCommit|buildDate)' "${dockerfile}"; then
  fail 'Dockerfile still injects nonexistent main package variables'
fi
grep -Fq 'org.opencontainers.image.version="${VERSION}"' "${dockerfile}" || \
  fail 'OCI version label does not use VERSION'
grep -Fq 'org.opencontainers.image.revision="${GIT_COMMIT}"' "${dockerfile}" || \
  fail 'OCI revision label does not use GIT_COMMIT'
grep -Fq 'org.opencontainers.image.created="${BUILD_DATE}"' "${dockerfile}" || \
  fail 'OCI created label does not use BUILD_DATE'

echo 'manager version build contract tests passed'
