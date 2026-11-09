#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p .local
checks_dir=$(mktemp -d .local/checks.XXXXXX)
trap 'rm -rf "$checks_dir"' EXIT
swiftc Sources/ClipSend/PairingDetails.swift Sources/ClipSend/BuildConfiguration.swift Tests/CoreChecks.swift -o "$checks_dir/core-checks"
"$checks_dir/core-checks"
swift scripts/ValidateBuild.swift --self-check
