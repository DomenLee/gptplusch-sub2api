#!/usr/bin/env bash
set -Eeuo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "${root_dir}/deploy/vultr-ssh-deploy.sh"
target_sha=0123456789abcdef0123456789abcdef01234567
validate_target "$target_sha" "ghcr.io/domenlee/sub2api:${target_sha}"
if validate_target "$target_sha" ghcr.io/domenlee/sub2api:latest; then
  echo 'Mutable image tag accepted' >&2
  exit 1
fi
if validate_target "$target_sha" "ghcr.io/example/sub2api:${target_sha}"; then
  echo 'Unapproved image repository accepted' >&2
  exit 1
fi
main_ref_sha="$target_sha"
curl() {
  printf '{"object":{"sha":"%s"}}\n' "$main_ref_sha"
}
verify_main_commit "$target_sha"
if verify_main_commit aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; then
  echo 'Non-main commit accepted' >&2
  exit 1
fi
curl() {
  return 22
}
if verify_main_commit "$target_sha" 2>/dev/null; then
  echo 'Deployment accepted without main verification' >&2
  exit 1
fi
echo 'Vultr main-only deployment tests passed'
