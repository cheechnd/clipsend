#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
xtool dev build --sign
python3 scripts/sign-extensions.py xtool/ClipSend.app
