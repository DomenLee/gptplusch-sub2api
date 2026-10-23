#!/usr/bin/env bash
set -Eeuo pipefail

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT
mkdir -p "${tmp_dir}/deploy" "${tmp_dir}/backend/cmd/server" "${tmp_dir}/backend/scripts"
cp "${root_dir}/deploy/check-production-release.sh" "${tmp_dir}/deploy/"
cp "${root_dir}/backend/scripts/resolve-version.sh" "${tmp_dir}/backend/scripts/"
printf '0.2.18\n' > "${tmp_dir}/backend/cmd/server/VERSION"
git -C "$tmp_dir" init -q
git -C "$tmp_dir" add .
git -C "$tmp_dir" -c user.name=release-test -c user.email=release-test@example.invalid commit -qm test
export GITHUB_SHA="$(git -C "$tmp_dir" rev-parse HEAD)"
export GITHUB_REF=refs/heads/main
export GITHUB_OUTPUT="${tmp_dir}/output"
bash "${tmp_dir}/deploy/check-production-release.sh"
grep -Fxq 'version=0.2.18' "$GITHUB_OUTPUT"

for branch in refs/heads/custom-ui refs/heads/codex/test refs/tags/v0.2.18; do
  if GITHUB_REF="$branch" bash "${tmp_dir}/deploy/check-production-release.sh"; then
    echo "Non-main source accepted: ${branch}" >&2
    exit 1
  fi
done
for version in '0.2.18+feature-20261023' 'v0.2.18' '0.2.18-rc.1' '00.2.18' '0.2' ''; do
  printf '%s\n' "$version" > "${tmp_dir}/backend/cmd/server/VERSION"
  if bash "${tmp_dir}/deploy/check-production-release.sh"; then
    echo "Invalid production version accepted: ${version}" >&2
    exit 1
  fi
done
printf '0.2.18\n' > "${tmp_dir}/backend/cmd/server/VERSION"
if GITHUB_SHA=0000000000000000000000000000000000000000 bash "${tmp_dir}/deploy/check-production-release.sh"; then
  echo 'Mismatched source SHA accepted' >&2
  exit 1
fi
git -C "$tmp_dir" tag v0.2.17
if bash "${tmp_dir}/deploy/check-production-release.sh"; then
  echo 'Mismatched release tag accepted' >&2
  exit 1
fi
echo 'Production release policy tests passed'
