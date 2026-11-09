# `internal/direct` integration API

This package owns the native TLS identity, private paired-device state, pairing,
and in-memory transfer sessions. Bonjour and CLI remain caller owned.

## Store

```go
store, err := direct.OpenStore(configDir) // private 0700 directory
if err != nil { ... }
defer store.Close()
identity, devices, err := store.Snapshot() // one consistent read; reports corrupt state
// Identity(): direct.Identity; Devices(): []direct.Device are convenience accessors.
phone, ok := store.FindDevice("iPad")
err = store.RenameDevice(phone.ID, "Travel iPad")
err = store.RevokeDevice(phone.ID)
```

`Identity` exports `ComputerID`, `CertificateSHA256` (lowercase SHA-256 of the DER
leaf), `CertificatePEM`, and `PrivateKeyPEM`. `Device` exports `ID`, `Alias`,
`Name`, `Secret`, APNs `DeviceToken`, `BundleID`, `Environment`, and `CreatedAt`.
Keep credentials out of output and logs. Store transactions use a short cross-process
file lock; reads do not rewrite state. Existing store identity and permission errors
are returned without repair writes.

## Pairing

```go
pairing, err := direct.ListenPairing(store, direct.PairingConfig{
    BindAddress: "0.0.0.0:0", EndpointHost: "192.168.1.4",
    ComputerName: "Desktop",
    ApprovePair: func(ctx context.Context, req direct.PairRequest) (alias string, approve bool) {
        // Show explicit approval UI; return the selected local alias.
    },
})
go pairing.Serve()
invitation := pairing.Invitation() // Value and ExpiresAt; include these in QR JSON
endpoint := pairing.Endpoint() // https://192.168.1.4:<random-port>
<-pairing.Done()
if pairing.Result() == direct.Acked { /* 201 credentials response was flushed */ }
```

`PairRequest` contains `DeviceName`, APNs hex `DeviceToken` (required), `BundleID`,
`Environment`, and `Invitation`. The token must be 1–4096 even-length hex characters;
app topic and environment must be valid. Unknown, malformed, expired, or reserved
invitations do not prompt approval. A valid invitation is reserved before the
callback. Its context is bounded by invitation expiry and at most 120 seconds.
Alias selection is required and unique case-insensitively. `PairingServer` exposes
`Endpoint()`, `Invitation()`, `Serve()`, `Done()`, `Result()`, and `Close()`. The
listener closes on success, cancellation, or the five-minute invitation expiry.

## Transfers

```go
session, err := direct.ListenSession(store, direct.SessionConfig{
    BindAddress: "0.0.0.0:0", EndpointHost: "192.168.1.4",
    DeviceID: phone.ID, SenderName: "Desktop", Text: text,
})
go session.Serve()
endpoint := session.Endpoint() // Bonjour endpoint; one random port per send
<-session.Done()
switch session.Result() {
case direct.Acked: /* report delivered */
case direct.Cancelled, direct.Expired: /* not delivered */
}
```

Text must be nonempty valid UTF-8 and at most 64,000 bytes. A session exposes
`ID()`, `ExpiresAt()`, `Endpoint()`, `Serve()`, `Cancel()`, `Done()`, and `Result()`.
It binds one immutable recipient and transfer ID, expires within 120 seconds, and
keeps text only in memory; terminal completion clears it. Every session route checks
the current on-disk bearer secret for that recipient, so cross-process revocation is
immediate. Routes include transfer listing, metadata/text retrieval, idempotent ack,
and authenticated APNs token refresh. `Done()` signals `Acked` only after the 204
ack response has been flushed. Pairing credentials and JSON/text responses use
`Cache-Control: no-store`; server header size is bounded.
