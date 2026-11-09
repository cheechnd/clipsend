.pragma library

function copyObject(value) {
  var out = {}
  for (var key in value || {}) out[key] = value[key]
  return out
}

function settleAction(state, event) {
  var next = copyObject(state)
  var actions = copyObject(state.requestActions)
  next.action = actions[event.id] || ""
  delete actions[event.id]
  next.requestActions = actions
  next.effects = []
  return next
}

function chooseDevice(state) {
  var devices = Array.isArray(state.devices) ? state.devices : []
  var selected = String(state.selectedAlias || "")
  for (var i = 0; i < devices.length; i++)
    if (String(devices[i].alias || "") === selected) return selected
  return devices.length ? String(devices[0].alias || "") : ""
}

function reduce(state, event) {
  var next = copyObject(state)
  next.effects = []
  if (!event || event.version !== 1 || typeof event.type !== "string") return next

  if (event.type === "devices") {
    next.devices = Array.isArray(event.devices) ? event.devices : []
    next.selectedAlias = chooseDevice(next)
  } else if (event.type === "inbox") {
    next.inbox = Array.isArray(event.items) ? event.items : []
  } else if (event.type === "incoming") {
    next.notice = "Received from " + String(event.alias || "device")
    next.effects = ["inbox"]
  } else if (event.type === "pairing") {
    if (!state.pairCancelling)
      next.pairing = { qr: String(event.qr || ""), png: String(event.png || ""), expiresAt: event.expiresAt }
  } else if (event.type === "approval") {
    next.approval = { requestId: String(event.requestId || event.id || ""), name: String(event.name || "Device") }
  } else if (event.type === "status" || event.type === "progress") {
    if (event.type === "progress" && Number(event.total) > 0) {
      next.notice = "Sending " + Math.floor(Number(event.bytes) * 100 / Number(event.total)) + "%"
    } else if (event.state === "preparing") next.notice = "Preparing…"
    else if (event.state === "waiting") next.notice = "Waiting for device…"
    else if (event.state === "sending") next.notice = "Sending…"
    else if (event.state === "receive_restored") {
      next.receiving = true
      next.receiveEndpoint = String(event.endpoint || next.receiveEndpoint || "")
      next.notice = "Receiving is on"
    }
  } else if (event.type === "result") {
    next = settleAction(state, event)
    if (next.action === "devices") {
      next.devices = Array.isArray(event.devices) ? event.devices : []
      next.selectedAlias = chooseDevice(next)
      if (next.notice === "Starting ClipSend…") next.notice = "Connected"
    } else if (next.action === "inbox") {
      next.inbox = Array.isArray(event.items) ? event.items : []
    } else if (next.action === "receive") {
      next.receiving = event.enabled === true
      next.receiveEndpoint = next.receiving ? String(event.endpoint || "") : ""
      next.notice = next.receiving ? "Receiving is on" : "Receiving is off"
    } else if (next.action === "send") {
      next.notice = event.state === "received" ? "Sent" : String(event.state || "Transfer complete")
      if (event.id === state.activeRequest) {
        next.activeRequest = ""
        next.activePayload = null
        next.failedSend = null
      }
    } else if (next.action === "pair") {
      next.pairing = null
      next.pairRequest = ""
      next.pairCancelling = false
      next.notice = "Device paired"
      next.effects = ["devices", "inbox"]
    } else if (next.action === "rename" || next.action === "unpair") {
      next.notice = next.action === "rename" ? "Device renamed" : "Device unpaired"
      if (next.action === "rename") next.effects = ["devices"]
    } else if (next.action === "clear") {
      next.effects = ["inbox"]
    } else if (next.action === "copy") {
      next.notice = "Copied"
    } else if (next.action === "save") {
      next.notice = "Saved"
    } else if (next.action === "approve") {
      next.approval = null
    }
  } else if (event.type === "error") {
    next = settleAction(state, event)
    next.notice = String(event.message || event.code || "Action failed")
    if (event.id === state.activeRequest) {
      next.activeRequest = ""
      next.failedSend = event.code === "cancelled" ? null : state.activePayload
      if (next.failedSend) next.failedSend = Object.assign({}, next.failedSend, { code: String(event.code || "") })
      next.activePayload = null
    }
    if (next.action === "pair") { next.pairing = null; next.pairRequest = ""; next.pairCancelling = false }
  }
  return next
}
