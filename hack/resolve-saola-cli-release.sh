#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
lock_helper="${script_dir}/saola-cli-lock.sh"
repository='harmonycloud/saola-cli'
tmp_root=''

usage() {
  printf 'usage: %s resolve-latest <output-lock>\n' "$0" >&2
  printf '       %s verify-lock <lock-file>\n' "$0" >&2
  exit 2
}

die() {
  printf 'saola-cli release resolver: %s\n' "$*" >&2
  exit 1
}

cleanup() {
  [[ -z "${tmp_root}" ]] || rm -rf "${tmp_root}"
}

ensure_tmp_root() {
  if [[ -z "${tmp_root}" ]]; then
    tmp_root="$(mktemp -d "${TMPDIR:-/tmp}/saola-cli-release.XXXXXX")"
    trap cleanup EXIT
  fi
}

sha256_file() {
  local path="$1"

  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${path}" | awk '{print $1}'
  else
    shasum -a 256 "${path}" | awk '{print $1}'
  fi
}

load_releases() {
  local output_file="$1"
  local raw_file

  ensure_tmp_root
  raw_file="${tmp_root}/releases-raw.$RANDOM.json"
  if [[ -n "${SAOLA_CLI_RELEASES_JSON_FILE:-}" ]]; then
    [[ -f "${SAOLA_CLI_RELEASES_JSON_FILE}" && -r "${SAOLA_CLI_RELEASES_JSON_FILE}" ]] || die 'cannot read Release JSON fixture'
    cp "${SAOLA_CLI_RELEASES_JSON_FILE}" "${raw_file}"
  else
    gh api --paginate --slurp "repos/${repository}/releases?per_page=100" >"${raw_file}" || die 'failed to query all GitHub Releases pages'
  fi

  jq -e '
    if type != "array" then error("Release response must be an array")
    elif length == 0 then .
    elif (.[0] | type) == "array" then add
    else .
    end
  ' "${raw_file}" >"${output_file}" || die 'invalid GitHub Releases response'
}

latest_stable_version() {
  local releases_file="$1"

  jq -er '
    [
      .[]
      | select(.draft == false and .prerelease == false)
      | select(.tag_name | type == "string")
      | .tag_name as $tag
      | ($tag | capture("^v(?<major>0|[1-9][0-9]*)\\.(?<minor>0|[1-9][0-9]*)\\.(?<patch>0|[1-9][0-9]*)$")) as $tuple
      | {
          tag: $tag,
          major: $tuple.major,
          minor: $tuple.minor,
          patch: $tuple.patch
        }
    ]
    | if length == 0 then error("no eligible final Release")
      else sort_by(
        (.major | length), .major,
        (.minor | length), .minor,
        (.patch | length), .patch
      )[-1].tag
      end
  ' "${releases_file}" || die 'no eligible final Release'
}

require_published_release() {
  local releases_file="$1"
  local version="$2"

  jq -e --arg version "${version}" '
    any(.[]; .tag_name == $version and .draft == false and .prerelease == false)
  ' "${releases_file}" >/dev/null || die "${version} is not a matching published final Release"
}

load_tag_refs() {
  local output_file="$1"

  if [[ -n "${SAOLA_CLI_TAG_REFS_FILE:-}" ]]; then
    [[ -f "${SAOLA_CLI_TAG_REFS_FILE}" && -r "${SAOLA_CLI_TAG_REFS_FILE}" ]] || die 'cannot read tag refs fixture'
    cp "${SAOLA_CLI_TAG_REFS_FILE}" "${output_file}"
  else
    git ls-remote "https://github.com/${repository}.git" 'refs/tags/*' >"${output_file}" || die 'failed to query remote tag refs'
  fi
}

peeled_tag_commit() {
  local refs_file="$1"
  local version="$2"
  local direct peeled

  direct="$(awk -v ref="refs/tags/${version}" '$2 == ref { print $1 }' "${refs_file}")"
  peeled="$(awk -v ref="refs/tags/${version}^{}" '$2 == ref { print $1 }' "${refs_file}")"
  [[ "$(wc -w <<<"${direct}")" -eq 1 ]] || die "missing or duplicate tag ref for ${version}"
  if [[ -n "${peeled}" ]]; then
    [[ "$(wc -w <<<"${peeled}")" -eq 1 ]] || die "duplicate peeled tag ref for ${version}"
    printf '%s\n' "${peeled}"
  else
    printf '%s\n' "${direct}"
  fi
}

