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
  property var rawInbox: []
  property var pendingClears: []
  readonly property var inbox: rawInbox.filter(function(item) { return root.pendingClears.indexOf(String(item.transferId)) < 0 })
  property bool revealInbox: false
  function clearItem(id) {
    if (pendingClears.indexOf(String(id)) >= 0) return
    pendingClears = pendingClears.concat([String(id)])
    undoTimer.restart()
  }
  function clearAll() {
    var ids = inbox.map(function(item) { return String(item.transferId) })
    if (!ids.length) return
    pendingClears = pendingClears.concat(ids)
    undoTimer.restart()
  }
  function undoClear() { undoTimer.stop(); pendingClears = [] }
  function commitClears() {
    var ids = pendingClears.slice()
    var submitted = ids.filter(function(id) { return root.request("clear", { transferId: id }) !== "" })
    rawInbox = rawInbox.filter(function(item) { return submitted.indexOf(String(item.transferId)) < 0 })
    pendingClears = []
  }
  Timer { id: undoTimer; interval: 8000; onTriggered: root.commitClears() }
  property var pairing: null
  property var approval: null
  property string notice: ""
  property string noticeKind: "info"
  property string noticeAction: ""
  property bool receiving: false
  property string receiveEndpoint: ""

  function setNotice(message, kind, action) {
    noticeKind = kind || "info"
    noticeAction = action || ""
    notice = message
  }

  function start(candidate) {
    var path = String(candidate || "clipsend").trim()
    desiredExecutable = path || "clipsend"
    if (started) {
      if (desiredExecutable !== executable) setNotice("Executable changed; restart the connection to apply it")
      else if (notice === "Executable changed; restart the connection to apply it") setNotice("Connected")
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
    setNotice("Starting ClipSend…")
    uiProcess.running = true
    return true
  }

  function request(command, fields) {
    if (!uiProcess.running) {
      setNotice("ClipSend UI process is unavailable", "error", command)
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
    if (activeRequest) { setNotice("A transfer is already in progress"); return }
    if (!alias) { setNotice("Pair a device to send"); return }
    var fields = { alias: alias }
    var payload = { alias: alias, files: files && files.length ? files.slice() : [], source: files && files.length ? "files" : "clipboard" }
    if (payload.files.length) fields.files = payload.files
    var id = request("send", fields)
    if (!id) return
    activeRequest = id
    activePayload = payload
    failedSend = null
    setNotice(files && files.length ? "Preparing files…" : "Preparing clipboard…")
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
    setNotice("Restarting ClipSend…")
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
    if (!request("cancel", { requestId: activeRequest })) return
    setNotice("Cancelling transfer…")
  }

  function cancelPairing() {
    if (!pairRequest || pairCancelling) return
    if (!request("cancel", { requestId: pairRequest })) return
    pairCancelling = true
    pairing = null
    setNotice("Cancelling pairing…")
  }

  function receiveLine(line) {
    var event
    try { event = JSON.parse(line) } catch (e) { return }
    var next = UiState.reduce({
      requestActions: requestActions, activeRequest: activeRequest,
      activePayload: activePayload, failedSend: failedSend,
      pairRequest: pairRequest, pairCancelling: pairCancelling, devices: devices, inbox: rawInbox,
      selectedAlias: selectedAlias, receiving: receiving,
      receiveEndpoint: receiveEndpoint, notice: notice, noticeKind: noticeKind, noticeAction: noticeAction,
      pairing: pairing, approval: approval
    }, event)
    requestActions = next.requestActions || requestActions
    activeRequest = next.activeRequest || ""
    activePayload = next.activePayload || null
    failedSend = next.failedSend || null
    pairRequest = next.pairRequest || ""
    pairCancelling = next.pairCancelling === true
    devices = next.devices || []
    rawInbox = next.inbox || []
    selectedAlias = next.selectedAlias || ""
    receiving = next.receiving === true
    receiveEndpoint = next.receiveEndpoint || ""
    notice = next.notice || ""
    noticeKind = next.noticeKind || "info"
    noticeAction = next.noticeAction || ""
    pairing = next.pairing || null
    approval = next.approval || null
    for (let i = 0; i < (next.effects || []).length; i++) request(next.effects[i])
    if (event.version === 1 && event.type === "incoming") {
      revealInbox = true
      if (shell) shell.summon("clipsend.omarchy", "{}")
    }
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
      root.rawInbox = []
      root.pendingClears = []
      undoTimer.stop()
      root.pairing = null
      root.approval = null
      root.requestActions = ({})
      if (root.restartRequested) {
        root.restartRequested = false
        root.start(root.desiredExecutable)
        root.refresh()
      } else {
        root.setNotice("ClipSend UI stopped (" + code + "). Choose Restart connection to reconnect.", "error")
      }
    }
  }

  // Explicitly stop the child when Omarchy disables/unloads the plugin.
  // Quickshell terminates the process; Go's signal context runs its deferred
  // listener and ephemeral inbox cleanup, so no child is left behind.
  Component.onDestruction: uiProcess.running = false
}
