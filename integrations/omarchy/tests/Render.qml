import QtQuick
import QtQuick.Window
import QtTest
import Quickshell
import qs.Commons
import qs.Commons as Commons
import "Plugin" as Plugin

Window {
  id: window
  visible: true
  width: 422
  height: 660
  color: Commons.Color.popups.background
  TestCase { id: input; name: "PanelInteraction"; when: false }
  property int step: 0
  property var frames: ["send", "chooser", "inbox", "clear", "undo", "devices", "empty", "busy", "retry", "retry-files", "pairing", "approval", "narrow", "narrow-send", "light"]
  property int dismissals: 0
  QtObject { id: fakeShell; property string summoned: ""; function summon(id, payload) { summoned = id; return true } }
  Plugin.Service { id: undoService; shell: fakeShell }
  QtObject {
    id: fixture
    property bool started: true
    property var devices: [{alias: "iPhone"}, {alias: "iPad"}, {alias: "Studio Mac"}]
    property string selectedAlias: "iPhone"
    property var requestActions: ({})
    property string activeRequest: ""
    property bool receiving: true
    property var failedSend: null
    property string pairRequest: ""
    property bool pairCancelling: false
    property var pairing: null
    property var approval: null
    property string notice: "Connected"
    property string noticeKind: "info"
    property string noticeAction: ""
    property var calls: []
    property var pendingClears: []
    property var clearedItems: []
    function clearItem(id) { pendingClears = pendingClears.concat([id]); clearedItems = clearedItems.concat(inbox.filter(function(x) { return x.transferId === id })); inbox = inbox.filter(function(x) { return x.transferId !== id }) }
    function clearAll() { var ids = inbox.map(function(x) { return x.transferId }); ids.forEach(function(id) { fixture.clearItem(id) }) }
    function undoClear() { inbox = clearedItems.concat(inbox); clearedItems = []; pendingClears = [] }
    property var inbox: [{transferId: "mixed", alias: "iPhone", receivedAt: "2026-11-09T12:56:00-05:00", text: "Here are the photos and notes for Saturday.\nMeet at the west entrance at 10:30.\n\nBring the printed route, water, and a jacket.\nThe longer trail takes about two hours.\nSee you there!", attachments: [
      {id: "photo", name: "Trail overlook.jpg", mime: "image/jpeg", size: 2612000},
      {id: "pdf", name: "Weekend walking route and meeting points.pdf", mime: "application/pdf", size: 19214}
    ]}, {transferId: "text", alias: "iPad", receivedAt: "2026-11-09T12:54:00-05:00", text: '<img src="https://example.invalid/tracker"> is shown as text.', attachments: []}]
    function refresh() { request("devices", {}); request("inbox", {}) }
    function request(command, fields) { calls.push({command: command, fields: fields}); return "fixture" }
    function send(alias, files) { calls.push({command: "send", alias: alias, files: files}) }
    function retryFailed() { send(failedSend.alias, failedSend.files) }
    function cancelActive() { request("cancel", {}) }
    function cancelPairing() { request("cancel", {}) }
    function restart() { request("restart", {}) }
  }
  Rectangle {
    id: card
    x: 8; y: 8
    width: window.width - 16
    height: Math.min(window.height - 16, panel.implicitHeight + 28)
    color: Commons.Color.popups.background
    border.color: Commons.Color.popups.border
    border.width: 1
    radius: Style.cornerRadius
    Plugin.PanelContent { id: panel; anchors.fill: parent; anchors.margins: 14; service: fixture; onDismiss: window.dismissals++ }
  }
  function assert(value, message) { if (!value) { console.error("FAIL " + message); Qt.exit(1) } }
  function findText(item, text) {
    if (item.text === text && typeof item.clicked === "function") return item
    for (var i = 0; i < item.children.length; i++) { var found = findText(item.children[i], text); if (found) return found }
    return null
  }
  function checkText(item) {
    if (item.textFormat !== undefined && String(item.text).indexOf("<img") >= 0) window.assert(item.textFormat === Text.PlainText, "Markup-shaped input renders as plain text")
    for (var i = 0; i < item.children.length; i++) checkText(item.children[i])
  }
  function find(item, name) {
    if (item.objectName === name) return item
    for (var i = 0; i < item.children.length; i++) { var found = find(item.children[i], name); if (found) return found }
    return null
  }
  Timer {
    id: scenes
    interval: 300
    running: true
    repeat: false
    onTriggered: {
      if (window.step >= window.frames.length) { console.log("PASS offscreen panel render and action assertions"); Qt.quit(); return }
      var name = window.frames[window.step]
      if (name === "send") {
        var wrapper = Qt.createComponent("Plugin/Panel.qml")
        window.assert(wrapper.status === Component.Ready || wrapper.errorString().indexOf("No PanelWindow backend loaded") >= 0, "Panel wrapper compiles up to offscreen platform boundary: " + wrapper.errorString())
        window.assert(fixture.calls.length === 0, "Opening does not read the clipboard")
        panel.focusCurrent()
        var send = window.find(panel, "sendClipboard")
        window.assert(send.activeFocus, "Send action receives initial focus")
        send.clicked()
        window.assert(fixture.calls[0].alias === "iPhone", "Send targets the selected device")
      }
      if (name === "chooser") window.find(panel, "targetChooser").open()
      if (name === "clear" || name === "undo") window.findText(panel, "Clear").clicked()
      if (name === "inbox") panel.showTab("receive")
      if (name === "devices") { fixture.undoClear(); panel.showTab("manage") }
      if (name === "empty") { fixture.devices = []; fixture.inbox = []; fixture.receiving = false; panel.showTab("send") }
      if (name === "busy") { fixture.devices = [{alias:"iPhone"}]; fixture.activeRequest = "send-1"; fixture.notice = "Waiting for device…"; panel.showTab("send") }
      if (name === "retry") { fixture.activeRequest = ""; fixture.failedSend = {alias:"Studio Mac", source:"clipboard", files:[]}; fixture.notice = "Studio Mac did not respond. Check that ClipSend is open and try again."; fixture.noticeKind = "error" }
      if (name === "retry-files") { fixture.failedSend = {alias:"Studio Mac", source:"files", files:["/tmp/review.pdf"]}; fixture.noticeAction = "send" }
      if (name === "pairing") { fixture.failedSend = null; fixture.notice = ""; fixture.pairRequest = "pair-1"; fixture.pairing = {png: Quickshell.env("CLIPSEND_FIXTURE_QR")}; panel.showTab("manage") }
      if (name === "approval") { fixture.failedSend = null; fixture.notice = ""; fixture.pairRequest = "pair-1"; fixture.approval = {requestId:"approval-1", name:'<img src="https://example.invalid/device">'}; panel.showTab("manage") }
      if (name === "narrow") { window.width = 300; window.height = 430; fixture.approval = null; fixture.pairing = null; fixture.pairRequest = ""; fixture.devices = [{alias:"A very long device alias that should elide"}]; fixture.selectedAlias = fixture.devices[0].alias; panel.confirmRestart = true }
      if (name === "narrow-send") { panel.confirmRestart = false; panel.showTab("send") }
      if (name === "light") { Style.styleOverrides = ({}); Commons.Color.accent = "#235bab"; Commons.Color.popups.background = "#fafafa"; Commons.Color.popups.text = "#202020"; Commons.Color.foreground = "#202020"; panel.showTab("send"); fixture.notice = "Sent"; fixture.noticeKind = "success" }
      capture.name = name
      capture.start()
      window.step++
    }
  }
  Timer {
    id: capture
    property string name
    interval: 120
    onTriggered: {
      window.checkText(panel)
      if (name === "send") {
        var send = window.find(panel, "sendClipboard")
        send.forceActiveFocus()
        input.keyClick(Qt.Key_Tab)
        window.assert(window.activeFocusItem.text === "Send files…", "Tab moves to the file action")
        input.keyClick(Qt.Key_Backtab)
        window.assert(send.activeFocus, "Shift Tab returns to clipboard")
        input.keyClick(Qt.Key_Return)
        window.assert(fixture.calls.length === 2, "Enter sends exactly once")
        input.keyClick(Qt.Key_Tab, Qt.ControlModifier)
        window.assert(panel.tab === "receive", "Ctrl Tab switches views")
        input.keyClick(Qt.Key_Backtab, Qt.ControlModifier | Qt.ShiftModifier)
        window.assert(panel.tab === "send", "Ctrl Shift Tab switches back")
        panel.showTab("send")
        input.wait(20)
        var chooser = window.find(panel, "targetChooser")
        chooser.open()
        input.wait(200)
        input.keyClick(Qt.Key_Down)
        input.keyClick(Qt.Key_Return)
        window.assert(fixture.selectedAlias === "iPad", "Keyboard chooser updates selected device")
        fixture.selectedAlias = "iPhone"
        window.assert(chooser.value === "iPhone", "External target changes remain reflected after selection")
        panel.focusCurrent()
      }
      if (name === "inbox") {
        var copy = window.findText(panel, "Copy image")
        copy.clicked()
        window.assert(fixture.calls[2].fields.itemId === "photo", "Image copy addresses the attachment")
        var show = window.findText(panel, "Show all")
        show.clicked()
        window.assert(window.findText(panel, "Show less") !== null, "Long text expands")
        window.findText(panel, "Show less").clicked()
        var scroll = window.find(panel, "contentScroll")
        scroll.contentY = scroll.contentHeight - scroll.height
        panel.focusCurrent()
        window.assert(scroll.contentY === 0, "Focus brings receive control back into view")
      }
      if (name === "busy") window.assert(window.activeFocusItem.objectName === "headerAction", "Busy send falls back to visible header action")
      if (name === "retry") {
        var retry = window.findText(panel, "Retry clipboard")
        retry.clicked()
        window.assert(fixture.calls[3].alias === "Studio Mac", "Retry honors the explicitly labelled original destination")
      }
      if (name === "clear") {
        window.assert(fixture.pendingClears[0] === "mixed", "Clear immediately offers undo")
        window.findText(panel, "Undo").clicked()
        window.assert(fixture.pendingClears.length === 0, "Undo restores cleared items")
        undoService.rawInbox = [{transferId:"a"}, {transferId:"b"}]
        undoService.clearItem("a")
        window.assert(undoService.inbox.length === 1, "Real service immediately hides a cleared item")
        undoService.clearItem("b")
        window.assert(undoService.inbox.length === 0, "Consecutive clears remain undoable")
        undoService.undoClear()
        window.assert(undoService.inbox.length === 2, "Real service undo restores both items")
        undoService.clearAll()
        window.assert(undoService.inbox.length === 0, "Clear all hides all visible deliveries")
        undoService.undoClear()
        window.assert(undoService.inbox.length === 2, "Undo Clear all restores the entire batch")
        undoService.clearItem("a")
        undoService.commitClears()
        window.assert(undoService.inbox.length === 2, "Unavailable backend restores item rather than silently losing it")
        undoService.receiveLine(JSON.stringify({version:1, type:"incoming", alias:"iPhone"}))
        window.assert(fakeShell.summoned === "clipsend.omarchy" && undoService.revealInbox, "Incoming delivery requests Inbox on the active output")
      }
      if (name === "devices") {
        var draft = window.find(panel, "pairName")
        draft.text = "Kitchen iPad"
        panel.showTab("send")
        panel.showTab("manage")
        window.assert(draft.text === "Kitchen iPad", "Navigation preserves pairing draft")
      }
      if (name === "retry-files") {
        window.findText(panel, "Retry same files").clicked()
        window.assert(fixture.calls[fixture.calls.length - 1].alias === "Studio Mac" && fixture.calls[fixture.calls.length - 1].files[0] === "/tmp/review.pdf", "File retry preserves destination and paths")
      }
      if (name === "narrow") {
        var restart = window.findText(panel, "Restart")
        restart.forceActiveFocus()
        var scroller = window.find(panel, "contentScroll")
        var pos = restart.mapToItem(scroller, 0, 0)
        window.assert(pos.y >= 0 && pos.y + restart.height <= scroller.height + 1, "Focused confirmation stays visible in narrow panel")
      }
      var frame = name
      card.grabToImage(function(result) {
        window.assert(result.saveToFile(Quickshell.env("CLIPSEND_RENDER_DIR") + "/" + frame + ".png"), "Screenshot saved")
        scenes.start()
      })
    }
  }
}