source_repository() {
  local version="$1"
  local output_dir="$2"

  if [[ -n "${SAOLA_CLI_SOURCE_DIR:-}" ]]; then
    [[ -d "${SAOLA_CLI_SOURCE_DIR}" ]] || die 'source fixture directory does not exist'
    git -C "${SAOLA_CLI_SOURCE_DIR}" rev-parse --git-dir >/dev/null 2>&1 || die 'source fixture is not a Git repository'
    printf '%s\n' "${SAOLA_CLI_SOURCE_DIR}"
    return
  fi

  git -C "${output_dir}" init -q
  git -C "${output_dir}" remote add origin "https://github.com/${repository}.git"
  git -C "${output_dir}" fetch -q --depth=1 origin "refs/tags/${version}:refs/tags/${version}" || die "failed to fetch ${version}"
  printf '%s\n' "${output_dir}"
}

validate_source_identity() {
  local source_dir="$1"
  local version="$2"
  local commit="$3"
  local source_tag_commit

  [[ "${commit}" =~ ^[0-9a-f]{40}$ ]] || die 'tag did not resolve to a full lowercase commit SHA'
  git -C "${source_dir}" cat-file -e "${commit}^{commit}" 2>/dev/null || die 'resolved commit is unavailable in the source repository'
  source_tag_commit="$(git -C "${source_dir}" rev-parse "refs/tags/${version}^{}" 2>/dev/null)" || die "source repository is missing tag ${version}"
  [[ "${source_tag_commit}" == "${commit}" ]] || die "tag ${version} does not peel to the locked commit"
}

commit_epoch() {
  local source_dir="$1"
  local commit="$2"
  local epoch

  epoch="$(git -C "${source_dir}" show -s --format=%ct "${commit}^{commit}")"
  [[ "${epoch}" =~ ^(0|[1-9][0-9]*)$ ]] || die 'commit timestamp is not a non-negative epoch'
  printf '%s\n' "${epoch}"
}

download_release_assets() {
  local version="$1"
  local output_dir="$2"
  local asset

  mkdir -p "${output_dir}"
  if [[ -n "${SAOLA_CLI_RELEASE_DIR:-}" ]]; then
    for asset in SHA256SUMS saola-linux-amd64 saola-linux-arm64; do
      [[ -f "${SAOLA_CLI_RELEASE_DIR}/${asset}" && -r "${SAOLA_CLI_RELEASE_DIR}/${asset}" ]] || die "missing Release asset: ${asset}"
      cp "${SAOLA_CLI_RELEASE_DIR}/${asset}" "${output_dir}/${asset}"
    done
  else
    gh release download "${version}" --repo "${repository}" \
      --pattern SHA256SUMS \
      --pattern saola-linux-amd64 \
      --pattern saola-linux-arm64 \
      --dir "${output_dir}" || die "failed to download Release assets for ${version}"
  fi

  for asset in SHA256SUMS saola-linux-amd64 saola-linux-arm64; do
    [[ -f "${output_dir}/${asset}" && -r "${output_dir}/${asset}" ]] || die "missing Release asset: ${asset}"
  done
}

read_checksums() {
  local sums_file="$1"
  local line hash filename extra
  local amd64='' arm64=''
  local seen_amd64=0 seen_arm64=0

  while IFS= read -r line || [[ -n "${line}" ]]; do
    read -r hash filename extra <<<"${line}"
    filename="${filename#\*}"
    [[ -z "${extra:-}" && "${hash}" =~ ^[0-9a-f]{64}$ ]] || die 'SHA256SUMS contains an invalid entry'
    case "${filename}" in
      saola-linux-amd64)
        ((seen_amd64 == 0)) || die 'SHA256SUMS contains duplicate amd64 entries'
        amd64="${hash}"
        seen_amd64=1
        ;;
      saola-linux-arm64)
        ((seen_arm64 == 0)) || die 'SHA256SUMS contains duplicate arm64 entries'
        arm64="${hash}"
        seen_arm64=1
        ;;
      *)
        die "SHA256SUMS contains an unexpected asset: ${filename}"
        ;;
    esac
  done <"${sums_file}"

  ((seen_amd64 == 1 && seen_arm64 == 1)) || die 'SHA256SUMS must contain both Linux architecture assets'
  CHECKSUM_AMD64="${amd64}"
  CHECKSUM_ARM64="${arm64}"
}

verify_asset_checksums() {
  local release_dir="$1"
  local actual_amd64 actual_arm64

  read_checksums "${release_dir}/SHA256SUMS"
  actual_amd64="$(sha256_file "${release_dir}/saola-linux-amd64")"
  actual_arm64="$(sha256_file "${release_dir}/saola-linux-arm64")"
  [[ "${actual_amd64}" == "${CHECKSUM_AMD64}" ]] || die 'published amd64 checksum mismatch'
  [[ "${actual_arm64}" == "${CHECKSUM_ARM64}" ]] || die 'published arm64 checksum mismatch'
}

