#!/usr/bin/env bash

set -Eeuo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
UPSTREAM_URL="https://github.com/router-for-me/CLIProxyAPI.git"
UPSTREAM_BRANCH="${CPA_UPSTREAM_BRANCH:-main}"
LOCAL_MAIN_BRANCH="${CPA_LOCAL_MAIN_BRANCH:-main}"
CUSTOM_BRANCH="${1:-custom/main}"

fail() {
  echo "Error: $*" >&2
  exit 1
}

command -v git >/dev/null 2>&1 || fail "git was not found"
git -C "${ROOT_DIR}" rev-parse --is-inside-work-tree >/dev/null 2>&1 ||
  fail "${ROOT_DIR} is not a Git repository"

if [[ -n "$(git -C "${ROOT_DIR}" status --porcelain --untracked-files=normal)" ]]; then
  fail "the Git checkout is dirty; commit or stash changes before syncing upstream"
fi

if ! git -C "${ROOT_DIR}" remote get-url upstream >/dev/null 2>&1; then
  git -C "${ROOT_DIR}" remote add upstream "${UPSTREAM_URL}"
fi

echo "Fetching origin and upstream..."
git -C "${ROOT_DIR}" fetch origin --prune
git -C "${ROOT_DIR}" fetch upstream --prune --tags

if git -C "${ROOT_DIR}" show-ref --verify --quiet "refs/heads/${LOCAL_MAIN_BRANCH}"; then
  git -C "${ROOT_DIR}" switch "${LOCAL_MAIN_BRANCH}"
else
  git -C "${ROOT_DIR}" switch --create "${LOCAL_MAIN_BRANCH}" --track "upstream/${UPSTREAM_BRANCH}"
fi

git -C "${ROOT_DIR}" merge --ff-only "upstream/${UPSTREAM_BRANCH}"

if git -C "${ROOT_DIR}" show-ref --verify --quiet "refs/heads/${CUSTOM_BRANCH}"; then
  git -C "${ROOT_DIR}" switch "${CUSTOM_BRANCH}"
elif git -C "${ROOT_DIR}" show-ref --verify --quiet "refs/remotes/origin/${CUSTOM_BRANCH}"; then
  git -C "${ROOT_DIR}" switch --create "${CUSTOM_BRANCH}" --track "origin/${CUSTOM_BRANCH}"
else
  git -C "${ROOT_DIR}" switch --create "${CUSTOM_BRANCH}" "${LOCAL_MAIN_BRANCH}"
fi

git -C "${ROOT_DIR}" merge --no-edit "${LOCAL_MAIN_BRANCH}"

echo
echo "Upstream is merged into ${CUSTOM_BRANCH}. Review and validate the result, then push:"
echo "  git push origin ${LOCAL_MAIN_BRANCH}"
echo "  git push -u origin ${CUSTOM_BRANCH}"
echo
echo "Check configuration and deployment-file changes before deploying:"
echo "  git diff HEAD@{1}..HEAD -- config.example.yaml docker-compose.yml Dockerfile"
