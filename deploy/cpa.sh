#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
COMPOSE_FILE="${SCRIPT_DIR}/compose.prod.yml"
SETTINGS_FILE="${CPA_DEPLOY_ENV_FILE:-${SCRIPT_DIR}/.env}"
STATE_FILE="${SCRIPT_DIR}/.state.env"

usage() {
  cat <<'EOF'
Usage: bash ./deploy/cpa.sh <command>

Commands:
  init      Create persistent directories and local deployment settings
  deploy    Build and deploy the currently checked-out commit
  upgrade   Fast-forward the configured origin branch, then deploy it
  rollback  Switch back to the previously deployed image
  status    Show the selected image and Docker Compose status
  logs      Follow container logs
  images    List locally built CPA images
EOF
}

fail() {
  echo "Error: $*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command '$1' was not found"
}

env_value() {
  local key="$1"
  local file="$2"
  local value

  [[ -f "${file}" ]] || return 1
  value="$(sed -n "s/^${key}=//p" "${file}" | tail -n 1)"
  [[ -n "${value}" ]] || return 1
  value="${value%$'\r'}"
  value="${value#\"}"
  value="${value%\"}"
  value="${value#\'}"
  value="${value%\'}"
  printf '%s' "${value}"
}

setting() {
  local key="$1"
  local default_value="$2"
  local value

  value="$(env_value "${key}" "${SETTINGS_FILE}" 2>/dev/null || true)"
  printf '%s' "${value:-${default_value}}"
}

state_value() {
  env_value "$1" "${STATE_FILE}" 2>/dev/null || true
}

compose() {
  local args=(docker compose --project-directory "${ROOT_DIR}")

  if [[ -f "${SETTINGS_FILE}" ]]; then
    args+=(--env-file "${SETTINGS_FILE}")
  fi
  if [[ -f "${STATE_FILE}" ]]; then
    args+=(--env-file "${STATE_FILE}")
  fi
  args+=(-f "${COMPOSE_FILE}")
  "${args[@]}" "$@"
}

write_state() {
  local current_image="$1"
  local current_commit="$2"
  local previous_image="$3"
  local previous_commit="$4"
  local temporary_file

  temporary_file="$(mktemp "${SCRIPT_DIR}/.state.env.XXXXXX")"
  {
    printf 'CLI_PROXY_IMAGE=%s\n' "${current_image}"
    printf 'CPA_CURRENT_COMMIT=%s\n' "${current_commit}"
    printf 'CPA_PREVIOUS_IMAGE=%s\n' "${previous_image}"
    printf 'CPA_PREVIOUS_COMMIT=%s\n' "${previous_commit}"
  } >"${temporary_file}"
  mv -f "${temporary_file}" "${STATE_FILE}"
}

ensure_repository() {
  git -C "${ROOT_DIR}" rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
    fail "${ROOT_DIR} is not a Git repository"
}

ensure_clean_checkout() {
  local changes

  changes="$(git -C "${ROOT_DIR}" status --porcelain --untracked-files=normal)"
  [[ -z "${changes}" ]] || fail "the Git checkout is dirty; commit or stash changes before deployment"
}

ensure_initialized() {
  [[ -f "${ROOT_DIR}/config.yaml" ]] ||
    fail "config.yaml is missing; run 'bash ./deploy/cpa.sh init' and edit it first"
  mkdir -p "${ROOT_DIR}/auths" "${ROOT_DIR}/logs" "${ROOT_DIR}/plugins"
}

validate_health_settings() {
  local retries="$1"
  local interval="$2"

  [[ "${retries}" =~ ^[1-9][0-9]*$ ]] || fail "CPA_HEALTH_RETRIES must be a positive integer"
  [[ "${interval}" =~ ^[1-9][0-9]*$ ]] || fail "CPA_HEALTH_INTERVAL must be a positive integer"
}

