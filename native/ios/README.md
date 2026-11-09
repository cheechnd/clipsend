# ClipSend for iPhone and iPad

Native SwiftUI receiver for ClipSend protocol v1. The app scans or accepts a pasted
computer pairing QR payload, requests pairing over HTTPS with a per-computer leaf
certificate pin, and waits up to two minutes for approval on the computer. Pairing
credentials are stored in Keychain under the installed app identifier.

While ClipSend is open, it browses Bonjour `_clipsend._tcp.` services for paired
computer IDs. It authenticates each candidate with the stored bearer secret and
certificate pin, validates the transfer and expiration, keeps the text in memory,
then sends the acknowledgment. The in-memory inbox is limited to 32 items. Copy,
Open link, and Clear are explicit actions. No clipboard write or automatic external
app handoff occurs. A valid APNs tap or foreground alert uses only its validated
computer and transfer metadata to fetch text inside ClipSend.

Computer aliases and pairings can be renamed or forgotten in Settings. Notification
permission is requested only after an explicit user action. Camera and local network
access have purpose-specific system descriptions. HTTPS redirects are rejected;
there are no ATS exceptions. Device tokens use the actual installed bundle ID and
the configured APNs environment.

## Build and portable checks

Reuse xtool 1.22.2, Swift 6.4.2, and the installed iPhoneOS SDK:

```sh
cd native/ios
mkdir -p .local
xtool dev build > .local/build.log 2>&1
bash scripts/check.sh
```

`xtool dev build` creates the local unsigned build artifact. It does not install
the app. `scripts/check.sh` runs host-portable protocol tests for whole-second and
fractional RFC3339 timestamps, private HTTPS endpoint validation, invitation
expiry, transfer size/identity/expiry, and deduplication keys, plus the existing
build-configuration self-check.

Physical camera, Bonjour permission, APNs delivery, onboarding, VoiceOver, Dynamic
Type, and iPhone/iPad layout behavior still need device confirmation. Compilation
and portable checks do not establish those device outcomes.
