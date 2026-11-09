pragma ComponentBehavior: Bound

import QtQuick
import QtQuick.Dialogs
import qs.Commons
import qs.Ui

BarWidget {
  id: root
  moduleName: "clipsend.omarchy"
  property bool opened: false
  property bool componentReady: false
  readonly property var clipService: bar && bar.shell ? bar.shell.serviceFor(moduleName) : null
  readonly property string executable: String(setting("executable", "clipsend") || "clipsend")
  property string fileTarget: ""
  property var pendingSave: null

  function initializeService() {
    if (componentReady && clipService && clipService.start(executable)) clipService.refresh()
  }
  function revealIncoming() {
    if (opened && clipService && clipService.revealInbox && panel.loadedContent) {
      panel.loadedContent.showTab("receive")
      clipService.revealInbox = false
    }
  }
  function open() { opened = true; panel.ensureContent(); Qt.callLater(revealIncoming); if (clipService) clipService.refresh() }
  function close() { opened = false }
  function toggle() { if (opened) close(); else open() }
  onClipServiceChanged: Qt.callLater(initializeService)
  onExecutableChanged: Qt.callLater(initializeService)
  Component.onCompleted: { componentReady = true; Qt.callLater(initializeService) }
  onOpenedChanged: if (opened) Qt.callLater(function() { if (panel.loadedContent) panel.loadedContent.focusCurrent() })

  implicitWidth: barSize
  implicitHeight: barSize
  BarIconButton {
    anchors.fill: parent
    bar: root.bar
    text: "󰆏"
    active: root.opened
    useActiveColor: false
    onPressed: root.toggle()
  }
  KeyboardPanel {
    id: panel
    anchorItem: root
    owner: root
    bar: root.bar
    open: root.opened
    focusTarget: panel.loadedContent
    onLoadedContentChanged: Qt.callLater(root.revealIncoming)
    contentWidth: panel.fittedContentWidth(Style.space(390))
    contentHeight: panel.fittedContentHeight(panel.loadedContentHeight, Style.space(640))
    content: PanelContent {
      anchors.fill: parent
      service: root.clipService
      onDismiss: root.close()
      onChooseFiles: function(alias) { root.fileTarget = alias; fileDialog.open() }
      onSaveAttachment: function(transferId, itemId) {
        root.pendingSave = { transferId: transferId, itemId: itemId }
        saveDialog.open()
      }
    }
  }
  FileDialog {
    id: fileDialog
    title: "Choose files to send"
    fileMode: FileDialog.OpenFiles
    onAccepted: {
      var paths = selectedFiles.map(function(url) { return decodeURIComponent(String(url).replace(/^file:\/\//, "")) })
      if (root.clipService) root.clipService.send(root.fileTarget, paths)
      root.fileTarget = ""
    }
    onRejected: root.fileTarget = ""
  }
  FileDialog {
    id: saveDialog
    title: "Save attachment"
    fileMode: FileDialog.SaveFile
    onAccepted: {
      if (!root.pendingSave) return
      var path = decodeURIComponent(String(selectedFile).replace(/^file:\/\//, ""))
      if (root.clipService) root.clipService.request("save", { transferId: root.pendingSave.transferId, itemId: root.pendingSave.itemId, path: path })
      root.pendingSave = null
    }
    onRejected: root.pendingSave = null
  }
}
