# ClipSend

Send your clipboard to your iPhone or iPad as a LocalSend message with a Copy
button. No text files, desktop notifications, or background service. An optional
iPhone/iPad companion can notify you when LocalSend needs opening.

```sh
clipsend iphone
clipsend ipad
clipsend          # choose a device in the terminal; Enter cancels
clipsend --list
```

Without the companion, open LocalSend on the receiving device on the same LAN.
After sending, tap Copy
in LocalSend and paste where you need it. This is an explicit text transfer,
not automatic clipboard synchronization. Images are not supported.

## Install

Requires Go 1.25+ to build and `wl-clipboard` to read the Wayland clipboard.
The binary implements the LocalSend protocol itself using Go's standard library;
Python, openssl, and the desktop LocalSend CLI are not required.

```sh
CGO_ENABLED=0 go build -trimpath -o clipsend .
mkdir -p ~/.local/bin
ln -s "$PWD/clipsend" ~/.local/bin/clipsend
```

Rebuild with the same `go build` command after source changes; the symlink
always points to the current build. Keep this checkout in place while installed.

There are no changes to Omarchy, Hyprland, keybindings, or menus.
Remove `~/.local/bin/clipsend` to uninstall the command.

## Device names

Use a device's full LocalSend name or an unambiguous part of it. For short names,
create `~/.config/clipsend/aliases.json`:

```json
{
  "iphone": "Your phone's LocalSend name",
  "ipad": "Your iPad's LocalSend name"
}
```

Aliases follow device names instead of DHCP addresses. `--list` shows current
names and addresses; addresses from that list are also accepted as destinations.
`XDG_CONFIG_HOME` is respected. The same directory holds the local TLS identity;
keep its private key private. No clipboard contents or transfer history are saved.

## Optional iPhone/iPad companion

The native companion lives in [`native/ios`](native/ios/README.md). It needs an
Apple developer account, a signed app installed on the device, notification
permission, and an **Open LocalSend** shortcut whose action opens LocalSend.
It sends no clipboard contents through Apple.

On the computer, store your APNs credentials in
`~/.config/clipsend/push.json` (permissions `0600`):

```json
{
  "keyID": "KEYID12345",
  "teamID": "YOURTEAM12",
  "keyPath": "/absolute/path/to/AuthKey_KEYID12345.p8"
}
```

Use the actual ten-character IDs from your developer account. `keyPath` must be
an absolute path to the APNs key, stored outside this repository with permissions
`0600`. An App Store Connect API key is a different credential.

In the companion, share its pairing details to the computer. Copy that JSON into
the computer's clipboard, then pair it with the same alias you use for LocalSend:

```sh
clipsend pair iphone                # reads pairing JSON from the clipboard
clipsend pair ipad /path/to/pairing.json
clipsend notify iphone              # explicit notification-only test
clipsend unpair iphone              # deletes this notification pairing
```

Pairing stores only notification routing. Keep `aliases.json` pointing at the
intended device's LocalSend name. Pairing again replaces that alias's notification
token; do this after reinstalling the companion or if Apple invalidates its token.
The companion exports the current token when opened.

With notifications configured, `clipsend iphone` still sends immediately when
LocalSend is available. Otherwise it sends one generic alert and waits up to
90 seconds for that device. Tap the alert, unlock if needed, and let the shortcut
open LocalSend. The captured clipboard stays in process memory; Ctrl+C or a
wait timeout discards it. A transfer whose result is uncertain is never retried.
No destination is guessed and no notification is sent for an ambiguous name.

Apple accepting a notification is not proof it appeared on the phone: permission,
Focus settings, connectivity, and the app's provisioning can affect delivery.
The CLI reports success only after LocalSend confirms the text message.

## Message-only guarantee

ClipSend includes the complete text as a LocalSend message preview and never
calls the file-upload endpoint. It refuses empty or invalid UTF-8 text and text
over 64,000 bytes. A receiver requesting file upload is cancelled. Success means
the receiver returned HTTP 204, not that you have tapped Copy. A timeout is an
unconfirmed result; ClipSend does not retry automatically.

HTTPS receivers are pinned to the certificate observed during discovery, checked
again before the message is sent. This is not persistent device authentication.

## Verification

```sh
go test -race ./...
go vet ./...
```

Tests use local mock receivers, including message-only refusal and certificate
checks. Actual iPhone/iPad Copy behavior and absence of saved files require a
manual device check; automated tests do not establish that outcome.

Discovery scans bounded, directly connected IPv4 LANs (at most 1,024 addresses
per subnet), excluding common VPN and container interfaces and this computer.
IPv6-only networks, larger LAN subnets, and multicast-only discovery are not
currently supported.

Live check on 2026-11-08: both iPhone and iPad accepted a multiline message
containing a URL and Unicode. Copy/paste and absence of new `.txt` files were
confirmed on both devices.

The notification companion builds and installs on iPhone, and its signed APNs
profile has been checked. A live test with LocalSend in the background delivered
an alert; tapping it opened LocalSend through the shortcut, and the waiting CLI
delivered the complete two-line message. The device screenshot confirmed the
message and Copy button. The companion was also installed and paired on iPad;
its test notification opened LocalSend without additional prompts.
The combined iPad test also passed: with LocalSend in the background, tapping
the alert opened it and the waiting CLI delivered both test lines with Copy.
