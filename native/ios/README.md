# ClipSend for iPhone and iPad

Native SwiftUI companion for ClipSend protocol v1. The app scans or accepts a pasted
computer pairing QR payload, requests pairing over HTTPS with a per-computer leaf
certificate pin, and waits up to two minutes for approval on the computer. Pairing
credentials are stored in Keychain under the installed app identifier.

While ClipSend is open, it browses Bonjour `_clipsend._tcp.` services for paired
computer IDs. It authenticates each candidate with the stored bearer secret and
certificate pin, validates the transfer and expiration, and acknowledges only
after attachment bytes have streamed into private temporary files and passed
declared-size and SHA-256 checks. Text and files remain ephemeral; the inbox is
limited to 32 items and 256 MiB of files. Copy, Preview, Share/Save, and Clear are
explicit actions. Image previews are downsampled; Copy image preserves original
PNG, JPEG, or HEIC bytes after bounded ImageIO validation. A valid APNs tap or
foreground alert uses only its validated computer and transfer metadata.

The in-app send composer accepts explicit text, Files, and Photos selections and
sends only to a paired computer advertising `receive=1`. The native Share
extension accepts text, URLs, images, and files, then uses the same pinned HTTPS
upload protocol. Its bundle ID is `dev.cheechnd.clipsend.share`; it queries the
root app’s existing Keychain service/access group with a temporary disposable
Keychain probe. It never moves or rewrites saved pairings. The device build
verifies separate host/extension identities and grants both the host's existing
pairing group, preserving installed pairings.

Computer aliases and pairings can be renamed or forgotten in Settings. Notification
permission is requested only after an explicit user action. Camera and local network
access have purpose-specific system descriptions. HTTPS redirects are rejected;
ATS exceptions are limited to private IPv4 ranges for pinned local HTTPS.
Global arbitrary loads are disabled, and each connection must match the paired
certificate. Device tokens use the actual installed bundle ID and
the configured APNs environment.

## Build and portable checks

Reuse xtool 1.22.2, Swift 6.4.2, and the installed iPhoneOS SDK:

```sh
cd native/ios
mkdir -p .local
xtool dev build > .local/build.log 2>&1
bash scripts/check.sh
```

`xtool dev build` packages the app and Share extension without provisioning or
device installation. xtool 1.22.2 applies ad-hoc signing whenever configured
products declare entitlements; extension entitlement mapping is limited by the
current signer implementation, so inspect command output and packaged host and
extension entitlements before signing for a device. `scripts/check.sh` runs host-portable protocol tests for whole-second and
fractional RFC3339 timestamps, private HTTPS endpoint validation, invitation
expiry, text and attachment validation, filename safety, deduplication keys, plus
the existing build-configuration self-check.

## Install on a device

A signed developer installation requires an Apple developer account, configured
xtool signing access, and an app identifier with Push Notifications enabled. With
the intended device connected over USB, unlocked, and trusted, run from this directory:

```sh
bash scripts/build-device.sh
pymobiledevice3 apps install --developer xtool/ClipSend.app
```

The build script provisions with xtool, then corrects xtool 1.22.2's single-bundle
entitlement handling using a pinned zsign source revision and a small signing
helper. It checks the resulting entitlements before returning. The first run
needs Git, Make, G++, OpenSSL development headers, and Python 3, and builds the
helper under `$XDG_CACHE_HOME/clipsend-build`. These are build tools, not mobile or
desktop runtime dependencies. Install `pymobiledevice3` as a shared CLI with
`uv tool install pymobiledevice3` if needed. Use the intended connected USB device;
disconnect other devices if selection is ambiguous.

Do not use `xtool dev run` for this extension build: it signs again and overwrites
the corrected extension identity. The installation command above uses the already
signed app without modifying it. Existing xtool credentials are reused locally;
temporary key copies have private permissions and are deleted after signing.

Open ClipSend manually after installation and follow setup. Configure desktop
notifications using the [root README](../../README.md#notifications), then pair.
Signing credentials and APNs keys stay outside the repository. There is currently
no App Store or TestFlight installation offered by this project.

Camera pairing, first-run setup, direct receipt, Copy, notification opening and
lock-screen delivery have been tested on physical iPhone and iPad devices.
Broader permission-recovery, VoiceOver, Dynamic Type, and layout coverage remain
release gates; compilation and portable checks do not establish those outcomes.
