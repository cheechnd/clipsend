pragma ComponentBehavior: Bound

import QtQuick
import QtQuick.Controls as Controls
import QtQuick.Layouts
import qs.Commons
import qs.Commons as Commons
import qs.Ui

FocusScope {
  id: root
  property var service: null
  property string tab: "send"
  property string editAlias: ""
  property string removeAlias: ""
  property bool confirmRestart: false
  property bool choosingTarget: false
  property Item headerAction: null
  readonly property var devices: service ? service.devices : []
  readonly property var inbox: service ? service.inbox : []
  readonly property string selectedAlias: service ? service.selectedAlias : ""
  readonly property bool sending: !!(service && service.activeRequest)
  readonly property bool receiving: !!(service && service.receiving)
  readonly property bool pairingBusy: !!(service && service.pairRequest)
  readonly property var pairing: service ? service.pairing : null
  readonly property var approval: service ? service.approval : null
  readonly property var failedSend: service ? service.failedSend : null
  readonly property string notice: service ? service.notice : strings.t("unavailable")
  readonly property bool showNotice: notice !== "" && ["Connected", "Receiving is on", "Receiving is off"].indexOf(notice) < 0
  readonly property color fg: Commons.Color.popups.text
  readonly property color muted: Qt.alpha(fg, 0.72)
  signal dismiss()
  signal chooseFiles(string alias)
  signal saveAttachment(string transferId, string itemId)
  implicitHeight: Math.min(Style.space(590), header.height + Style.space(16) + body.implicitHeight + (status.visible ? status.height + Style.space(10) : 0))

  Commons.Translations { id: strings; directory: Qt.resolvedUrl("i18n") }
  function pending(command) {
    if (!service) return false
    var actions = service.requestActions || {}
    return Object.keys(actions).some(function(key) { return actions[key] === command })
  }
  function request(command, fields) { return service ? service.request(command, fields) : "" }
  function showTab(name) {
    choosingTarget = false
    tab = name
    scroll.contentY = 0
    Qt.callLater(focusCurrent)
  }
  function focusCurrent() {
    var item
    if (tab === "send") item = sendClipboard.enabled ? sendClipboard : root.devices.length === 0 ? addFirst : headerAction
    else if (tab === "receive") item = receiveToggle.enabled ? receiveToggle : headerAction
    else item = pairName.enabled ? pairName : headerAction
    if (item) { item.forceActiveFocus(); keepVisible(item) }
  }
  function keepVisible(item) {
    if (!item || !scroll.interactive) return
    var ancestor = item.parent
    while (ancestor && ancestor !== scroll.contentItem) ancestor = ancestor.parent
    if (!ancestor) return
    var top = item.mapToItem(scroll.contentItem, 0, 0).y
    if (top < scroll.contentY) scroll.contentY = Math.max(0, top - Style.space(8))
    else if (top + item.height > scroll.contentY + scroll.height)
      scroll.contentY = Math.min(scroll.contentHeight - scroll.height, top + item.height - scroll.height + Style.space(8))
  }
  function beginPair() {
    if (!service || pairingBusy || !pairName.text.trim()) return
    service.pairing = null
    service.pairRequest = request("pair", { alias: pairName.text.trim() })
  }
  function fileSize(size) {
    var n = Math.max(0, Number(size) || 0)
    return n < 1024 ? Commons.I18n.formatNumber(n) + " B" : n < 1048576 ? Commons.I18n.formatNumber(n / 1024, 1) + " KB" : Commons.I18n.formatNumber(n / 1048576, 1) + " MB"
  }
  Keys.onEscapePressed: {
    if (choosingTarget) { choosingTarget = false; target.forceActiveFocus() }
    else if (editAlias || removeAlias || confirmRestart) { editAlias = ""; removeAlias = ""; confirmRestart = false; focusCurrent() }
    else if (tab !== "send") showTab("send")
    else dismiss()
  }
  Keys.onPressed: function(event) {
    if ((event.modifiers & Qt.ControlModifier) && (event.key === Qt.Key_Tab || event.key === Qt.Key_Backtab)) {
      var tabs = ["send", "receive", "manage"]
      showTab(tabs[(tabs.indexOf(tab) + ((event.modifiers & Qt.ShiftModifier) || event.key === Qt.Key_Backtab ? 2 : 1)) % 3])
      event.accepted = true
    }
  }

  component Action: Button {
    focusable: true
    foreground: root.fg
    opacity: enabled ? 1 : 0.45
    implicitHeight: Style.space(34)
    Accessible.role: Accessible.Button
    Accessible.name: text
    Accessible.onPressAction: if (enabled) clicked()
    onActiveFocusChanged: if (activeFocus) root.keepVisible(this)
  }
  component Hint: Label { color: root.muted; font.pixelSize: Style.font.bodySmall }
  component Rule: PanelSeparator { foreground: root.fg }
  component Section: PanelSectionHeader { foreground: root.fg }
  component Glyph: Label { font.pixelSize: Style.font.icon; wrapMode: Text.NoWrap }
  component IconAction: PanelActionButton {
    focusable: true
    foreground: root.fg
    size: Style.space(32)
    Accessible.role: Accessible.Button
    Accessible.name: tooltipText
    Accessible.onPressAction: if (enabled) clicked()
    onActiveFocusChanged: if (activeFocus) root.keepVisible(this)
  }
  // Button supplies shell hover, press and keyboard focus; the row supplies
  // the same icon / label / quiet value / chevron rhythm as native panels.
  component ActionRow: Action {
    id: row
    property string title: ""
    property string glyph: ""
    property string detail: ""
    property string suffix: "›"
    implicitHeight: Style.space(40)
    Accessible.name: title + (detail ? ", " + detail : "")
    RowLayout {
      anchors.fill: parent
      anchors.leftMargin: Style.space(10)
      anchors.rightMargin: Style.space(10)
      spacing: Style.space(10)
      Glyph { visible: row.glyph !== ""; text: row.glyph; Layout.preferredWidth: Style.space(20); horizontalAlignment: Text.AlignHCenter }
      Label { text: row.title; Layout.fillWidth: true; maximumLineCount: 1; elide: Text.ElideRight }
      Hint { visible: row.detail !== ""; text: row.detail; Layout.maximumWidth: row.width * 0.48; maximumLineCount: 1; elide: Text.ElideRight }
      Glyph { visible: row.suffix !== ""; text: row.suffix; font.pixelSize: Style.font.body; color: root.muted }
    }
  }

  Column {
    id: header
    width: parent.width
    spacing: Style.space(16)
    PanelHero {
      width: parent.width
      foreground: root.fg
      title: "ClipSend"
      meta: root.tab === "send" ? strings.t(root.receiving ? "receivingOn" : "receivingOff") : strings.t(root.tab === "receive" ? "inbox" : "devices")
      iconComponent: Glyph { text: "󰆏"; font.pixelSize: Style.font.display }
      trailingControl: IconAction {
        id: backButton
        objectName: "headerAction"
        iconText: root.tab === "send" ? "󰑓" : "󰁍"
        tooltipText: strings.t(root.tab === "send" ? "refresh" : "backToSend")
        enabled: root.tab !== "send" || !!root.service
        Component.onCompleted: root.headerAction = this
        onClicked: if (root.tab === "send") root.service.refresh(); else root.showTab("send")
      }
    }
    Rule {}
  }

  Flickable {
    id: scroll
    objectName: "contentScroll"
    anchors.top: header.bottom
    anchors.topMargin: Style.space(16)
    anchors.bottom: status.visible ? status.top : parent.bottom
    anchors.bottomMargin: status.visible ? Style.space(10) : 0
    width: parent.width
    contentWidth: width
    contentHeight: body.implicitHeight
    clip: true
    boundsBehavior: Flickable.StopAtBounds
    interactive: contentHeight > height
    Controls.ScrollBar.vertical: Controls.ScrollBar { policy: scroll.interactive ? Controls.ScrollBar.AlwaysOn : Controls.ScrollBar.AlwaysOff }
    Column {
      id: body
      width: scroll.width - (scroll.interactive ? Style.space(10) : 0)
      spacing: Style.space(12)

      Column {
        visible: root.tab === "send"
        width: parent.width
        spacing: Style.space(12)
        Column {
          visible: root.devices.length === 0
          width: parent.width
          spacing: Style.space(8)
          Label { text: strings.t("firstTitle"); font.bold: true }
          Hint { width: parent.width; text: strings.t("firstHint") }
          Action { id: addFirst; text: strings.t("pairDevice"); selected: true; onClicked: root.showTab("manage") }
        }
        Column {
          visible: root.devices.length > 0
          width: parent.width
          spacing: Style.space(12)
          Column {
            width: parent.width
            spacing: Style.space(2)
            ActionRow {
              id: target
              objectName: "targetChooser"
              readonly property string value: root.selectedAlias
              function open() {
                root.choosingTarget = true
                Qt.callLater(function() {
                  for (var i = 0; i < deviceChoices.count; i++) {
                    if (deviceChoices.itemAt(i).alias === root.selectedAlias) {
                      deviceChoices.itemAt(i).forceActiveFocus()
                      return
                    }
                  }
                  if (deviceChoices.count) deviceChoices.itemAt(0).forceActiveFocus()
                })
              }
              width: parent.width
              title: strings.t("sendTo")
              detail: root.selectedAlias
              glyph: "󰌷"
              suffix: root.choosingTarget ? "⌃" : "›"
              enabled: !root.sending
              onClicked: if (root.choosingTarget) root.choosingTarget = false; else open()
            }
            Column {
              visible: root.choosingTarget
              width: parent.width
              spacing: Style.space(2)
              Repeater {
                id: deviceChoices
                model: root.devices
                delegate: ActionRow {
                  required property var modelData
                  required property int index
                  readonly property string alias: String(modelData.alias || "")
                  width: parent.width
                  title: alias
                  glyph: "󰌷"
                  selected: alias === root.selectedAlias
                  suffix: selected ? "" : ""
                  enabled: !root.sending
                  Accessible.role: Accessible.RadioButton
                  Accessible.checkable: true
                  Accessible.checked: selected
                  onClicked: { if (root.service) root.service.selectedAlias = alias; root.choosingTarget = false; target.forceActiveFocus() }
                  Keys.onDownPressed: deviceChoices.itemAt(Math.min(deviceChoices.count - 1, index + 1)).forceActiveFocus()
                  Keys.onUpPressed: deviceChoices.itemAt(Math.max(0, index - 1)).forceActiveFocus()
                }
              }
            }
          }
          Action {
            id: sendClipboard
            objectName: "sendClipboard"
            width: parent.width
            implicitHeight: Style.space(44)
            iconText: ""
            text: strings.t("sendClipboard")
            selected: true
            enabled: !!root.selectedAlias && !root.sending
            onClicked: root.service.send(root.selectedAlias, [])
          }
          DropArea {
            id: dropArea
            width: parent.width
            height: Style.space(40)
            onEntered: function(drag) { drag.accepted = drag.hasUrls && !!root.selectedAlias && !root.sending }
            onDropped: function(drop) {
              var paths = []
              for (var i = 0; i < drop.urls.length; i++) {
                var path = String(drop.urls[i])
                if (path.indexOf("file://") === 0) paths.push(decodeURIComponent(path.replace(/^file:\/\//, "")))
              }
              if (paths.length && root.selectedAlias && !root.sending) root.service.send(root.selectedAlias, paths)
            }
            Rectangle {
              anchors.fill: parent
              radius: Style.cornerRadius
              color: dropArea.containsDrag ? Style.selectedFillFor(root.fg, Commons.Color.accent) : "transparent"
            }
            RowLayout {
              anchors.fill: parent
              spacing: Style.space(8)
              Action {
                text: strings.t("chooseFiles")
                iconText: "󰈔"
                leftAlign: true
                Layout.fillWidth: true
                enabled: !!root.selectedAlias && !root.sending
                onClicked: root.chooseFiles(root.selectedAlias)
              }
              Hint {
                Layout.rightMargin: Style.space(10)
                text: strings.t("dropFiles")
                font.pixelSize: Style.font.caption
                visible: dropArea.width >= Style.space(330)
              }
            }
          }
        }
        Column {
          visible: !!root.failedSend && !root.sending
          width: parent.width
          spacing: Style.space(6)
          Hint { width: parent.width; text: strings.t("retryTarget").arg(root.failedSend ? root.failedSend.alias : "") }
          Hint { width: parent.width; visible: !!root.failedSend && root.failedSend.source === "clipboard"; text: strings.t("retryClipboardHint") }
          Action { text: root.failedSend && root.failedSend.source === "files" ? strings.t("retryFiles") : strings.t("retryClipboard"); bordered: true; enabled: !!root.service; onClicked: root.service.retryFailed() }
        }
        Rule {}
        Column {
          width: parent.width
          spacing: Style.space(2)
          ActionRow {
            id: inboxLink
            objectName: "inboxLink"
            width: parent.width
            title: strings.t("inbox")
            glyph: "󰇚"
            detail: root.inbox.length ? Commons.I18n.formatNumber(root.inbox.length) : ""
            onClicked: root.showTab("receive")
          }
          ActionRow {
            width: parent.width
            title: strings.t("devices")
            glyph: "󰌷"
            detail: Commons.I18n.formatNumber(root.devices.length)
            onClicked: root.showTab("manage")
          }
        }
      }

      Column {
        visible: root.tab === "receive"
        width: parent.width
        spacing: Style.space(12)
        Toggle {
          id: receiveToggle
          width: parent.width
          label: strings.t("receiveHere")
          description: ""
          checked: root.receiving
          enabled: !!root.service && !root.pending("receive")
          opacity: enabled ? 1 : 0.45
          titleSize: Style.font.body
          Accessible.role: Accessible.CheckBox
          Accessible.name: label
          Accessible.checkable: true
          Accessible.checked: checked
          Accessible.onToggleAction: if (enabled) clicked()
          onActiveFocusChanged: if (activeFocus) root.keepVisible(this)
          onClicked: root.request("receive", { enabled: !root.receiving })
        }
        Hint { visible: root.inbox.length === 0; width: parent.width; text: root.receiving ? strings.t("emptyInboxOn") : strings.t("emptyInboxOff") }
        Row {
          visible: root.inbox.length > 0
          width: parent.width
          spacing: Style.space(12)
          Hint { width: parent.width - clearAll.width - parent.spacing; text: strings.t("inboxHint"); anchors.verticalCenter: parent.verticalCenter }
          Action { id: clearAll; text: strings.t("clearAll"); fontSize: Style.font.bodySmall; onClicked: root.service.clearAll() }
        }
        Repeater {
          model: root.inbox
          delegate: Column {
            id: transfer
            required property var modelData
            property bool expanded: false
            width: parent.width
            spacing: Style.space(8)
            Rule {}
            Row {
              width: parent.width
              spacing: Style.space(6)
              Column {
                width: parent.width - clear.width - parent.spacing
                spacing: Style.space(3)
                Label { width: parent.width; text: String(transfer.modelData.alias || "Device"); elide: Text.ElideRight; maximumLineCount: 1; font.bold: true }
                Hint { visible: !!transfer.modelData.receivedAt; text: visible ? new Date(transfer.modelData.receivedAt).toLocaleTimeString(Qt.locale(Commons.I18n.formatLocale), "hh:mm:ss") : "" }
              }
              Action { id: clear; text: strings.t("clear"); fontSize: Style.font.bodySmall; onClicked: root.service.clearItem(String(transfer.modelData.transferId)) }
            }
            Label {
              id: receivedText
              visible: !!transfer.modelData.text
              width: parent.width
              text: String(transfer.modelData.text || "")
              maximumLineCount: transfer.expanded ? 100000 : 4
              elide: Text.ElideRight
            }
            Flow {
              visible: !!transfer.modelData.text
              width: parent.width
              spacing: Style.space(6)
              Action { text: strings.t("copyText"); iconText: "󰆏"; bordered: true; enabled: !root.pending("copy"); onClicked: root.request("copy", { transferId: transfer.modelData.transferId }) }
              Action { visible: receivedText.truncated || transfer.expanded; text: transfer.expanded ? strings.t("showLess") : strings.t("showAll"); onClicked: transfer.expanded = !transfer.expanded }
            }
            Repeater {
              model: transfer.modelData.attachments || []
              delegate: Rectangle {
                id: attachment
                required property var modelData
                readonly property bool copyable: modelData.mime === "image/png" || modelData.mime === "image/jpeg"
                width: transfer.width
                implicitHeight: attachmentBody.implicitHeight + Style.space(16)
                radius: Style.cornerRadius
                color: Qt.alpha(root.fg, 0.045)
                Column {
                  id: attachmentBody
                  anchors { left: parent.left; right: parent.right; top: parent.top; margins: Style.space(8) }
                  spacing: Style.space(4)
                  Label { width: parent.width; text: String(attachment.modelData.name || "Attachment"); elide: Text.ElideMiddle; maximumLineCount: 1 }
                  Hint { text: strings.t(String(attachment.modelData.mime || "").indexOf("image/") === 0 || /\.(heic|heif)$/i.test(String(attachment.modelData.name || "")) ? "imageSize" : "fileSize").arg(root.fileSize(attachment.modelData.size)); width: parent.width }
                  Flow {
                    width: parent.width
                    spacing: Style.space(6)
                    Action { visible: attachment.copyable; text: strings.t("copyImage"); iconText: "󰆏"; bordered: true; enabled: !root.pending("copy"); onClicked: root.request("copy", { transferId: transfer.modelData.transferId, itemId: attachment.modelData.id }) }
                    Action { text: strings.t("open"); iconText: "󰏌"; bordered: true; enabled: !root.pending("open"); onClicked: root.request("open", { transferId: transfer.modelData.transferId, itemId: attachment.modelData.id }) }
                    Action { text: strings.t("save"); iconText: "󰇚"; bordered: true; enabled: !root.pending("save"); onClicked: root.saveAttachment(transfer.modelData.transferId, attachment.modelData.id) }
                  }
                }
              }
            }
          }
        }
      }

      Column {
        visible: root.tab === "manage"
        width: parent.width
        spacing: Style.space(12)
        Section { text: strings.t("pairDevice").toUpperCase() }
        Row {
          width: parent.width
          spacing: Style.space(6)
          TextField {
            id: pairName
            objectName: "pairName"
            width: parent.width - pairButton.width - parent.spacing
            placeholderText: strings.t("aliasPlaceholder")
            maximumLength: 128
            enabled: !root.pairingBusy
            Accessible.name: strings.t("aliasPlaceholder")
            onActiveFocusChanged: if (activeFocus) root.keepVisible(this)
            onAccepted: root.beginPair()
          }
          Action { id: pairButton; text: strings.t("pair"); selected: true; enabled: !!root.service && pairName.text.trim().length > 0 && !root.pairingBusy; onClicked: root.beginPair() }
        }
        Column {
          visible: root.pairingBusy
          width: parent.width
          spacing: Style.space(8)
          Hint { width: parent.width; text: root.approval ? strings.t("confirmDevice") : root.pairing ? strings.t("scanHint") : strings.t("preparingPair") }
          Image {
            visible: !!root.pairing && !root.approval
            width: Style.space(176); height: width
            anchors.horizontalCenter: parent.horizontalCenter
            source: root.pairing ? "data:image/png;base64," + root.pairing.png : ""
            fillMode: Image.PreserveAspectFit
            Accessible.name: strings.t("pairCode")
          }
          Action { text: strings.t("cancelPair"); enabled: !!root.service && !root.service.pairCancelling; onClicked: root.service.cancelPairing() }
        }
        Column {
          visible: !!root.approval
          width: parent.width
          spacing: Style.space(6)
          Label { width: parent.width; text: strings.t("allowDevice").arg(root.approval ? root.approval.name : ""); font.bold: true }
          Flow { width: parent.width; spacing: Style.space(6)
            Action { text: strings.t("approve"); selected: true; enabled: !root.pending("approve"); onClicked: root.request("approve", { requestId: root.approval.requestId, allow: true }) }
            Action { text: strings.t("deny"); enabled: !root.pending("approve"); onClicked: root.request("approve", { requestId: root.approval.requestId, allow: false }) }
          }
        }
        Rule {}
        Section { text: strings.t("pairedDevices").toUpperCase() }
        Hint { visible: root.devices.length === 0; width: parent.width; text: strings.t("noDevices") }
        Repeater {
          model: root.devices
          delegate: Column {
            id: device
            required property var modelData
            readonly property string alias: String(modelData.alias || modelData.name || "Device")
            width: parent.width
            spacing: Style.space(6)
            Row {
              width: parent.width
              spacing: Style.space(4)
              Glyph { id: deviceGlyph; text: "󰌷"; width: Style.space(28); horizontalAlignment: Text.AlignHCenter; anchors.verticalCenter: parent.verticalCenter }
              Label { width: parent.width - deviceGlyph.width - renameAction.width - unpairAction.width - parent.spacing * 3; text: device.alias; elide: Text.ElideRight; maximumLineCount: 1; anchors.verticalCenter: parent.verticalCenter }
              IconAction { id: renameAction; iconText: "󰏫"; tooltipText: strings.t("rename"); onClicked: { root.editAlias = device.alias; root.removeAlias = ""; renameInput.text = device.alias; renameInput.forceActiveFocus(); renameInput.selectAll() } }
              IconAction { id: unpairAction; iconText: "󰅖"; tooltipText: strings.t("unpair"); onClicked: { root.removeAlias = device.alias; root.editAlias = "" } }
            }
            Row {
              visible: root.editAlias === device.alias
              width: parent.width
              spacing: Style.space(6)
              TextField { id: renameInput; width: parent.width - renameSave.width - parent.spacing; maximumLength: 128; Accessible.name: strings.t("newAlias"); onActiveFocusChanged: if (activeFocus) root.keepVisible(this); onAccepted: if (renameSave.enabled) renameSave.clicked() }
              Action { id: renameSave; text: strings.t("rename"); bordered: true; enabled: !!renameInput.text.trim() && renameInput.text.trim() !== device.alias && !root.pending("rename"); onClicked: { root.request("rename", { alias: device.alias, newAlias: renameInput.text.trim() }); root.editAlias = "" } }
            }
            Action { visible: root.editAlias === device.alias; text: strings.t("cancel"); onClicked: { root.editAlias = ""; renameAction.forceActiveFocus() } }
            Column {
              visible: root.removeAlias === device.alias
              width: parent.width
              spacing: Style.space(4)
              Hint { width: parent.width; text: strings.t("unpairHint") }
              Flow { width: parent.width; spacing: Style.space(6)
                Action { text: strings.t("unpairConfirm"); bordered: true; enabled: !root.pending("unpair"); onClicked: { root.request("unpair", { alias: device.alias }); root.removeAlias = "" } }
                Action { text: strings.t("cancel"); onClicked: root.removeAlias = "" }
              }
            }
          }
        }
        Rule {}
        Action { text: strings.t("restart"); iconText: "󰑓"; onClicked: root.confirmRestart = !root.confirmRestart }
        Column {
          visible: root.confirmRestart
          width: parent.width
          spacing: Style.space(6)
          Hint { width: parent.width; text: strings.t("restartHint") }
          Flow { width: parent.width; spacing: Style.space(6)
            Action { text: strings.t("restartConfirm"); bordered: true; enabled: !!root.service; onClicked: { root.service.restart(); root.confirmRestart = false } }
            Action { text: strings.t("cancel"); onClicked: root.confirmRestart = false }
          }
        }
      }
    }
  }
  Column {
    id: status
    anchors.bottom: parent.bottom
    width: parent.width
    spacing: Style.space(6)
    visible: !!(root.service && root.service.pendingClears && root.service.pendingClears.length) || root.showNotice || root.sending || (!!root.approval && root.tab !== "manage")
    Rule {}
    Row {
      visible: !!(root.service && root.service.pendingClears && root.service.pendingClears.length)
      spacing: Style.space(12)
      Label { text: strings.t("itemsCleared"); anchors.verticalCenter: parent.verticalCenter }
      Action { text: strings.t("undo"); onClicked: root.service.undoClear() }
    }
    Label {
      visible: root.showNotice && !(root.service && root.service.pendingClears && root.service.pendingClears.length)
      width: parent.width
      text: root.service && root.service.noticeKind === "error" && root.service.noticeAction
        ? strings.t("error." + root.service.noticeAction, strings.t("actionFailed")).arg(root.notice) : root.notice
      color: root.service && root.service.noticeKind === "error" ? Commons.Color.urgent : root.fg
      font.pixelSize: Style.font.bodySmall
      maximumLineCount: 4
      elide: Text.ElideRight
      Accessible.role: Accessible.StaticText
      Accessible.name: text
    }
    Action { visible: root.sending; text: strings.t("cancelTransfer"); enabled: !root.pending("cancel"); onClicked: root.service.cancelActive() }
    Action { visible: root.service && !root.service.started; text: strings.t("restart"); onClicked: { root.showTab("manage"); root.confirmRestart = true } }
    Action { visible: !!root.approval && root.tab !== "manage"; text: strings.t("reviewPair"); bordered: true; onClicked: root.showTab("manage") }
  }
}
