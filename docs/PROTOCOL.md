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
a new `clipsend-native` config directory (separate from the retired bridge config).

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

Each CLI send serves HTTPS on LAN TCP port 53318 by default until acknowledgment,
cancellation, or expiry. A persistent UI receiver shares that port with sends.
Explicit `--port` overrides permit separate listeners where the firewall allows them.
Text stays in memory. Every route below requires `Authorization: Bearer SECRET`
for that specific paired recipient. Wrong/unknown tokens get 401 without details.

GET /v1/transfers => 200 `{"version":1,"transfers":[{"id":"...","expiresAt":"..."}]}`.
GET /v1/transfers/ID => 200 `{"version":1,"id":"...","computerID":"...","senderName":"Desktop","text":"...","expiresAt":"..."}`.
POST /v1/transfers/ID/ack => 204 after the phone holds complete validated text and attachments.
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

Generic APNs alert "ClipSend ready" / "Tap to receive in ClipSend." plus custom payload:
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


For personal operator installations, the CLI can load a private operator config
and send directly to APNs without running the HTTP service. This uses the same
request validation and scoped credentials; hosted HTTP rate limits do not apply
to these trusted local operations. See the root README for configuration.

## Attachments and return delivery

Version 1 transfer payloads may include `attachments`, an array of
`{id,name,mime,size,sha256}` descriptors. `size` is a JSON integer byte count and
`sha256` is lowercase hexadecimal. Text may be empty when attachments are present.
Identifiers contain only ASCII letters, digits, `_`, and `-`, up to 128 bytes.
There are at most 16 attachments, each at most 128 MiB, totaling at most 256 MiB.
The sender snapshots files before serving them so later source edits cannot change
the advertised content. `GET /v1/transfers/ID/items/ITEM` streams the bytes using
the same recipient credential. The receiver verifies length and hash before ack.

A computer with receiving explicitly enabled advertises Bonjour TXT `receive=1`.
iOS uses its existing pairing pin and bearer credential to upload:

1. `POST /v1/incoming` with `{version:1,text,attachments}` returns 201 with
   `{id,expiresAt}` and reserves the declared bytes against the inbox quota.
2. `PUT /v1/incoming/ID/items/ITEM` uploads raw bytes and returns 204 after length
   and hash validation.
3. `POST /v1/incoming/ID/complete` returns 204 only when every item is validated.
4. `DELETE /v1/incoming/ID` cancels a pending delivery.

All routes authenticate the paired device. The inbox is limited to 32 deliveries
and 256 MiB, including pending reservations. Partial deliveries expire after two
minutes. Completed desktop items expire after 24 hours, explicit Clear, or process
exit. The persistent listener also accepts authenticated `/v1/device/token` refreshes.

## Desktop UI transport

`clipsend ui` accepts version-1 JSONL commands on stdin and emits correlated
events on stdout. Commands include devices, send, cancel, pair, approve, rename,
unpair, receive, inbox, copy, save, and clear. Each request has an `id` and
`command`; terminal events use `result` or `error`. Pairing QR events are private
invitation material. Public device-list events contain only aliases and names.

The panel owns one backend process across monitors. A mode-0600 Unix socket in
the private config directory lets ordinary CLI sends delegate to that process;
both sides verify the peer UID. It is not a network API. Clipboard operations
remain explicit, saves never overwrite, and EOF or termination cancels work and
cleans temporary data. Pairing pauses and then restores the receiving listener.
