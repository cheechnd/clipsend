# ClipSend native protocol v1

Implementation contract for the replacement. No LocalSend, Shortcuts, plaintext
clipboard relay, or permanent text history. All times UTC RFC3339. JSON camelCase.
Text is UTF-8, nonempty, max 64,000 bytes. Transfer IDs and invitation/secret values
are cryptographically random. Unknown versions fail closed. Transfer TTL 120 seconds;
pair invitation TTL 300 seconds. Tokens/secrets never enter logs.

## Trust and pairing

Computer persists a self-signed TLS certificate/private key and random computerID.
Phone pins SHA256 of the DER leaf certificate, independent of endpoint hostname.
Phone credentials in Keychain; desktop credentials in private atomic 0600 files in
a new `clipsend-native` config directory (never touch legacy clipsend config).

QR encodes this JSON as UTF-8 (not a URL):
`{"version":1,"computerID":"random-id","computerName":"Desktop","endpoint":"https://192.168.1.2:12345","certificateSHA256":"64-lowercase-hex","invitation":"random-32-byte-base64url","expiresAt":"RFC3339"}`.

POST /v1/pair, pinned TLS, JSON body:
`{"invitation":"...","deviceName":"iPad","deviceToken":"APNs-hex","bundleID":"actual-signed-id","environment":"sandbox"}`.
Desktop explicitly approves device name. Invitation is single use; expiry or denial
rejects. Response 201:
`{"version":1,"computerID":"...","computerName":"Desktop","deviceID":"random-id","secret":"random-32-byte-base64url"}`.
Phone stores the QR fingerprint + response identity/secret. Desktop records same
deviceID/secret with user-selected alias and APNs routing. No hostnames are trusted
without cert pinning. TLS minimum1.2. Never globally disable TLS validation.

## Delivery

Each CLI send serves HTTPS on a random port until acknowledgment, cancellation,
or expiry. Multiple sends may run concurrently, with distinct transfer IDs/ports.
Text stays in memory. Every route below requires `Authorization: Bearer SECRET`
for that specific paired recipient. Wrong/unknown tokens get 401 without details.

GET /v1/transfers => 200 `{"version":1,"transfers":[{"id":"...","expiresAt":"..."}]}`.
GET /v1/transfers/ID => 200 `{"version":1,"id":"...","computerID":"...","senderName":"Desktop","text":"...","expiresAt":"..."}`.
POST /v1/transfers/ID/ack => 204 after phone holds complete validated text in memory.
Repeated ack is idempotent while session remains; never report desktop success on GET.
Expired transfer =>410. Unknown ID =>404. Phone deduplicates by computerID+transferID
in memory; no clipboard writing until explicit Copy. Clear/dismiss drops text.

POST /v1/device/token (bearer) body `{"deviceToken":"...","bundleID":"...","environment":"sandbox"}`
updates APNs routing atomically. Client refreshes token on a discovered authenticated
session. Token never logged. A foreground scan can discover/retrieve without APNs.

## Discovery and wake

Bonjour service `_clipsend._tcp.` domain `local.`. TXT `v=1`, `computerID=...`.
Unique service instance per server session. Resolve addresses+port, match stored
computerID, then connect with stored cert pin/bearer. Bonjour alone grants no trust.
Phone browses while active. No background polling service.

Generic APNs alert "Text ready" / "Tap to receive in ClipSend." plus custom payload:
`"clipsend":{"version":1,"computerID":"...","transferID":"...","endpoints":["https://192.168.1.2:12345"]}`.
No text, secret, or cert material in push. On tap, match known computer then retrieve
using pinned TLS. Unknown computers/expired work show clear recovery UI, no fetch.
Notification handling never routes to a third-party app. An explicit user-selected
Open Link action may open an HTTP(S) link after receipt. Receiving while active
retrieves via discovery.

## Notification service

Separate Go service, private APNs key, no clipboard data. Authenticated installations
have scoped bearer credentials issued by the operator (public enrollment/hosting is
a release gate, not an unauthenticated endpoint). POST /v1/notify body:
`{"deviceToken":"hex","bundleID":"signed-id","environment":"sandbox","notification":{"version":1,"computerID":"...","transferID":"...","endpoints":["https://192.168.1.2:12345"]}}`.
Server validates allowed app topics, environment, body size, private IP endpoint URLs,
rate limits per credential, and fixed alert copy. 202 means Apple accepted; never
means receiver got text. Desktop stores relayURL+relayToken outside repo. No direct
APNs credentials distributed to customer clients. Tests use local injected mock APNs.

## Ownership

Protocol worker: internal/direct/** (server+private store+meaningful tests).
Native worker: native/ios/** (replace companion, scan/pair/discover/receive/copy UI).
Service worker: internal/relay/** and cmd/clipsend-relay/** (service+client+tests).
Primary: root CLI, dependencies, docs, packaging, integration and review.
Workers may not commit/push, install on devices, edit other owned files, or publish.
