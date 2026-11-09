# ClipSend for iPhone and iPad

A small native SwiftUI notification companion, display name **ClipSend**, minimum
iOS/iPadOS 17. The configured bundle ID is `dev.cheechnd.clipsend`. There is no
WebView, pairing server, login, or pairing network request. The computer CLI/APNs
sender is separate from this app.

## Build and check

Reuse xtool 1.22.2 on PATH, Swift 6.4.2, and the installed `darwin` SDK
(iPhoneOS 27.1). Existing SDK/authentication is shared; no reconfiguration is needed.

```sh
cd native/ios
mkdir -p .local
xtool dev build > .local/build.log 2>&1
swift scripts/ValidateBuild.swift xtool/ClipSend.app
file xtool/ClipSend.app/ClipSend
bash scripts/check.sh
```

The build does not use a developer signing identity. xtool applies the requested
entitlements with an ad hoc signature during packaging; this is not a provisioned
development signature. Expected executable: Mach-O 64-bit arm64. Check build logs
for warnings.

`Resources/AppIcon.svg` is original clipboard/send artwork; its opaque 1024×1024
RGB PNG is selected by `iconPath`. The bundle contains `AppIcon.png` and its
Info.plist references `CFBundleIconFile = AppIcon`. Regenerate with installed tools:

```sh
rsvg-convert -w 1024 -h 1024 Resources/AppIcon.svg -o .local/icon.png
magick .local/icon.png -alpha off PNG24:Resources/AppIcon.png
```

## Development signing and exact APNs topic

The development settings must agree:

| Setting | Value |
| --- | --- |
| Info.plist `ClipSendAPNSEnvironment` | `sandbox` |
| ClipSend.entitlements `aps-environment` | `development` |
| xtool.yml entitlement key | `entitlementsPath: ClipSend.entitlements` |

xtool 1.22.2 development signing rewrites the bundle ID with an identity prefix.
For example, a signing identity may produce **`XTL-1234ABCD.dev.cheechnd.clipsend`**.
The prefix shown here is synthetic; use the actual ID from your signed artifact.
Both the canonical and this prefixed App ID have Push Notifications enabled.
Pairing exports `Bundle.main.bundleIdentifier` exactly as installed. **Do not strip
the prefix: the APNs `apns-topic` must equal the exported, actually signed ID.**
The environment comes independently from the explicit Info.plist setting.
Missing/unknown environment values visibly prevent export. There is no `DEBUG`
inference, private SecTask API, or runtime code-signature parser.

Retain a signed IPA so its actual profile can be checked before installation:

```sh
xtool dev build --sign --ipa > .local/signed-build.log 2>&1
retained_dir=$(mktemp -d .local/signed.XXXXXX)
unzip -q xtool/ClipSend.ipa -d "$retained_dir"
swift scripts/ValidateBuild.swift "$retained_dir/Payload/ClipSend.app" \
  --profile "$retained_dir/Payload/ClipSend.app/embedded.mobileprovision" \
  --expected-bundle-id XTL-1234ABCD.dev.cheechnd.clipsend
xtool dev run --usb
```

For another signing identity, deliberately set the expected ID to that identity's
exact artifact/profile ID. The validator never accepts a wildcard or silently
removes the xtool prefix. It uses Swift Foundation plus installed OpenSSL to
decode the actual CMS profile and check APS environment, exact prefixed App ID,
and expiration. CMS signature verification uses `-noverify` (no certificate-chain
validation). It does not independently validate the executable signature; iOS
does that at installation. It prints no profile contents or device identifiers.

`xtool dev run --usb` builds/signs/installs from the canonical project identity.
Its signed temporary copy may be discarded, so the retained IPA above is a
separate profile-validation artifact. The original build directory may remain ad
hoc without a profile. Do not use `xtool install` on the already-signed IPA:
xtool 1.22.2 adds its prefix again and installs a duplicate app. No additional
signing tools are needed.
An APNs provider key belongs on the computer sender, never inside this app.

For private manual profile inspection:

```sh
openssl cms -verify -inform DER -noverify \
  -in "$retained_dir/Payload/ClipSend.app/embedded.mobileprovision" \
  -out .local/profile.plist 2> .local/profile-decode.log
```

On macOS, `security cms -D -i PROFILE > .local/profile.plist` is an alternative.
Do not paste decoded profiles; they contain device/account identifiers.

For production, change **both** Info.plist and `aps-environment` to `production`,
rebuild with matching distribution signing/profile, and validate the retained
artifact's exact ID/environment. xtool development provisioning is not a
production distribution workflow. Re-export pairing from that installation;
sandbox and production tokens are not interchangeable.

## Permissions and pairing

