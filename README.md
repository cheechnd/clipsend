# ClipSend

Send clipboard text from Linux directly to your paired iPhone or iPad. Tap the
notification, read the text in ClipSend, and Copy. No LocalSend or Shortcuts,
automatic clipboard monitoring, exported text files, or permanent message history.

**Native receiver.** Direct delivery, notification opening, and Copy have been
tested on physical iPhone and iPad devices. The iOS app currently requires a signed developer installation;
public distribution and hosted customer enrollment are not yet available; see [release readiness](docs/RELEASE.md).

## Daily use

```sh
clipsend iphone
clipsend ipad
printf 'A link or some text' | clipsend send ipad --stdin
```

The computer and device must be on the same local IPv4 network. Keep the computer
awake and the command running until the device confirms receipt. Only UTF-8 text is supported, up to 64,000 bytes per send. Text is held only
in process memory and each send expires after two minutes. The command reports
**Received** after the app acknowledges holding the text; it does not claim that you
have copied or pasted it. Cancellation or expiry means receipt was not confirmed.

When the app is already open it can discover the send directly. Otherwise, tapping a generic
notification opens ClipSend to retrieve it. Each send and notification targets only
the named paired device. Notification service failure does not
prevent opening the app manually while the send is pending.

## Install and pair

The desktop build needs Go 1.26+ and Linux. Run the build commands from a clone
of this repository and ensure `~/.local/bin` is on your `PATH`. Clipboard reading uses `wl-clipboard`
in a Wayland session; `--stdin` works independently of the clipboard.

```sh
go build -trimpath -o clipsend .
mkdir -p ~/.local/bin
ln -s "$PWD/clipsend" ~/.local/bin/clipsend
clipsend pair iphone
```

Install the [native iOS app](native/ios/README.md) and choose **Set up ClipSend**.
Setup requests notification permission, checks local-network discovery, then offers
the pairing scanner. Camera permission is requested when you scan. Denied access
has an Open Settings action; setup checks again when you return. You can explicitly
continue without alerts and receive with the app open.

Scan the terminal QR code, then approve the named device on the computer. The CLI
offers an optional test notification after pairing (requires notification configuration
below): put the device on its Home
Screen before accepting the test. Pairing expires in five minutes. If camera access is unavailable,
`clipsend pair iphone --code` prints pairing content for the app’s Paste pairing code
action. `--qr /path/to/pair.png` additionally creates a private QR image for scanning; the command removes that image when pairing ends.

```sh
clipsend devices
clipsend rename iphone mobile
clipsend unpair mobile
clipsend doctor
```

Choose a unique name when pairing each device, such as `iphone`, `ipad`, or
`family-phone`. These are stored pairing names, not built-in device types or shell
aliases. Aliases are exact, case-insensitive names. Unpairing deletes the device credential
and revokes its access to the computer. Forgetting a computer in the mobile app
removes its locally saved pairing. If you lose a device, unpair it on each computer.

If several supported local networks are active, run `clipsend doctor` and pass
`--host <local-IP>` to pairing or sending. This avoids guessing which network your
phone is on. Multicast/Bonjour must be permitted for foreground discovery; wake
notifications also carry direct local endpoint information. Guest Wi-Fi client
isolation can prevent direct delivery.

Pairing and sends listen on TCP port **53318**, bound to the chosen LAN address.
Allow that port from your trusted local subnet through the computer's firewall.
Only one pairing or send can use that address/port at a time; finish or cancel it
before starting another. `--port PORT` selects an explicitly allowed alternative.
The CLI never changes firewall rules itself. It listens only while pairing or
sending; no router port forwarding is needed. Push notifications use outbound
HTTPS to Apple or your configured relay.

## Notifications

Notifications are optional: foreground delivery works without them. Configure
either direct APNs access for a personal Apple developer installation or an
operator-provided hosted relay. Neither route sends clipboard text through Apple.

### Hosted relay

With a hosted notification service, the desktop client does not need an Apple
developer account or APNs private key.
A separately operated [notification service](internal/relay/README.md) holds those
credentials and accepts scoped installation credentials. The service sends fixed
alert text and opaque computer/transfer IDs plus local connection endpoints. It
never receives clipboard text or device-pairing secrets.

Public hosting and automatic customer enrollment are not deployed. For a private
installation, the operator provisions a scoped credential and supplies a mode `0600`
file containing:

```json
{"url":"https://notify.example.com","token":"REPLACE_WITH_OPERATOR_ISSUED_INSTALLATION_TOKEN"}
```

Import it with `clipsend relay /path/to/private-config.json`. For local operator
work, HTTP is accepted only on a numeric loopback address; remote services require
verified HTTPS. Installing the client never starts a background daemon.

### Personal Apple developer installation

An operator who owns an APNs key can send notifications directly from the CLI,
without a running relay process. Keep the service configuration described in
[the operator guide](internal/relay/README.md) in a private file, then import:

```json
{"operatorConfig":"/absolute/path/to/operator.json","token":"REPLACE_WITH_SCOPED_CREDENTIAL"}
```

`operatorConfig` and `url` are mutually exclusive. The operator file and APNs key
must remain private and outside the repository. The scoped credential must match
the paired computer and signed app bundle. Direct sends use the same notification
validation and TLS transport as the service. Service rate limits apply to the
hosted HTTP endpoint; direct operator sends are trusted local operations.

## Data and trust

Desktop identity, paired-device credentials and notification routing live in
`$XDG_CONFIG_HOME/clipsend-native` (default `~/.config/clipsend-native`). This is a
separate store from the prior release. Files are private and written atomically.
The phone stores paired credentials in Keychain. Text remains in memory; clearing
it or terminating the app removes the app's in-memory copy. Explicit Copy places
text on the operating system clipboard, whose own retention is outside ClipSend.

A scanned invitation pins the computer's certificate. Subsequent direct transfers
require both that TLS pin and the device's bearer credential. Bonjour discovery
and push metadata never grant trust by themselves. See [protocol](docs/PROTOCOL.md)
and [security boundaries](docs/SECURITY.md).

## Development and releases

```sh
go test -race -timeout 120s ./...
go vet ./...
bash native/ios/scripts/check.sh
bash scripts/build-release.sh v0.1.0
```

The release script builds Linux amd64/arm64 executables and checksums in ignored
`dist/`. Bash completion is in `completions/clipsend.bash`. Native builds use the
existing Swift/xtool workflow; private signing materials never belong in the repo.
`bash scripts/build-ios-preview.sh` builds the same native sources under a separate
**ClipSend Preview** identity so testing does not replace the working installed app.

An opt-in integration suite exercises the real LAN listener, CLI, QR decoding,
mock notification service and pinned receiver. It uses disposable credentials,
not your clipboard or paired devices. It needs a single eligible LAN and `zbarimg`:

```sh
CLIPSEND_NETWORK_TEST=1 go test -race -run TestNative -timeout 40s .
```

[Release readiness](docs/RELEASE.md) distinguishes automated evidence from device
acceptance, service operation, and App Store distribution.
