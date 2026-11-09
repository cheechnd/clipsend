# ClipSend Omarchy plugin

Build and install `clipsend` first using the root README, then install the optional
panel from the repository root:

```sh
mkdir -p ~/.config/omarchy/plugins
cp -R integrations/omarchy ~/.config/omarchy/plugins/clipsend.omarchy
omarchy-shell shell rescanPlugins
omarchy plugin enable clipsend.omarchy --section right
omarchy-shell shell summon clipsend.omarchy '{}'
```

Discovery is asynchronous; if enable says the plugin is not yet known, run that
command again after the rescan completes. For a source update, replace the files
inside the existing plugin directory rather than copying another directory into
it. If the running shell retains old QML code, `omarchy restart shell` reloads it;
this stops receiving and clears the temporary inbox. It does not reset your shell
configuration. Removing or disabling this plugin leaves the standalone CLI usable.

Use **Send** for clipboard or file delivery, **Inbox** to enable receiving and
Copy/Save/Clear deliveries, and **Devices** for pairing, renaming, and unpairing.
Opening the panel does not read the clipboard. PNG and JPEG images can be copied
without changing their encoded bytes. Other attachments can be explicitly saved.
The UI uses keyboard-focusable controls; Tab navigates and Escape closes the panel.

`manifest.json` registers a shared service and an on demand bar widget.
Omarchy creates one `Service.qml` per shell; all per-monitor `Panel.qml`
instances use that service's single `clipsend ui` process, version 1 JSONL
stream, transfer state, and inbox. Closing a panel only hides its
`KeyboardPanel`. The service is kept loaded during shell hot reload, and
receiving starts off until enabled by the switch. Disabling/unloading the
plugin stops its child process; the backend's EOF cleanup turns off listeners
and removes ephemeral inbox data.

The plugin uses the installed Omarchy `BarWidget`, `BarIconButton`,
`KeyboardPanel`, `Button`, `Toggle`, and Quickshell `Process`/`SplitParser`
APIs. Shell summon/hide/toggle calls route through the widget's `open`,
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
