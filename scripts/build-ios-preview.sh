#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p native/ios/.local
preview_dir=$(mktemp -d "$PWD/native/ios/.local/preview.XXXXXX")
cp native/ios/Package.swift native/ios/ClipSend.entitlements "$preview_dir/"
cp -R native/ios/Sources native/ios/Resources "$preview_dir/"
sed 's/bundleID: dev.cheechnd.clipsend$/bundleID: dev.cheechnd.clipsend.preview/' \
  native/ios/xtool.yml > "$preview_dir/xtool.yml"
sed 's/<string>ClipSend<\/string>/<string>ClipSend Preview<\/string>/' \
  native/ios/Info.plist > "$preview_dir/Info.plist"
(
  cd "$preview_dir"
  xtool dev build > build.log 2>&1
)
printf '%s\n' "$preview_dir" > native/ios/.local/preview-path
printf 'Preview built in %s\n' "$preview_dir"
printf 'Install on the intended USB device from that directory with: xtool dev run --usb\n'