wait_for_health() {
  local health_url
  local retries
  local interval
  local attempt

  health_url="$(setting CPA_HEALTH_URL http://127.0.0.1:8317/healthz)"
  retries="$(setting CPA_HEALTH_RETRIES 30)"
  interval="$(setting CPA_HEALTH_INTERVAL 2)"
  validate_health_settings "${retries}" "${interval}"

  for ((attempt = 1; attempt <= retries; attempt++)); do
    if curl --fail --silent --show-error --max-time 3 "${health_url}" >/dev/null 2>&1; then
      echo "Health check passed: ${health_url}"
      return 0
    fi
    sleep "${interval}"
  done

  echo "Health check failed after ${retries} attempts: ${health_url}" >&2
  return 1
}

show_config_changes() {
  local old_commit="$1"
  local new_commit="$2"

  [[ -n "${old_commit}" ]] || return 0
  git -C "${ROOT_DIR}" cat-file -e "${old_commit}^{commit}" 2>/dev/null || return 0

  echo "Configuration-related upstream changes:"
  git -C "${ROOT_DIR}" diff --stat "${old_commit}..${new_commit}" -- \
    config.example.yaml docker-compose.yml Dockerfile || true
}

deploy_current_commit() {
  local full_commit
  local short_commit
  local image_repository
  local image
  local version
  local build_date
  local old_image
  local old_commit
  local state_backup
  local had_state=false

  require_command docker
  require_command git
  require_command curl
  ensure_repository
  ensure_clean_checkout
  ensure_initialized
  docker compose version >/dev/null

  full_commit="$(git -C "${ROOT_DIR}" rev-parse HEAD)"
  short_commit="$(git -C "${ROOT_DIR}" rev-parse --short=12 HEAD)"
  image_repository="$(setting CLI_PROXY_IMAGE_REPOSITORY cli-proxy-api)"
  image="${image_repository}:custom-${short_commit}"
  version="custom-${short_commit}"
  build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  old_image="$(state_value CLI_PROXY_IMAGE)"
  old_commit="$(state_value CPA_CURRENT_COMMIT)"

  if [[ "${old_commit}" == "${full_commit}" ]] &&
    [[ -n "${old_image}" ]] &&
    docker image inspect "${old_image}" >/dev/null 2>&1; then
    echo "Commit ${full_commit} is already deployed as ${old_image}."
    compose up -d --remove-orphans --pull never
    wait_for_health
    return 0
  fi

  echo "Building ${image} from commit ${full_commit}"
  docker build --pull \
    --label "org.opencontainers.image.revision=${full_commit}" \
    --build-arg "VERSION=${version}" \
    --build-arg "COMMIT=${short_commit}" \
    --build-arg "BUILD_DATE=${build_date}" \
    --tag "${image}" \
    "${ROOT_DIR}"

  state_backup="$(mktemp)"
  if [[ -f "${STATE_FILE}" ]]; then
    cp "${STATE_FILE}" "${state_backup}"
    had_state=true
  fi

  write_state "${image}" "${full_commit}" "${old_image}" "${old_commit}"
  if ! compose up -d --remove-orphans --pull never; then
    echo "Container startup failed; restoring the previous deployment state." >&2
    if [[ "${had_state}" == true ]]; then
      mv -f "${state_backup}" "${STATE_FILE}"
      compose up -d --remove-orphans --pull never || true
      wait_for_health || true
    else
      compose down || true
      rm -f "${state_backup}" "${STATE_FILE}"
    fi
    return 1
  fi

  if wait_for_health; then
    rm -f "${state_backup}"
    show_config_changes "${old_commit}" "${full_commit}"
    echo "Deployed ${image}"
    return 0
  fi

  compose logs --tail=100 >&2 || true
  echo "Deployment failed; restoring the previous deployment state." >&2
  if [[ "${had_state}" == true ]]; then
    mv -f "${state_backup}" "${STATE_FILE}"
    compose up -d --remove-orphans --pull never || true
    wait_for_health || true
  else
    compose down || true
    rm -f "${state_backup}" "${STATE_FILE}"
  fi
  return 1
}

