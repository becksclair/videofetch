#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SOURCE_DIR="${ROOT_DIR}/dist/firefox"
ARTIFACTS_DIR="${FIREFOX_SIGN_ARTIFACTS_DIR:-${ROOT_DIR}/dist/artifacts}"
SECRETS_FILE="${VIDEOFETCH_SECRETS_FILE:-${HOME}/personal/dotfiles/secrets.sh}"
CHANNEL="${FIREFOX_SIGN_CHANNEL:-unlisted}"

load_secrets_if_needed() {
  if [[ -n "${AMO_JWT_ISSUER:-${WEB_EXT_API_KEY:-}}" && -n "${AMO_JWT_SECRET:-${WEB_EXT_API_SECRET:-}}" ]]; then
    return
  fi
  if [[ -f "${SECRETS_FILE}" ]]; then
    set +u
    # shellcheck source=/dev/null
    . "${SECRETS_FILE}"
    set -u
  fi
}

resolve_api_key() {
  if [[ -n "${AMO_JWT_ISSUER:-}" ]]; then
    printf '%s' "${AMO_JWT_ISSUER}"
    return
  fi
  if [[ -n "${WEB_EXT_API_KEY:-}" ]]; then
    printf '%s' "${WEB_EXT_API_KEY}"
    return
  fi
}

resolve_api_secret() {
  if [[ -n "${AMO_JWT_SECRET:-}" ]]; then
    printf '%s' "${AMO_JWT_SECRET}"
    return
  fi
  if [[ -n "${WEB_EXT_API_SECRET:-}" ]]; then
    printf '%s' "${WEB_EXT_API_SECRET}"
    return
  fi
}

load_secrets_if_needed

API_KEY="$(resolve_api_key || true)"
API_SECRET="$(resolve_api_secret || true)"

if [[ -z "${API_KEY}" || -z "${API_SECRET}" ]]; then
  cat >&2 <<EOF
Missing AMO signing credentials.

Set AMO_JWT_ISSUER and AMO_JWT_SECRET, or WEB_EXT_API_KEY and WEB_EXT_API_SECRET.
The script also tried to source: ${SECRETS_FILE}
EOF
  exit 1
fi

cd "${ROOT_DIR}"
mkdir -p "${ARTIFACTS_DIR}"

bun run build:firefox
"${ROOT_DIR}/node_modules/.bin/addons-linter" --enable-data-collection-permissions --disable-linter-rules=no-unsanitized/property "${SOURCE_DIR}"
"${ROOT_DIR}/node_modules/.bin/web-ext" sign \
  --no-input \
  --no-config-discovery \
  --source-dir "${SOURCE_DIR}" \
  --artifacts-dir "${ARTIFACTS_DIR}" \
  --channel "${CHANNEL}" \
  --api-key "${API_KEY}" \
  --api-secret "${API_SECRET}"