APNs registration runs on every process launch without asking for alert
permission. **Enable notifications** deliberately requests alert/sound/badge
permission. If denied, use **Notification settings**; returning refreshes status.
Tokens live only in memory and are never logged or uploaded by ClipSend.

Name the device, then use **Share pairing details** or **Copy pairing details**.
Choose LocalSend, or paste the text into LocalSend's text sender, and send it to
the computer. Sharing does not silently create a remote pairing; ClipSend cannot
know whether the computer imported it. Bundle ID and environment are behind
**Device details**. “Ready to share” appears only when a complete export is available.

The JSON has exactly five keys. This example uses a synthetic token:

```json
{"version":1,"deviceName":"My iPhone","deviceToken":"000a80ff","bundleID":"XTL-1234ABCD.dev.cheechnd.clipsend","environment":"sandbox"}
```

Tokens are lowercase hexadecimal without a fixed-length assumption. Launch and
**Refresh device token** withhold sharing until a fresh token arrives. Failure or
timeout remains visible with a refresh button. Re-share after token rotation,
reinstall, restore, or a signing/environment change; update the computer's record.
A previously exported/copied token does not update itself. Only the device name
is saved locally. No ATS exceptions or background execution modes are requested.

## Open LocalSend

On each iPhone/iPad, install LocalSend and Shortcuts. Create a shortcut named
exactly **Open LocalSend**, add **Open App**, choose **LocalSend**, and run it once.
The app's button and a default-action push-notification tap use:

```text
shortcuts://run-shortcut?name=Open%20LocalSend
```

A push tap queues the request until the scene is active, including a cold launch.
Dismissal does not route. Mere receipt only presents the notification; it never
auto-launches another app or executes a payload URL. **Open LocalSend** remains
available for retry.

URL acceptance only means Shortcuts accepted the request. Missing shortcuts or
Open App failures are shown by Shortcuts; ClipSend displays setup/retry guidance
and reports inability to open Shortcuts itself. Verify LocalSend actually opens.

## Verification status

Verified on 2026-11-08:

- Warning-free unsigned arm64 build with xtool 1.22.2, Swift 6.4.2 and iPhoneOS 27.1.
- Correct display name, sandbox environment, iPhone/iPad families, icon metadata,
  and packaged opaque 1024px PNG.
- Standalone Swift checks: exact JSON schema/escaping/hex, production export,
  preserving the actual prefixed bundle ID, missing configuration, mismatched
  environments, and expired/wildcard/wrong-App-ID profiles. Test executables clean
  themselves up.
- A retained signed artifact confirmed xtool's prefix rewrite and retained
  `sandbox`. The corrected signed IPA's embedded profile passed development APS,
  exact signed App ID, and expiration checks. Both
  unsigned and signed builds passed without warnings. The earlier on-device
  “unknown environment” was caused by a runtime
  canonical-ID guard, which has been removed. A valid token alone no longer
  produces a misleading “Ready to share” status.

The corrected app was installed using `xtool dev run --usb`. The retained
validation artifact is `xtool/ClipSend.ipa`; its inspected copy is under
`.local/final-signed/Payload/ClipSend.app`. Both are ignored build outputs.

On iPhone, notification permission, pairing export through LocalSend, APNs delivery,
and tapping the notification to open LocalSend were confirmed. With LocalSend in
the background, the CLI sent an alert, waited, and delivered a two-line message
after the tap; a screenshot confirmed both lines and the Copy button. Shortcuts
requested permission to output the app item, which was allowed using Always Allow.

On iPad, installation, token registration, pairing export, and notification delivery
were confirmed. Tapping the test notification opened LocalSend without prompts;
the Open LocalSend shortcut was already available.
The combined iPad test also passed: LocalSend was in the background, the alert
opened it, and the waiting CLI delivered both test lines with the Copy button.

Remaining device checks include permission denial and Settings return, foreground
and cold-launch notification taps.
Check iPhone/iPad rotation, Dynamic
Type, VoiceOver, keyboard/scrolling, and iPad share popovers on actual devices.
Compilation and URL acceptance do not establish those outcomes.

Generated apps, IPA/profile/key files, retained artifacts and logs stay ignored.
No credentials are bundled. This native directory does not include the APNs sender.

References: [xtool schema](https://github.com/xtool-org/xtool/blob/1.22.2/Sources/PackLib/PackSchema.swift),
[xtool signing](https://github.com/xtool-org/xtool/blob/1.22.2/Sources/XToolSupport/DevCommand.swift),
[Apple APNs registration](https://developer.apple.com/documentation/usernotifications/registering-your-app-with-apns),
[Apple Shortcuts URLs](https://support.apple.com/en-ae/guide/shortcuts/apd624386f42/ios).
