# Release readiness

The native receiver has replaced the LocalSend bridge. Direct delivery, notification
opening, Copy, and lock-screen delivery have passed physical iPhone/iPad checks.
A successful build is not evidence of physical iPhone/iPad behavior or App Store
approval. No public service, paid infrastructure, or store release is authorized by
local implementation work.

## Personal enhancement build

The functional enhancement scope is implemented; the interface-polish phase is
not complete. The current panel and mobile screens are a working baseline, not
the finished personal product. Remaining design work includes a clearer primary
send/receive action, less repeated explanatory copy and nested framing, more
discoverable attachment actions, and consistent transfer feedback across surfaces.
That work needs visual and interaction review on the desktop, iPhone, and iPad,
including keyboard, scrolling, touch targets, and transitions, before calling the
polish phase complete.

The optional Omarchy panel, clipboard images, attachment batches, and two-way
transfers are implemented. Physical checks covered image previews and Copy on
iPad, image/PDF batches, PDF previews on both devices, visible Done dismissal on
both devices, and notification delivery into the updated iPhone app. The iPad
Share extension returned an image and the iPhone Share extension returned a PDF;
both returned files matched the originals byte-for-byte. Existing pairings survived
the updates. Desktop image Copy was exercised through the real panel and pasted.

Automated coverage includes actual CLI/UI subprocesses and pinned HTTPS sockets,
unauthorized and revoked access, malformed manifests, length/hash failures, quota
reservations, PNG/JPEG copy fidelity, save-without-overwrite, receive/pair restoration,
token refresh, ordinary CLI handoff while receiving, and interrupted sends. These
checks run with Go's race detector. UI reducer checks and Swift protocol checks
complement actual shell/device testing; they do not replace it.

Real-device large-file stress, every Photos/Files provider, VoiceOver, all Dynamic
Type sizes, denied permissions, and network transitions still require broader
coverage before distribution. The shipped app remains a personal developer build.

## Acceptance gates

- Go race tests, static checks, malformed/unauthorized protocol tests, and actual
  TLS socket exchanges pass.
- QR produced by the actual CLI decodes and pairs with an explicit approval.
- CLI -> notification metadata -> pinned fetch -> acknowledgment works end to end
  with disposable test fixtures, including Unicode and the message limit.
- iPhone/iPad install, camera scan/manual fallback, Keychain persistence, notification
  permission, local-network permission, cold launch, Copy, links, and recovery pass
  on actual devices.
- Simultaneous sends, revoked pairings, expired transfers, Wi-Fi changes, sleeping
  computers, denied permissions, reinstall and token renewal have clear outcomes.
- Accessibility, Dynamic Type, dark/light mode, iPad layout, keyboard, scrolling,
  camera denial and interruption are manually checked.
- Notification service hosting, credential enrollment/rotation, monitoring, abuse
  protection, retention and operating cost policy are approved and deployed.
- Public app identity, production APNs signing, TestFlight beta, store materials,
  privacy policy, support channel and licensing terms are finalized.

## Cutover

Updates preserve the existing native identity, aliases, and Keychain pairings.
Test candidate executables separately before replacing the installed binary. The
Share extension must be signed with its own application identity and the host's
existing pairing group; use the documented device-build script. No Preview app,
LocalSend transport, or Shortcuts handoff is required for ordinary operation.

## Uninstall

Remove the desktop executable/symlink and optional completion. Remove the mobile
app. Revoke paired devices and operator-issued notification credentials. Delete the
native config directory only when its identities and pairings are no longer needed.
No system service is installed by the desktop CLI.
If installed, disable/remove the optional `clipsend.omarchy` plugin as well.
Its temporary inbox is under the user cache directory and can be deleted after
the process stops. Build tooling caches are separate from pairing credentials.