verify_rebuild() {
  local source_dir="$1"
  local version="$2"
  local commit="$3"
  local source_date_epoch="$4"
  local expected_amd64="$5"
  local expected_arm64="$6"
  local build_dir actual_amd64 actual_arm64

  ensure_tmp_root
  build_dir="${tmp_root}/source-build.$RANDOM"
  mkdir -p "${build_dir}"
  git -C "${source_dir}" archive "${commit}^{commit}" | tar -x -C "${build_dir}" || die 'failed to export exact source commit'
  env -u GITHUB_OUTPUT -u GITHUB_ENV -u GITHUB_PATH -u GITHUB_STEP_SUMMARY \
    make -C "${build_dir}" \
      VERSION="${version}" \
      GIT_COMMIT="${commit}" \
      SOURCE_DATE_EPOCH="${source_date_epoch}" \
      release-build release-checksums >/dev/null || die 'exact-commit Release rebuild failed'

  for asset in SHA256SUMS saola-linux-amd64 saola-linux-arm64; do
    [[ -f "${build_dir}/dist/${asset}" ]] || die "rebuild did not produce ${asset}"
  done
  verify_asset_checksums "${build_dir}/dist"
  actual_amd64="${CHECKSUM_AMD64}"
  actual_arm64="${CHECKSUM_ARM64}"
  [[ "${actual_amd64}" == "${expected_amd64}" ]] || die 'rebuilt amd64 checksum mismatch'
  [[ "${actual_arm64}" == "${expected_arm64}" ]] || die 'rebuilt arm64 checksum mismatch'
}

verify_lock() {
  local lock_file="$1"
  local version commit source_date_epoch
  local releases_file refs_file source_clone source_dir release_dir
  local remote_commit actual_epoch expected_amd64 expected_arm64

  "${lock_helper}" validate "${lock_file}" || die 'invalid lock'
  version="$("${lock_helper}" get "${lock_file}" version)"
  commit="$("${lock_helper}" get "${lock_file}" commit)"
  source_date_epoch="$("${lock_helper}" get "${lock_file}" source_date_epoch)"

  ensure_tmp_root
  releases_file="${tmp_root}/releases.$RANDOM.json"
  refs_file="${tmp_root}/refs.$RANDOM.txt"
  source_clone="${tmp_root}/source.$RANDOM"
  release_dir="${tmp_root}/assets.$RANDOM"
  load_releases "${releases_file}"
  require_published_release "${releases_file}" "${version}"
  load_tag_refs "${refs_file}"
  remote_commit="$(peeled_tag_commit "${refs_file}" "${version}")"
  [[ "${remote_commit}" == "${commit}" ]] || die "tag ${version} does not match the locked commit"

  source_dir="$(source_repository "${version}" "${source_clone}")"
  validate_source_identity "${source_dir}" "${version}" "${commit}"
  actual_epoch="$(commit_epoch "${source_dir}" "${commit}")"
  [[ "${actual_epoch}" == "${source_date_epoch}" ]] || die 'source_date_epoch does not match the commit timestamp'

  download_release_assets "${version}" "${release_dir}"
  verify_asset_checksums "${release_dir}"
  expected_amd64="${CHECKSUM_AMD64}"
  expected_arm64="${CHECKSUM_ARM64}"
  verify_rebuild "${source_dir}" "${version}" "${commit}" "${source_date_epoch}" "${expected_amd64}" "${expected_arm64}"
}

resolve_latest() {
  local output_lock="$1"
  local releases_file refs_file source_clone source_dir
  local version commit source_date_epoch candidate

  ensure_tmp_root
  releases_file="${tmp_root}/resolve-releases.json"
  refs_file="${tmp_root}/resolve-refs.txt"
  source_clone="${tmp_root}/resolve-source"
  load_releases "${releases_file}"
  version="$(latest_stable_version "${releases_file}")"
  load_tag_refs "${refs_file}"
  commit="$(peeled_tag_commit "${refs_file}" "${version}")"
  source_dir="$(source_repository "${version}" "${source_clone}")"
  validate_source_identity "${source_dir}" "${version}" "${commit}"
  source_date_epoch="$(commit_epoch "${source_dir}" "${commit}")"

  candidate="$(mktemp "${output_lock}.tmp.XXXXXX")" || die 'cannot create atomic lock candidate'
  trap 'rm -f "${candidate}"; cleanup' EXIT
  printf '%s\n' \
    "repository=${repository}" \
    "version=${version}" \
    "commit=${commit}" \
    'channel=stable' \
    "source_date_epoch=${source_date_epoch}" \
    >"${candidate}"
  verify_lock "${candidate}"
  mv -f "${candidate}" "${output_lock}"
  trap cleanup EXIT
}

case "${1:-}" in
  resolve-latest)
    [[ "$#" -eq 2 ]] || usage
    resolve_latest "$2"
    ;;
  verify-lock)
    [[ "$#" -eq 2 ]] || usage
    verify_lock "$2"
    ;;
  *) usage ;;
esac
