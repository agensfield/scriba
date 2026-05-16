import AppKit

enum AppMenus {
    @MainActor
    static func install() {
        let mainMenu = NSMenu()

        let appItem = NSMenuItem()
        let appMenu = NSMenu()
        appMenu.addItem(NSMenuItem(
            title: "About ScribaBar",
            action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)),
            keyEquivalent: ""))
        appMenu.addItem(.separator())
        appMenu.addItem(NSMenuItem(
            title: "Quit ScribaBar",
            action: #selector(NSApplication.terminate(_:)),
            keyEquivalent: "q"))
        appItem.submenu = appMenu
        mainMenu.addItem(appItem)

        let editItem = NSMenuItem()
        let editMenu = NSMenu(title: "Edit")
        addEditItem("Undo", "z", #selector(UndoManager.undo), to: editMenu)
        addEditItem("Redo", "Z", #selector(UndoManager.redo), to: editMenu)
        editMenu.addItem(.separator())
        addEditItem("Cut", "x", #selector(NSText.cut(_:)), to: editMenu)
        addEditItem("Copy", "c", #selector(NSText.copy(_:)), to: editMenu)
        addEditItem("Paste", "v", #selector(NSText.paste(_:)), to: editMenu)
        addEditItem("Select All", "a", #selector(NSText.selectAll(_:)), to: editMenu)
        editItem.submenu = editMenu
        mainMenu.addItem(editItem)

        NSApplication.shared.mainMenu = mainMenu
    }

    private static func addEditItem(
        _ title: String,
        _ keyEquivalent: String,
        _ action: Selector,
        to menu: NSMenu)
    {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: keyEquivalent)
        item.target = nil
        menu.addItem(item)
    }
}
