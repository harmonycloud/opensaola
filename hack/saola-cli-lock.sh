#!/usr/bin/env bash

set -euo pipefail

usage() {
  printf 'usage: %s validate <lock-file>\n' "$0" >&2
  printf '       %s get <lock-file> <key>\n' "$0" >&2
  exit 2
}

die() {
  printf 'saola-cli lock: %s\n' "$*" >&2
  exit 1
}

validate_stable_version() {
  local version="$1"

  [[ "${version}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
}

parse_lock() {
  local lock_file="$1"
  local line key value
  local repository='' version='' commit='' channel='' source_date_epoch=''
  local seen_repository=0 seen_version=0 seen_commit=0 seen_channel=0 seen_source_date_epoch=0

  [[ -f "${lock_file}" && -r "${lock_file}" ]] || die "cannot read ${lock_file}"

  while IFS= read -r line || [[ -n "${line}" ]]; do
    [[ -n "${line}" && "${line}" == *=* ]] || die 'each line must be a non-empty key=value pair'
    key="${line%%=*}"
    value="${line#*=}"
    [[ -n "${value}" && "${value}" != *=* ]] || die "invalid value for ${key}"

    case "${key}" in
      repository)
        ((seen_repository == 0)) || die 'duplicate key: repository'
        repository="${value}"
        seen_repository=1
        ;;
      version)
        ((seen_version == 0)) || die 'duplicate key: version'
        version="${value}"
        seen_version=1
        ;;
      commit)
        ((seen_commit == 0)) || die 'duplicate key: commit'
        commit="${value}"
        seen_commit=1
        ;;
      channel)
        ((seen_channel == 0)) || die 'duplicate key: channel'
        channel="${value}"
        seen_channel=1
        ;;
      source_date_epoch)
        ((seen_source_date_epoch == 0)) || die 'duplicate key: source_date_epoch'
        source_date_epoch="${value}"
        seen_source_date_epoch=1
        ;;
      *)
        die "unknown key: ${key}"
        ;;
    esac
  done <"${lock_file}"

  ((seen_repository == 1)) || die 'missing key: repository'
  ((seen_version == 1)) || die 'missing key: version'
  ((seen_commit == 1)) || die 'missing key: commit'
  ((seen_channel == 1)) || die 'missing key: channel'
  ((seen_source_date_epoch == 1)) || die 'missing key: source_date_epoch'

  [[ "${repository}" == 'harmonycloud/saola-cli' ]] || die 'repository must be harmonycloud/saola-cli'
  [[ "${commit}" =~ ^[0-9a-f]{40}$ ]] || die 'commit must be a full lowercase 40-character SHA'
  [[ "${source_date_epoch}" =~ ^(0|[1-9][0-9]*)$ ]] || die 'source_date_epoch must be a non-negative integer'

  [[ "${channel}" = stable ]] || die 'channel must be stable'
  validate_stable_version "${version}" || die 'stable version must be a final vMAJOR.MINOR.PATCH release tag'

  LOCK_REPOSITORY="${repository}"
  LOCK_VERSION="${version}"
  LOCK_COMMIT="${commit}"
  LOCK_CHANNEL="${channel}"
  LOCK_SOURCE_DATE_EPOCH="${source_date_epoch}"
}

(( $# >= 2 )) || usage
command="$1"
lock_file="$2"

case "${command}" in
  validate)
    (( $# == 2 )) || usage
    parse_lock "${lock_file}"
    ;;
  get)
    (( $# == 3 )) || usage
    key="$3"
    parse_lock "${lock_file}"
    case "${key}" in
      repository) printf '%s\n' "${LOCK_REPOSITORY}" ;;
      version) printf '%s\n' "${LOCK_VERSION}" ;;
      commit) printf '%s\n' "${LOCK_COMMIT}" ;;
      channel) printf '%s\n' "${LOCK_CHANNEL}" ;;
      source_date_epoch) printf '%s\n' "${LOCK_SOURCE_DATE_EPOCH}" ;;
      *) die "unknown key: ${key}" ;;
    esac
    ;;
  *)
    usage
    ;;
esac
