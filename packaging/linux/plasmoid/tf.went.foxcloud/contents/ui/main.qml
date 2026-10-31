import QtQuick
import QtQuick.Layouts
import org.kde.plasma.plasmoid
import org.kde.plasma.core as PlasmaCore
import org.kde.plasma.components as PlasmaComponents
import org.kde.plasma.plasma5support as P5Support
import org.kde.kirigami as Kirigami

PlasmoidItem {
    id: root

    // What `foxcloud status-json` prints: { running, rev, update, jobs: [{ job, state, message, until, resync }] }
    property var view: ({ running: false, rev: "offline", jobs: [] })
    property bool failed: false   // the foxcloud command itself could not run

    readonly property string cli: "\"$HOME/.local/bin/foxcloud\""
    readonly property string queryCmd: cli + " status-json"
    readonly property string waitCmd: cli + " status-json -wait "

    // Event-driven, no timer: `status-json -wait REV` blocks inside foxcloud (on the
    // daemon's event stream, or on its socket file appearing) and exits when the state
    // differs from REV. Each answer re-arms the next wait with its own revision, and a
    // wait that starts after a change returns at once, so nothing is missed in between.
    //
    // Sources are (re)connected from a fresh event-loop turn, never from inside onNewData:
    // the executable engine can answer a just-disconnected, still-cached source
    // synchronously, which can recurse until the JS stack overflows. A trailing shell
    // comment makes every run a distinct source, so a cached answer is never replayed.
    property int seq: 0
    function uniq(cmd) { return cmd + " # " + (++seq) }
    function connectQuery() { exec.connectSource(uniq(queryCmd)) }
    function connectWait(rev) { exec.connectSource(uniq(waitCmd + rev)) }

    P5Support.DataSource {
        id: exec
        engine: "executable"
        connectedSources: []
        onNewData: (source, data) => {
            disconnectSource(source)
            const isStatus = source.startsWith(root.queryCmd)
            if (!isStatus) return   // actions (sync now, stop): nothing to read back
            let v = null
            try { v = JSON.parse(data.stdout) } catch (e) {}
            if (v === null) {
                // foxcloud missing or broken. Retry once in a while rather than
                // spinning on a command that fails at once.
                root.failed = true
                retry.restart()
                return
            }
            root.failed = false
            root.view = v
            Qt.callLater(function () { root.connectWait(v.rev) })
        }
    }

    // Only reached when the command itself fails (not installed yet, say).
    Timer {
        id: retry
        interval: 30000
        onTriggered: root.connectQuery()
    }

    Component.onCompleted: connectQuery()

    function act(verb, job) {
        // Job names come from the config; quote them for the shell.
        exec.connectSource(uniq(cli + " " + verb + " '" + job.replace(/'/g, "'\\''") + "'"))
    }

    readonly property var jobs: view.jobs || []
    readonly property var running: ["uploading", "downloading", "syncing"]
    readonly property bool anyRunning: jobs.some(j => running.indexOf(j.state) >= 0)
    readonly property int conflictTotal: jobs.reduce((n, j) => n + (j.conflicts || 0), 0)

    function syncAll() { for (const j of jobs) { if (j.state !== "mounted") act("sync-now", j.job) } }
    function stopAll() { for (const j of jobs) { if (running.indexOf(j.state) >= 0) act("stop", j.job) } }

    // Conflict resolution is a conversation, so it gets a terminal of its own:
    // the first emulator found, detached so closing the popup does not close it.
    function resolveConflicts() {
        const pick = "for t in xdg-terminal-exec konsole kgx gnome-terminal xterm; do " +
            "command -v $t >/dev/null || continue; " +
            "case $t in xdg-terminal-exec) exec $t \"$@\";; gnome-terminal) exec $t -- \"$@\";; *) exec $t -e \"$@\";; esac; done; " +
            "echo 'no terminal emulator found' >&2; exit 1"
        exec.connectSource(uniq("setsid -f sh -c '" + pick.replace(/'/g, "'\\''") + "' sh " + cli + " conflicts resolve"))
    }

    function has(state) { return jobs.some(j => j.state === state) }

    // The same summary the tray uses: trouble outranks activity, which outranks rest.
    readonly property string iconName: {
        if (!view.running) return "offline"
        if (has("failed")) return "failed"
        if (has("paused")) return "paused"
        const up = has("uploading"), down = has("downloading")
        if (has("syncing") || (up && down)) return "syncing"
        if (up) return "uploading"
        if (down) return "downloading"
        if (has("waiting")) return "idle"
        if (has("synced") || has("mounted")) return "synced"
        return "idle"
    }

    // What the job does, in words, shown under its name.
    function roleText(j) {
        switch (j.role) {
        case "push": return i18n("Uploads this computer's folder to the server")
        case "pull": return i18n("Downloads from the server, never deletes here")
        case "bisync": return i18n("Keeps this folder and the server in step")
        case "mount": return i18n("Server files on demand")
        default: return ""
        }
    }

    function jobText(j) {
        switch (j.state) {
        case "waiting": return i18n("Waiting for the system to settle")
        case "idle": return j.message === "mounting" ? i18n("Mounting…") : i18n("Idle")
        case "uploading": return j.resync ? i18n("Full resync…") : i18n("Uploading…")
        case "downloading": return j.resync ? i18n("Full resync…") : i18n("Downloading…")
        case "syncing": return j.resync ? i18n("Full resync…") : i18n("Syncing…")
        case "synced": return i18n("Up to date")
        case "mounted": return i18n("Mounted")
        case "failed": return i18n("Failed")
        case "paused": return i18n("Paused")
        default: return j.state
        }
    }

    readonly property string stateText: {
        if (failed) return i18n("foxcloud is not installed")
        if (!view.running) return i18n("Service not running")
        switch (iconName) {
        case "failed": return i18n("Sync failed")
        case "paused": return i18n("Sync paused")
        case "syncing": return i18n("Syncing…")
        case "uploading": return i18n("Uploading…")
        case "downloading": return i18n("Downloading…")
        case "synced": return i18n("Up to date")
        default: return i18n("Idle")
        }
    }

    // Only a failure or a pause makes the tray icon pulse: they need a person.
    readonly property bool needsAttention: iconName === "failed" || iconName === "paused"
    // A stopped service is shown steadily (amber icon), not animated.
    readonly property bool offline: !view.running && !failed
    readonly property bool active: iconName === "syncing" || iconName === "uploading" || iconName === "downloading"

    Plasmoid.icon: "folder-cloud"
    toolTipMainText: i18n("FoxCloud")
    toolTipSubText: {
        let lines = [stateText]
        for (const j of jobs) lines.push(j.job + ": " + jobText(j) + (j.message && (j.state === "failed" || j.state === "paused") ? " – " + j.message : ""))
        if (view.update) lines.push(i18n("Update available: %1", view.update))
        return lines.join("\n")
    }
    Plasmoid.status: needsAttention ? PlasmaCore.Types.NeedsAttentionStatus
        : (active || offline) ? PlasmaCore.Types.ActiveStatus
        : PlasmaCore.Types.PassiveStatus

    Plasmoid.contextualActions: [
        PlasmaCore.Action {
            text: i18n("Sync all now")
            icon.name: "view-refresh"
            enabled: root.view.running
            onTriggered: root.syncAll()
        }
    ]

    compactRepresentation: MouseArea {
        onClicked: root.expanded = !root.expanded
        StatusIcon {
            anchors.fill: parent
            name: root.iconName
        }
    }

    fullRepresentation: Item {
        // Layout.margins has no effect on a popup's root item, so the padding is an anchored inset
        readonly property int pad: Kirigami.Units.gridUnit
        Layout.minimumWidth: Kirigami.Units.gridUnit * 18 + 2 * pad
        Layout.preferredWidth: Kirigami.Units.gridUnit * 20 + 2 * pad
        implicitHeight: content.implicitHeight + 2 * pad

        ColumnLayout {
            id: content
            anchors.fill: parent
            anchors.margins: parent.pad
            spacing: Kirigami.Units.largeSpacing

            RowLayout {
                spacing: Kirigami.Units.largeSpacing
                StatusIcon { name: root.iconName; implicitWidth: Kirigami.Units.iconSizes.large; implicitHeight: implicitWidth }
                ColumnLayout {
                    spacing: 0
                    Layout.fillWidth: true
                    PlasmaComponents.Label { text: i18n("FoxCloud"); opacity: 0.7; font: Kirigami.Theme.smallFont }
                    PlasmaComponents.Label { text: root.stateText; font.bold: true; font.pointSize: Kirigami.Theme.defaultFont.pointSize * 1.2 }
                }
            }

            PlasmaComponents.Label {
                visible: root.failed
                text: i18n("Install FoxCloud first (packaging/linux/install.sh); the widget runs ~/.local/bin/foxcloud.")
                wrapMode: Text.Wrap
                Layout.fillWidth: true
            }
            PlasmaComponents.Label {
                visible: !root.failed && !root.view.running
                text: i18n("Start it with: systemctl --user start foxcloudd")
                opacity: 0.7
                wrapMode: Text.Wrap
                Layout.fillWidth: true
            }
            PlasmaComponents.Label {
                visible: !!root.view.update
                text: i18n("Update available: %1 (run foxcloud update)", root.view.update)
                color: Kirigami.Theme.positiveTextColor
                wrapMode: Text.Wrap
                Layout.fillWidth: true
            }

            Repeater {
                model: root.jobs
                ColumnLayout {
                    required property var modelData
                    Layout.fillWidth: true
                    spacing: Kirigami.Units.smallSpacing

                    RowLayout {
                        Layout.fillWidth: true
                        ColumnLayout {
                            spacing: 0
                            Layout.fillWidth: true
                            PlasmaComponents.Label { text: modelData.job; font.bold: true }
                            PlasmaComponents.Label {
                                visible: !!root.roleText(modelData)
                                text: root.roleText(modelData)
                                font: Kirigami.Theme.smallFont
                                opacity: 0.6
                                wrapMode: Text.Wrap
                                Layout.fillWidth: true
                            }
                            PlasmaComponents.Label {
                                text: root.jobText(modelData)
                                opacity: 0.7
                                color: (modelData.state === "failed" || modelData.state === "paused") ? Kirigami.Theme.negativeTextColor : Kirigami.Theme.textColor
                            }
                        }
                    }
                    PlasmaComponents.Label {
                        visible: (modelData.state === "failed" || modelData.state === "paused") && !!modelData.message
                        text: modelData.message || ""
                        color: Kirigami.Theme.negativeTextColor
                        wrapMode: Text.Wrap
                        Layout.fillWidth: true
                    }
                    PlasmaComponents.Label {
                        visible: (modelData.conflicts || 0) > 0
                        text: i18np("%1 file has conflicting versions", "%1 files have conflicting versions", modelData.conflicts || 0)
                        color: Kirigami.Theme.neutralTextColor
                        wrapMode: Text.Wrap
                        Layout.fillWidth: true
                    }
                    PlasmaComponents.Label {
                        visible: modelData.state === "waiting" && !!modelData.until
                        text: modelData.until ? i18n("First sync starts at %1", Qt.formatTime(new Date(modelData.until))) : ""
                        opacity: 0.7
                    }
                }
            }

            // Absorbs spare height so extra window size doesn't spread the content apart
            Item { Layout.fillHeight: true }

            // Fixed at the bottom whatever the number of jobs.
            Kirigami.Separator { Layout.fillWidth: true }
            RowLayout {
                Layout.fillWidth: true
                spacing: Kirigami.Units.smallSpacing
                PlasmaComponents.Button {
                    visible: root.conflictTotal > 0
                    text: i18n("Resolve conflicts…")
                    icon.name: "document-edit"
                    onClicked: root.resolveConflicts()
                }
                Item { Layout.fillWidth: true }
                PlasmaComponents.Button {
                    enabled: root.view.running && root.jobs.length > 0
                    text: root.anyRunning ? i18n("Stop") : i18n("Sync now")
                    icon.name: root.anyRunning ? "process-stop" : "view-refresh"
                    onClicked: root.anyRunning ? root.stopAll() : root.syncAll()
                }
            }
        }
    }
}
