import QtQuick
import Quickshell
import Quickshell.Io
import "State.js" as UiState

// One service instance is registered by Omarchy for this plugin id. Every
// monitor's bar widget forwards to this object and shares its single JSONL
// process, transfer state, and inbox.
Item {
  id: root
  visible: false

  property var shell: null
  property bool started: false
  property string executable: "clipsend"
  property string desiredExecutable: "clipsend"
  property bool restartRequested: false
  property int requestSerial: 0
  property string activeRequest: ""
  property var activePayload: null
  property var failedSend: null
  property string pairRequest: ""
  property bool pairCancelling: false
  property var requestActions: ({})
  property string selectedAlias: ""
  property var devices: []
  property var inbox: []
  property var pairing: null
  property var approval: null
  property string notice: ""
  property bool receiving: false
  property string receiveEndpoint: ""

  function start(candidate) {
    var path = String(candidate || "clipsend").trim()
    desiredExecutable = path || "clipsend"
    if (started) {
      if (desiredExecutable !== executable) notice = "Executable changed; restart the connection to apply it"
      else if (notice === "Executable changed; restart the connection to apply it") notice = "Connected"
      return false
    }
    executable = desiredExecutable
    var command = [executable, "ui"]
    // Fixture-only override for local integration runs. Kept out of plugin
    // settings so the product UI has no test endpoint controls.
    var fixtureHost = String(Quickshell.env("CLIPSEND_UI_HOST") || "").trim()
    var fixturePort = Number(Quickshell.env("CLIPSEND_UI_PORT") || 0)
    if (fixtureHost && fixtureHost.length <= 255) command.push("--host", fixtureHost)
    if (Number.isInteger(fixturePort) && fixturePort > 0 && fixturePort <= 65535)
      command.push("--port", String(fixturePort))
    uiProcess.command = command
    started = true
    notice = "Starting ClipSend…"
    uiProcess.running = true
    return true
  }

  function request(command, fields) {
    if (!uiProcess.running) {
      notice = "ClipSend UI process is unavailable"
      return ""
    }
    requestSerial++
    var id = "ui-" + requestSerial
    var message = Object.assign({ id: id, command: command }, fields || {})
    var actions = Object.assign({}, requestActions)
    actions[id] = command
    requestActions = actions
    uiProcess.write(JSON.stringify(message) + "\n")
    return id
  }

  function refresh() { request("devices"); request("inbox") }

  function send(alias, files) {
    if (activeRequest) { notice = "A transfer is already in progress"; return }
    if (!alias) { notice = "Pair a device to send"; return }
    var fields = { alias: alias }
    var payload = { alias: alias, files: files && files.length ? files.slice() : [], source: files && files.length ? "files" : "clipboard" }
    if (payload.files.length) fields.files = payload.files
    var id = request("send", fields)
    if (!id) return
    activeRequest = id
    activePayload = payload
    failedSend = null
    notice = files && files.length ? "Preparing files…" : "Preparing clipboard…"
  }

  function retryFailed() {
    if (!failedSend) return
    var prior = failedSend
    send(prior.alias, prior.files)
  }

  function restart() {
    receiving = false
    receiveEndpoint = ""
    activeRequest = ""
    activePayload = null
    failedSend = null
    pairRequest = ""
    pairCancelling = false
    pairing = null
    approval = null
    notice = "Restarting ClipSend…"
    if (uiProcess.running) {
      restartRequested = true
      uiProcess.running = false
      return
    }
    started = false
    start(desiredExecutable)
    refresh()
  }

  function cancelActive() {
    if (!activeRequest) return
    request("cancel", { requestId: activeRequest })
    notice = "Cancelling transfer…"
  }

  function cancelPairing() {
    if (!pairRequest || pairCancelling) return
    if (!request("cancel", { requestId: pairRequest })) return
    pairCancelling = true
    pairing = null
    notice = "Cancelling pairing…"
  }

  function receiveLine(line) {
    var event
    try { event = JSON.parse(line) } catch (e) { return }
    var next = UiState.reduce({
      requestActions: requestActions, activeRequest: activeRequest,
      activePayload: activePayload, failedSend: failedSend,
      pairRequest: pairRequest, pairCancelling: pairCancelling, devices: devices, inbox: inbox,
      selectedAlias: selectedAlias, receiving: receiving,
      receiveEndpoint: receiveEndpoint, notice: notice,
      pairing: pairing, approval: approval
    }, event)
    requestActions = next.requestActions || requestActions
    activeRequest = next.activeRequest || ""
    activePayload = next.activePayload || null
    failedSend = next.failedSend || null
    pairRequest = next.pairRequest || ""
    pairCancelling = next.pairCancelling === true
    devices = next.devices || []
    inbox = next.inbox || []
    selectedAlias = next.selectedAlias || ""
    receiving = next.receiving === true
    receiveEndpoint = next.receiveEndpoint || ""
    notice = next.notice || ""
    pairing = next.pairing || null
    approval = next.approval || null
    for (let i = 0; i < (next.effects || []).length; i++) request(next.effects[i])
  }

  Process {
    id: uiProcess
    running: false
    stdinEnabled: true
    stdout: SplitParser { onRead: function(line) { root.receiveLine(line) } }
    stderr: SplitParser { onRead: function(line) { if (line.trim()) console.warn("clipsend", line.trim()) } }
    onExited: function(code) {
      root.started = false
      root.activeRequest = ""
      root.activePayload = null
      root.failedSend = null
      root.pairRequest = ""
      root.pairCancelling = false
      root.receiving = false
      root.receiveEndpoint = ""
      root.inbox = []
      root.pairing = null
      root.approval = null
      root.requestActions = ({})
      if (root.restartRequested) {
        root.restartRequested = false
        root.start(root.desiredExecutable)
        root.refresh()
      } else {
        root.notice = "ClipSend UI stopped (" + code + "). Choose Restart connection to reconnect."
      }
    }
  }

  // Explicitly stop the child when Omarchy disables/unloads the plugin.
  // Quickshell terminates the process; Go's signal context runs its deferred
  // listener and ephemeral inbox cleanup, so no child is left behind.
  Component.onDestruction: uiProcess.running = false
}
