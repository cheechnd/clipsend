#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
release_version=${1:-dev}
case "$release_version" in *[!A-Za-z0-9._-]*|'') echo 'Use a simple version such as v1.0.0' >&2; exit 1;; esac
mkdir -p dist
for release_arch in amd64 arm64; do
  release_name="clipsend-${release_version}-linux-${release_arch}"
  CGO_ENABLED=0 GOOS=linux GOARCH="$release_arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$release_version" -o "dist/$release_name" .
done
(
  cd dist
  sha256sum "clipsend-${release_version}-linux-amd64" "clipsend-${release_version}-linux-arm64" > "SHA256SUMS-${release_version}"
)
echo "Built Linux amd64/arm64 binaries and checksums in dist/."
