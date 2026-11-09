pragma ComponentBehavior: Bound

import QtQuick
import QtQuick.Controls
import QtQuick.Dialogs
import qs.Commons
import qs.Commons as Commons
import qs.Ui

BarWidget {
  id: root
  moduleName: "clipsend.omarchy"

  property bool opened: false
  property bool componentReady: false
  property string tab: "send"
  readonly property var clipService: bar && bar.shell ? bar.shell.serviceFor(moduleName) : null
  readonly property var devices: clipService ? clipService.devices : []
  readonly property var inbox: clipService ? clipService.inbox : []
  readonly property string selectedAlias: clipService ? clipService.selectedAlias : ""
  readonly property string activeRequest: clipService ? clipService.activeRequest : ""
  readonly property var pairing: clipService ? clipService.pairing : null
  readonly property var approval: clipService ? clipService.approval : null
  readonly property string notice: clipService ? clipService.notice : "ClipSend service is unavailable"
  readonly property bool receiving: clipService ? clipService.receiving : false
  readonly property string receiveEndpoint: clipService ? clipService.receiveEndpoint : ""
  readonly property var failedSend: clipService ? clipService.failedSend : null
  readonly property string executable: String(setting("executable", "clipsend") || "clipsend")
  readonly property color fg: bar ? bar.barForeground : Commons.Color.foreground
  readonly property color muted: Qt.darker(fg, 1.5)
  readonly property color surface: Commons.Color.popups.background

  function showTab(name) {
    tab = name
    Qt.callLater(function() {
      if (panel.loadedContent) panel.loadedContent.focusTab(name)
    })
  }

  function initializeService() {
    if (componentReady && clipService && clipService.start(executable)) clipService.refresh()
  }
  function open() { opened = true; if (clipService) clipService.refresh() }
  function close() { opened = false }
  function toggle() { if (opened) close(); else open() }

  function send(files) { if (clipService) clipService.send(selectedAlias, files) }
  function request(command, fields) { return clipService ? clipService.request(command, fields) : "" }
  function cancelActive() { if (clipService) clipService.cancelActive() }
  function cancelPairing() { if (clipService) clipService.cancelPairing() }

  function keepFocusedItemVisible(item) {
    if (panel.loadedContent) panel.loadedContent.keepVisible(item)
  }

  onClipServiceChanged: Qt.callLater(initializeService)
  onExecutableChanged: Qt.callLater(initializeService)
  Component.onCompleted: { componentReady = true; Qt.callLater(initializeService) }

  implicitWidth: barSize
  implicitHeight: barSize
  BarIconButton {
    anchors.fill: parent
    bar: root.bar
    text: "󰆏"
    active: root.opened || root.receiving
    onPressed: root.toggle()
  }

  KeyboardPanel {
    id: panel
    anchorItem: root
    owner: root
    bar: root.bar
    open: root.opened
    focusTarget: panel.loadedContent
    contentWidth: panel.fittedContentWidth(Style.space(390))
    contentHeight: panel.fittedContentHeight(panel.loadedContentHeight, Style.space(720))
    content: Item {
      id: panelContent
      function focusTab(name) {
        if (name === "send" && sendClipboard.enabled) sendClipboard.forceActiveFocus()
        else if (name === "receive") receiveToggle.forceActiveFocus()
        else if (name === "manage") pairName.forceActiveFocus()
      }
      function keepVisible(item) {
        if (!item || !panelScroll.interactive) return
        var top = item.mapToItem(panelScroll.contentItem, 0, 0).y
        if (top < panelScroll.contentY) panelScroll.contentY = Math.max(0, top - Style.space(8))
        else if (top + item.height > panelScroll.contentY + panelScroll.height)
          panelScroll.contentY = Math.min(panelScroll.contentHeight - panelScroll.height, top + item.height - panelScroll.height + Style.space(8))
      }
      Keys.onEscapePressed: root.close()
      implicitHeight: Math.min(body.implicitHeight, Style.space(620))
      anchors.fill: parent
      Flickable {
        id: panelScroll
        anchors.fill: parent
        contentWidth: width
        contentHeight: body.implicitHeight
        clip: true
        boundsBehavior: Flickable.StopAtBounds
        interactive: contentHeight > height
      Column {
        id: body
        width: panelScroll.width
        spacing: Style.space(12)

        Row {
          width: parent.width
          spacing: Style.space(6)
          Repeater {
            model: [{ id: "send", label: "Send" }, { id: "receive", label: "Inbox" }, { id: "manage", label: "Devices" }]
            delegate: Button {
              required property var modelData
              text: modelData.label
              selected: root.tab === modelData.id
              focusable: true
              onClicked: root.showTab(modelData.id)
            }
          }
        }

        Text {
          visible: root.notice !== ""
          width: parent.width
          text: root.notice
          textFormat: Text.PlainText
          color: root.muted
          wrapMode: Text.Wrap
          font.pixelSize: Style.font.caption
        }

        Column {
          visible: root.tab === "send"
          width: parent.width
          spacing: Style.space(10)
          Text { text: "Send to"; textFormat: Text.PlainText; color: root.fg; font.pixelSize: Style.font.body }
          Text { visible: root.devices.length === 0; text: "No paired devices. Add one under Devices."; textFormat: Text.PlainText; color: root.muted; wrapMode: Text.Wrap; width: parent.width }
          Flow {
            width: parent.width
            spacing: Style.space(5)
            Repeater {
              model: root.devices
              delegate: Button {
                required property var modelData
                text: String(modelData.alias || modelData.name || "Device")
                selected: root.selectedAlias === String(modelData.alias || "")
                focusable: true
                onClicked: if (root.clipService) root.clipService.selectedAlias = String(modelData.alias || "")
              }
            }
          }
          Row {
            spacing: Style.space(8)
            Button { id: sendClipboard; text: "Send clipboard"; enabled: !!root.selectedAlias && !root.activeRequest; selected: true; focusable: true; onClicked: root.send([]) }
            Button { text: "Choose files"; enabled: !!root.selectedAlias && !root.activeRequest; focusable: true; onActiveFocusChanged: if (activeFocus) root.keepFocusedItemVisible(this); onClicked: fileDialog.open() }
          }
          Button { visible: !!root.activeRequest; text: "Cancel transfer"; focusable: true; onActiveFocusChanged: if (activeFocus) root.keepFocusedItemVisible(this); onClicked: root.cancelActive() }
          Button { visible: !!root.failedSend && root.failedSend.source === "clipboard"; text: "Retry from clipboard"; enabled: !!root.selectedAlias; focusable: true; onActiveFocusChanged: if (activeFocus) root.keepFocusedItemVisible(this); onClicked: if (root.clipService) root.clipService.retryFailed() }
          Button { visible: !!root.failedSend && root.failedSend.source === "files"; text: "Retry same files"; enabled: !!root.selectedAlias; focusable: true; onActiveFocusChanged: if (activeFocus) root.keepFocusedItemVisible(this); onClicked: if (root.clipService) root.clipService.retryFailed() }
          DropArea {
            width: parent.width
            height: Style.space(66)
            onEntered: function(drag) { drag.accepted = drag.hasUrls && !root.activeRequest }
            onDropped: function(drop) {
              let paths = []
              for (let i = 0; i < drop.urls.length; i++) {
                let path = String(drop.urls[i])
                if (path.indexOf("file://") === 0) paths.push(decodeURIComponent(path.replace(/^file:\/\//, "")))
              }
              if (paths.length && !root.activeRequest) root.send(paths)
            }
            Rectangle {
              anchors.fill: parent
              radius: Style.cornerRadius
              color: "transparent"
              border.color: root.muted
              border.width: 1
              Text { anchors.centerIn: parent; text: "Drop files here"; textFormat: Text.PlainText; color: root.muted }
            }
          }
        }

        Column {
          visible: root.tab === "receive"
          width: parent.width
          spacing: Style.space(10)
          Toggle { id: receiveToggle; width: parent.width; label: "Receive on this computer"; checked: root.receiving; onClicked: root.request("receive", { enabled: !root.receiving }) }
          Repeater {
            model: root.inbox
            delegate: Rectangle {
              id: transferCard
              required property var modelData
              width: body.width
              height: inboxCard.implicitHeight + Style.space(18)
              radius: Style.cornerRadius
              color: Qt.darker(root.surface, 1.04)
              Column {
                id: inboxCard
                anchors.left: parent.left; anchors.right: parent.right; anchors.top: parent.top
                anchors.margins: Style.space(9)
                spacing: Style.space(6)
                Text { text: String(transferCard.modelData.alias || "Device"); textFormat: Text.PlainText; color: root.fg; font.bold: true }
                Text { visible: !!transferCard.modelData.text; width: parent.width; text: String(transferCard.modelData.text || ""); textFormat: Text.PlainText; wrapMode: Text.Wrap; maximumLineCount: 5; elide: Text.ElideRight; color: root.fg }
                Repeater {
                  model: transferCard.modelData.attachments || []
                  delegate: Row {
                    id: attachmentRow
                    required property var modelData
                    width: parent.width
                    spacing: Style.space(8)
                    Text { width: Math.max(30, parent.width - saveButton.width - (imageCopy.visible ? imageCopy.width + parent.spacing : 0) - parent.spacing); text: String(attachmentRow.modelData.name || "Attachment"); textFormat: Text.PlainText; color: root.muted; elide: Text.ElideMiddle }
                    Button {
                      id: imageCopy
                      visible: attachmentRow.modelData.mime === "image/png" || attachmentRow.modelData.mime === "image/jpeg"
                      text: "Copy image"
                      focusable: true
                      onActiveFocusChanged: if (activeFocus) root.keepFocusedItemVisible(this)
                      onClicked: root.request("copy", { transferId: transferCard.modelData.transferId, itemId: attachmentRow.modelData.id })
                    }
                    Button {
                      id: saveButton
                      text: "Save…"
                      focusable: true
                      onActiveFocusChanged: if (activeFocus) root.keepFocusedItemVisible(this)
                      onClicked: {
                        root.pendingSave = { transferId: transferCard.modelData.transferId, itemId: attachmentRow.modelData.id }
                        saveDialog.open()
                      }
                    }
                  }
                }
                Row {
                  spacing: Style.space(6)
                  Button { text: "Copy text"; visible: !!transferCard.modelData.text; focusable: true; onActiveFocusChanged: if (activeFocus) root.keepFocusedItemVisible(this); onClicked: root.request("copy", { transferId: transferCard.modelData.transferId }) }
                  Button { text: "Clear"; focusable: true; onActiveFocusChanged: if (activeFocus) root.keepFocusedItemVisible(this); onClicked: root.request("clear", { transferId: transferCard.modelData.transferId }) }
                }
              }
            }
          }
          Text { visible: root.inbox.length === 0; text: "Nothing received yet"; textFormat: Text.PlainText; color: root.muted }
        }

        Column {
          visible: root.tab === "manage"
          width: parent.width
          spacing: Style.space(9)
          Button { text: "Restart connection"; focusable: true; onClicked: restartDialog.open() }
          Row {
            width: parent.width
            spacing: Style.space(6)
            TextField { id: pairName; width: parent.width - pairButton.width - Style.space(6); placeholderText: "New device alias"; text: root.pairAlias; onTextChanged: root.pairAlias = text }
            Button { id: pairButton; text: "Pair"; selected: true; enabled: root.pairAlias.trim().length > 0 && !(root.clipService && root.clipService.pairRequest); focusable: true; onActiveFocusChanged: if (activeFocus) root.keepFocusedItemVisible(this); onClicked: if (root.clipService) { root.clipService.pairing = null; root.clipService.pairRequest = root.request("pair", { alias: root.pairAlias.trim() }) } }
          }
          Repeater {
            model: root.devices
            delegate: Row {
              id: deviceRow
              required property var modelData
              width: parent.width
              spacing: Style.space(6)
              Text { width: parent.width - rename.width - remove.width - Style.space(12); text: String(deviceRow.modelData.alias || deviceRow.modelData.name || "Device"); textFormat: Text.PlainText; color: root.fg; elide: Text.ElideRight; anchors.verticalCenter: parent.verticalCenter }
              Button { id: rename; text: "Rename"; focusable: true; onActiveFocusChanged: if (activeFocus) root.keepFocusedItemVisible(this); onClicked: { root.renameTarget = String(deviceRow.modelData.alias || ""); root.renameAlias = root.renameTarget; renameDialog.open() } }
              Button { id: remove; text: "Unpair"; focusable: true; onActiveFocusChanged: if (activeFocus) root.keepFocusedItemVisible(this); onClicked: { root.unpairAlias = String(deviceRow.modelData.alias || ""); unpairDialog.open() } }
            }
          }
          Rectangle {
            visible: !!root.pairing
            width: parent.width; height: visible ? Style.space(220) : 0
            color: "transparent"
            Column {
              anchors.centerIn: parent; spacing: Style.space(6)
              Image { width: Style.space(176); height: Style.space(176); fillMode: Image.PreserveAspectFit; source: root.pairing ? "data:image/png;base64," + root.pairing.png : "" }
              Text { text: "Scan with ClipSend, then approve here"; textFormat: Text.PlainText; color: root.muted; wrapMode: Text.Wrap; width: Style.space(220); horizontalAlignment: Text.AlignHCenter }
              Button { text: "Cancel pairing"; focusable: true; onClicked: root.cancelPairing() }
            }
          }
          Rectangle {
            visible: !!root.approval
            width: parent.width; height: visible ? Style.space(92) : 0
            color: Qt.darker(root.surface, 1.04); radius: Style.cornerRadius
            Column {
              anchors.centerIn: parent; spacing: Style.space(6)
              Text { text: "Allow " + String(root.approval ? root.approval.name : "device") + " to pair?"; textFormat: Text.PlainText; color: root.fg }
              Row { spacing: Style.space(6)
                Button { text: "Approve"; selected: true; focusable: true; onClicked: root.request("approve", { requestId: root.approval.requestId, allow: true }) }
                Button { text: "Deny"; focusable: true; onClicked: root.request("approve", { requestId: root.approval.requestId, allow: false }) }
              }
            }
          }
        }
        }
      }
    }
  }

  property var pendingSave: null
  property string renameAlias: ""
  property string renameTarget: ""
  property string pairAlias: ""
  property string unpairAlias: ""
  Dialog {
    id: unpairDialog
    implicitWidth: Style.space(320)
    width: implicitWidth
    title: "Unpair device?"
    modal: true
    standardButtons: Dialog.Ok | Dialog.Cancel
    contentItem: Text {
      text: "Remove “" + root.unpairAlias + "”? Pair it again before sending or receiving."
      textFormat: Text.PlainText
      color: root.fg
      wrapMode: Text.Wrap
      padding: Style.space(12)
    }
    onAccepted: root.request("unpair", { alias: root.unpairAlias })
  }
  Dialog {
    id: restartDialog
    implicitWidth: Style.space(340)
    width: Style.space(340)
    title: "Restart ClipSend connection?"
    modal: true
    standardButtons: Dialog.Ok | Dialog.Cancel
    contentItem: Text {
      text: root.clipService && (root.clipService.receiving || root.clipService.activeRequest || root.clipService.pairRequest || root.clipService.inbox.length > 0 || root.clipService.failedSend)
        ? "Restarting stops receiving, cancels active work, and clears the temporary inbox and retry state. Nothing will be resent automatically."
        : "Restart ClipSend. Receiving will be off until you turn it on again."
      textFormat: Text.PlainText
      color: root.fg
      wrapMode: Text.Wrap
      width: Style.space(300)
      padding: Style.space(16)
    }
    onAccepted: if (root.clipService) root.clipService.restart()
  }
  FileDialog {
    id: fileDialog
    title: "Choose files to send"
    fileMode: FileDialog.OpenFiles
    onAccepted: {
      var paths = selectedFiles.map(function(url) { return decodeURIComponent(String(url).replace(/^file:\/\//, "")) })
      root.send(paths)
    }
  }
  FileDialog {
    id: saveDialog
    title: "Save attachment"
    fileMode: FileDialog.SaveFile
    onAccepted: {
      if (!root.pendingSave) return
      var path = decodeURIComponent(String(selectedFile).replace(/^file:\/\//, ""))
      root.request("save", { transferId: root.pendingSave.transferId, itemId: root.pendingSave.itemId, path: path })
      root.pendingSave = null
    }
  }
  Dialog {
    id: renameDialog
    implicitWidth: Style.space(300)
    width: Style.space(300)
    title: "Rename device"
    modal: true
    standardButtons: Dialog.Ok | Dialog.Cancel
    contentItem: TextField { text: root.renameAlias; onTextChanged: root.renameAlias = text }
    onAccepted: {
      if (root.renameTarget && root.renameAlias.trim() && root.renameAlias.trim() !== root.renameTarget)
        root.request("rename", { alias: root.renameTarget, newAlias: root.renameAlias.trim() })
    }
  }
}