initialize() {
  mkdir -p "${ROOT_DIR}/auths" "${ROOT_DIR}/logs" "${ROOT_DIR}/plugins"

  if [[ ! -f "${ROOT_DIR}/config.yaml" ]]; then
    cp "${ROOT_DIR}/config.example.yaml" "${ROOT_DIR}/config.yaml"
    chmod 600 "${ROOT_DIR}/config.yaml"
    echo "Created ${ROOT_DIR}/config.yaml"
  fi

  if [[ ! -f "${SETTINGS_FILE}" ]]; then
    cp "${SCRIPT_DIR}/.env.example" "${SETTINGS_FILE}"
    echo "Created ${SETTINGS_FILE}"
  fi

  echo "Edit config.yaml and ${SETTINGS_FILE} before the first deployment."
}

upgrade() {
  local branch
  local current_branch

  require_command git
  ensure_repository
  ensure_clean_checkout
  branch="$(setting CPA_DEPLOY_BRANCH custom/main)"
  current_branch="$(git -C "${ROOT_DIR}" branch --show-current)"
  [[ "${current_branch}" == "${branch}" ]] ||
    fail "server checkout is '${current_branch}', but CPA_DEPLOY_BRANCH is '${branch}'"

  git -C "${ROOT_DIR}" fetch origin --prune
  git -C "${ROOT_DIR}" merge --ff-only "origin/${branch}"
  deploy_current_commit
}

rollback() {
  local current_image
  local current_commit
  local previous_image
  local previous_commit
  local state_backup

  require_command docker
  require_command curl
  ensure_initialized
  current_image="$(state_value CLI_PROXY_IMAGE)"
  current_commit="$(state_value CPA_CURRENT_COMMIT)"
  previous_image="$(state_value CPA_PREVIOUS_IMAGE)"
  previous_commit="$(state_value CPA_PREVIOUS_COMMIT)"
  [[ -n "${previous_image}" ]] || fail "no previous deployment is recorded"
  docker image inspect "${previous_image}" >/dev/null 2>&1 ||
    fail "previous image '${previous_image}' is not available locally"

  state_backup="$(mktemp)"
  cp "${STATE_FILE}" "${state_backup}"
  write_state "${previous_image}" "${previous_commit}" "${current_image}" "${current_commit}"
  if ! compose up -d --remove-orphans --pull never; then
    echo "Rollback startup failed; restoring ${current_image}." >&2
    mv -f "${state_backup}" "${STATE_FILE}"
    compose up -d --remove-orphans --pull never || true
    wait_for_health || true
    return 1
  fi

  if wait_for_health; then
    rm -f "${state_backup}"
    echo "Rolled back to ${previous_image}"
    return 0
  fi

  echo "Rollback health check failed; restoring ${current_image}." >&2
  mv -f "${state_backup}" "${STATE_FILE}"
  compose up -d --remove-orphans --pull never || true
  wait_for_health || true
  return 1
}

show_status() {
  echo "Current image:  $(state_value CLI_PROXY_IMAGE)"
  echo "Current commit: $(state_value CPA_CURRENT_COMMIT)"
  echo "Previous image: $(state_value CPA_PREVIOUS_IMAGE)"
  if [[ -f "${STATE_FILE}" ]]; then
    compose ps
  fi
}

show_images() {
  local image_repository

  require_command docker
  image_repository="$(setting CLI_PROXY_IMAGE_REPOSITORY cli-proxy-api)"
  docker image ls "${image_repository}" --format 'table {{.Repository}}\t{{.Tag}}\t{{.ID}}\t{{.CreatedSince}}'
}

command_name="${1:-}"
case "${command_name}" in
  init)
    initialize
    ;;
  deploy)
    deploy_current_commit
    ;;
  upgrade)
    upgrade
    ;;
  rollback)
    rollback
    ;;
  status)
    show_status
    ;;
  logs)
    compose logs --follow --tail="$(setting CPA_LOG_TAIL 200)"
    ;;
  images)
    show_images
    ;;
  *)
    usage
    [[ -n "${command_name}" ]] && exit 1
    ;;
esac
