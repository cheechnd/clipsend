# ClipSend Omarchy plugin

Build and install `clipsend` first using the root README. Review and validate the
plugin source before deploying. Install or update the complete plugin with the
shell stopped; copying QML files into a running shell triggers repeated watcher
reloads. A Qt garbage-collector crash occurred during one such update; stopping
the shell avoids that reload burst.

From the repository root, with the desktop unlocked:

```sh
# Verify the running shell's config path first (normally /usr/share/omarchy/shell).
quickshell list --all --json
quickshell kill -p /usr/share/omarchy/shell --any-display
# Wait for the matching shell instance to exit before copying.
mkdir -p ~/.config/omarchy/plugins/clipsend.omarchy
cp -a integrations/omarchy/. ~/.config/omarchy/plugins/clipsend.omarchy/
omarchy restart shell
```

Use the verified live shell path in the kill command if it differs from the
packaged path above. Start the shell once, after all files are in place; do not
rescan or hot-reload between file copies. On first installation only, enable the
widget after startup discovery completes:

```sh
omarchy plugin enable clipsend.omarchy --section right
omarchy-shell shell summon clipsend.omarchy '{}'
```

Stopping the shell temporarily stops receiving, cancels active transfers and
pairing invitations, and clears the ephemeral inbox and retry state. Save any
inbox files you want to keep first. Existing device pairings and saved files
remain untouched. Turn receiving back on in Inbox after restart. Removing or
disabling this plugin leaves the standalone CLI usable.

The panel opens on **Send**, with a compact ClipSend header and receiving state.
Select the recipient row to reveal paired devices, then send the clipboard or
use the file row to choose or drop files. **Inbox** and **Devices** are navigation
rows below the send actions; the header back arrow returns to Send. The file
picker keeps the destination selected when it opened. A failed send offers a retry labelled with its original destination; clipboard retry
reads the current clipboard, while file retry uses the same paths.

**Inbox** keeps text and attachments together, with explicit Copy and Save
actions. Long text can expand in place. Deliveries appear newest first with receipt times. Clear all removes the current
inbox as one undoable batch. Clear removes a delivery immediately and offers Undo for eight seconds;
consecutive clears share the Undo window. Temporary files are deleted after that
window expires. Attachments have an explicit Open action for supported system
viewers, as well as Save. Incoming deliveries automatically open Inbox on the
active desktop output. The bar icon stays neutral; its underline marks an open panel. **Devices** holds pairing, rename/unpair, and an
explicit restart confirmation that explains what will be cleared.
Opening the panel does not read the clipboard. PNG and JPEG images can be copied
without changing their encoded bytes. Other attachments can be explicitly saved.
Tab navigates controls and Ctrl+Tab switches views. Escape cancels an inline
edit, recipient selection, or pairing/restart confirmation, then returns from
a secondary view to Send before closing the panel. Navigation preserves drafts.
The header/back action and feedback stay visible while content scrolls; focused
content is brought into view.

`manifest.json` registers a shared service and an on demand bar widget.
Omarchy creates one `Service.qml` per shell; all per-monitor `Panel.qml`
instances use that service's single `clipsend ui` process, version 1 JSONL
stream, transfer state, and inbox. Closing a panel only hides its
`KeyboardPanel`. The service is kept loaded during shell hot reload, and
receiving starts off until enabled by the switch. Disabling/unloading the
plugin stops its child process; the backend's EOF cleanup turns off listeners
and removes ephemeral inbox data.

The plugin uses the installed Omarchy `BarWidget`, `BarIconButton`,
`KeyboardPanel`, `PanelHero`, `PanelSeparator`, `PanelActionButton`, `Button`,
`Toggle`, and Quickshell `Process`/`SplitParser` APIs. Shell summon/hide/toggle calls route through the widget's `open`,
`close`, and `toggle` methods, including keyboard shortcuts. Clipboard reads
and writes, pairing state, transfer data, and inbox ownership remain in the Go
process. QML sends paths as JSON fields, never as shell command text. Dynamic
labels and inbox content use plain text rendering.

For candidate testing, set the widget's **ClipSend executable** setting to an
absolute path to the candidate binary. The default is `clipsend` from `PATH`.
Changing the executable while connected updates the desired path and asks for
an explicit connection restart; it never silently interrupts transfers.
Local fixtures can use `CLIPSEND_UI_HOST` and `CLIPSEND_UI_PORT` in the shell's
environment; these are passed as argument-array options and are not product
settings. No files need to be installed or copied into Omarchy's packaged
directories. Hiding the bar visually leaves the shared service alive. Explicit
plugin disable/unload ends the child process, which turns receiving off and
lets Go's signal-driven shutdown close listeners and remove its temporary
inbox. `Process.running = false` terminates the child; this is not an stdin EOF.

Run the state reducer regression checks with `node integrations/omarchy/State.test.cjs`.

The view lives in `PanelContent.qml`, separately from the bar window and native
file dialogs in `Panel.qml`. `Label.qml` enforces plain text; installed `qs.Ui`
controls provide the theme, focus states, and control styling. Plugin labels
use `i18n/en.json`, with the shell's language fallback. Error category/action
metadata is presentation-only; the backend JSONL protocol is unchanged.

For an isolated visual and interaction check using the installed Omarchy kit:

```sh
integrations/omarchy/tests/render.sh /tmp/clipsend-panel-review
omarchy plugin validate integrations/omarchy
```

The harness needs Quickshell, QtTest, and `qrencode`. It uses an offscreen window,
a fake service, and a nonfunctional QR fixture. It does not run ClipSend, open
live windows, install the plugin, or access the clipboard. It writes PNGs and a
log for Send, recipient selection, Inbox/Clear/Undo, Devices, empty/busy,
clipboard/file retry, pairing/approval, narrow, and light states. Assertions exercise Tab/Enter, action routing, plain-text markup, and
focus scrolling. The native layer-shell window and file dialogs still need
integration testing in a deployment review; the offscreen platform cannot
instantiate `KeyboardPanel`'s Wayland backend.
