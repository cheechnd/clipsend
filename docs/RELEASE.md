# Release readiness

The native receiver has replaced the LocalSend bridge. Direct delivery, notification
opening, Copy, and lock-screen delivery have passed physical iPhone/iPad checks.
A successful build is not evidence of physical iPhone/iPad behavior or App Store
approval. No public service, paid infrastructure, or store release is authorized by
local implementation work.

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

Keep the existing binary, aliases, private config and installed companion working
until the new receiver passes device acceptance. Pair new native identities in the
separate `clipsend-native` config. Switch the installed binary only after testing.
The replacement source has no LocalSend transport or Shortcuts handoff. Removing
legacy private state and the old app/shortcut is a deliberate final migration step,
not a prerequisite for testing the native branch.

## Uninstall

Remove the desktop executable/symlink and optional completion. Remove the mobile
app. Revoke paired devices and operator-issued notification credentials. Delete the
native config directory only when its identities and pairings are no longer needed.
No system service is installed by the desktop CLI.
