import QtQuick
import org.kde.kirigami as Kirigami

// A FoxCloud status icon. The PNGs are staged into contents/icons by
// install.sh from internal/tray/icons; the palette follows the panel: pale
// glyphs when the theme's text is light (a dark panel), deep ones otherwise.
Item {
    id: icon
    property string name: "idle"   // idle | uploading | downloading | syncing | synced | failed | paused | offline
    readonly property string palette: Kirigami.Theme.textColor.hslLightness > 0.5 ? "dark" : "light"

    Image {
        anchors.fill: parent
        source: Qt.resolvedUrl("../icons/" + icon.palette + "-" + icon.name + ".png")
        sourceSize.width: width * 2
        sourceSize.height: height * 2
        fillMode: Image.PreserveAspectFit
        smooth: true
    }
}
