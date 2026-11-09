# Security boundaries

ClipSend transfers explicit text between paired devices on one local network.
It does not monitor the clipboard or synchronize it in the background.

## Trusted components

The paired computer and phone can read transferred text. A compromised endpoint
or user account is outside the protection provided by transport encryption.
Pairing QR codes contain an expiring bearer invitation and a certificate pin;
show them only when pairing an intended device. The desktop asks for approval.

The notification operator holds the APNs signing key. The relay accepts scoped
installation credentials, fixed alert wording, bounded routing metadata and
allowed app topics. It sees device tokens, local endpoint addresses, request timing,
and computer/transfer identifiers. Apple sees the push payload. Neither receives
clipboard text or the direct-transfer bearer credential.

TLS protects text on the LAN. The phone verifies a certificate established by
pairing rather than trusting the LAN, DNS, Bonjour, or a push-provided address.
There is no trust-on-first-use reconnect or automatic re-pair after a mismatch.
Unexpected identity changes require deliberate re-pairing.

The iOS app declares local networking and ATS exceptions limited to private IPv4
ranges, permitting manual trust evaluation with a TLS 1.2 minimum. Its client
still requires HTTPS and an exact paired certificate pin, and evaluates that
certificate as an explicit trust anchor. The streaming request receives the pinned
delegate explicitly for authentication and redirect handling. Global arbitrary
loads remain disabled; HTTP URLs and redirects are rejected by the client.

## Lifetime and revocation

Invitations expire after five minutes and are single use. Sends expire after two
minutes; receipt must be explicitly acknowledged by the authenticated receiver.
No message journal is written. Text messages live only in memory. Attachments use
private temporary files; receivers enforce declared lengths and SHA-256 before
acknowledgment, and sender snapshots isolate transfers from source-file changes.
Desktop inbox quotas include pending reservations. Partial uploads expire, and
clearing or restarting removes temporary payloads. iOS removes stale temporary
payloads at startup. Explicit Save/Share is the only export to user storage.
The OS clipboard
and any destination into which the user pastes are separate from ClipSend storage.

Computer-side unpair deletes the bearer credential and makes existing sessions
reject that device. Phone-side forgetting removes the corresponding Keychain entry.
Protect and revoke notification-service credentials separately; they cannot fetch
text but can cause notifications for their permitted installation/app scope.

The optional Omarchy service is a local frontend to the Go process. Its private
Unix socket checks same-UID peers, and its JSON device list omits tokens and
pairing secrets. Network-supplied labels are displayed as plain text. Receiving
must be explicitly enabled and never writes the clipboard automatically.
The iOS Share extension has its own signed application identity and only the
containing app's pairing Keychain access group. It uses the same certificate pin
and rejects redirects; discovery does not grant trust.

## Public release requirements

Do not deploy an open APNs forwarding endpoint. Hosted enrollment needs a deliberate
identity/abuse policy, credential issuance and rotation, operational rate limits,
incident response, and a privacy/retention policy. The current operator-provisioned
service is a private beta deployment path, not automatic public onboarding.

The tests are development evidence, not an independent security audit. Certificate
pinning, malformed traffic, replay/deduplication, concurrent store writes, revoked
pairings and production provisioning require review before a public release.
