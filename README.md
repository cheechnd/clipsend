# ClipSend

Send your clipboard to your iPhone or iPad as a LocalSend message with a Copy
button. No text files, desktop notifications, or background service.

```sh
clipsend iphone
clipsend ipad
clipsend          # choose a device in the terminal; Enter cancels
clipsend --list
```

Open LocalSend on the receiving device on the same LAN. After sending, tap Copy
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
