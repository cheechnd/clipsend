#!/usr/bin/env bash
# Offscreen only: real installed shell controls, fake service, no clipboard or backend.
set -euo pipefail
source_dir=$(cd "$(dirname "$0")/.." && pwd)
output_dir=${1:-/tmp/clipsend-panel-review}
mkdir -p "$output_dir"
output_dir=$(realpath "$output_dir")
render_dir=$(mktemp -d /tmp/clipsend-panel-render.XXXXXX)
trap 'rm -rf "$render_dir"' EXIT
ln -s /usr/share/omarchy/shell/Commons "$render_dir/Commons"
ln -s /usr/share/omarchy/shell/Ui "$render_dir/Ui"
ln -s "$source_dir" "$render_dir/Plugin"
cp "$source_dir/tests/Render.qml" "$render_dir/shell.qml"
fixture_qr=$(qrencode -t PNG -o - 'clipsend-render-fixture-not-an-invitation' | base64 -w0)
if ! timeout 30s env -u HYPRLAND_INSTANCE_SIGNATURE -u WAYLAND_DISPLAY QT_QPA_PLATFORM=offscreen QT_QUICK_BACKEND=software \
  CLIPSEND_FIXTURE_QR="$fixture_qr" CLIPSEND_RENDER_DIR="$output_dir" quickshell --no-color -p "$render_dir/shell.qml" > "$output_dir/render.log" 2>&1; then
  cat "$output_dir/render.log"
  exit 1
fi
if rg -n 'FAIL|Error|ReferenceError|TypeError|Unable to|Cannot assign|Binding loop' "$output_dir/render.log"; then exit 1; fi
rg 'PASS' "$output_dir/render.log"
printf 'Renders: %s\n' "$output_dir"
