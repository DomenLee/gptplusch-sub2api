#!/usr/bin/env bash
set -Eeuo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
version="$(tr -d '\r\n' < "${root_dir}/backend/cmd/server/VERSION")"

if [[ "${GITHUB_REF:-}" != refs/heads/main ]]; then
  echo 'Production releases must run from main; select main in GitHub Actions.' >&2
  exit 1
fi
if [[ ! "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo 'Production VERSION must be X.Y.Z without branch names, dates, hashes or build metadata.' >&2
  exit 1
fi
if [[ "${GITHUB_SHA:-}" != "$(git -C "$root_dir" rev-parse HEAD)" ]]; then
  echo 'Checked out commit does not match the GitHub Actions source SHA; stop and rerun main.' >&2
  exit 1
fi
if [[ "$version" != "$(bash "${root_dir}/backend/scripts/resolve-version.sh")" ]]; then
  echo 'Release tag and VERSION disagree; update VERSION before publishing.' >&2
  exit 1
fi
printf 'version=%s\ndate=%s\n' "$version" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >> "${GITHUB_OUTPUT:?GitHub Actions output file is required}"
printf 'Production source verified: main %s version=%s\n' "$GITHUB_SHA" "$version"
