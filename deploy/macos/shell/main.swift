// The macOS shell: a window with a web view, and a daemon whose lifetime is the window's.
//
// It exists for one reason — springback is a server, and a server started by double-clicking has
// to be stopped by closing the thing that was double-clicked. The shell script this replaces
// started the daemon and handed it to the browser, which left a process nobody could see running
// after the browser tab was closed.

import Cocoa
import WebKit

let listenAddr = "127.0.0.1:8971"

final class AppDelegate: NSObject, NSApplicationDelegate, WKNavigationDelegate {
	private var window: NSWindow!
	private var webView: WKWebView!
	private var server: Process?
	private let base = URL(string: "http://\(listenAddr)")!

	func applicationDidFinishLaunching(_: Notification) {
		startServer()
		buildWindow()
		loadWhenHealthy(retriesLeft: 100)
	}

	// CLOSING THE WINDOW QUITS. There is exactly one window and it is the entire application, so
	// the macOS default — keep running with no windows — would recreate the invisible-daemon
	// problem this shell was written to solve.
	func applicationShouldTerminateAfterLastWindowClosed(_: NSApplication) -> Bool { true }

	func applicationWillTerminate(_: Notification) { stopServer() }

	// MARK: - the daemon

	private func startServer() {
		guard let dir = Bundle.main.executableURL?.deletingLastPathComponent() else { return }
		let p = Process()
		p.executableURL = dir.appendingPathComponent("springback-server")
		p.arguments = ["-listen", listenAddr]
		// If the daemon dies, the window is showing a corpse. Take the app down with it rather
		// than leaving a blank frame the user has to interpret.
		p.terminationHandler = { _ in
			DispatchQueue.main.async { NSApp.terminate(nil) }
		}
		do { try p.run() } catch {
			fatalError("cannot start springback-server: \(error)")
		}
		server = p
	}

	private func stopServer() {
		guard let p = server, p.isRunning else { return }
		// Cleared first, or terminate() re-enters NSApp.terminate through the handler above.
		p.terminationHandler = nil
		p.terminate() // SIGTERM, which main.go already installs a handler for
		let deadline = Date().addingTimeInterval(3)
		while p.isRunning, Date() < deadline { usleep(50_000) }
		if p.isRunning { kill(p.processIdentifier, SIGKILL) }
	}

	// MARK: - the window

	private func buildWindow() {
		webView = WKWebView(frame: .zero, configuration: WKWebViewConfiguration())
		webView.navigationDelegate = self
		window = NSWindow(
			contentRect: NSRect(x: 0, y: 0, width: 1100, height: 820),
			styleMask: [.titled, .closable, .miniaturizable, .resizable],
			backing: .buffered, defer: false
		)
		window.title = "springback"
		window.contentView = webView
		window.setFrameAutosaveName("springback-main")
		window.center()
		window.makeKeyAndOrderFront(nil)
		NSApp.activate(ignoringOtherApps: true)
	}

	// The daemon takes a moment to bind. Loading immediately shows a connection-refused page for a
	// server that came up 200ms later, so poll the health endpoint and load only once it answers.
	private func loadWhenHealthy(retriesLeft: Int) {
		var req = URLRequest(url: base.appendingPathComponent("api/health"))
		req.timeoutInterval = 1
		URLSession.shared.dataTask(with: req) { _, resp, _ in
			DispatchQueue.main.async {
				if (resp as? HTTPURLResponse)?.statusCode == 200 {
					self.webView.load(URLRequest(url: self.base))
				} else if retriesLeft > 0 {
					DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) {
						self.loadWhenHealthy(retriesLeft: retriesLeft - 1)
					}
				} else {
					self.webView.loadHTMLString(
						"<body style='font:14px -apple-system;padding:3em'>springback did not start listening on \(listenAddr).</body>",
						baseURL: nil)
				}
			}
		}.resume()
	}
}

// A minimal main menu, because without one Cmd-Q is not bound to anything.
func buildMenu() {
	let main = NSMenu()
	let appItem = NSMenuItem()
	main.addItem(appItem)
	let appMenu = NSMenu()
	appMenu.addItem(withTitle: "About springback", action: #selector(NSApplication.orderFrontStandardAboutPanel(_:)), keyEquivalent: "")
	appMenu.addItem(.separator())
	appMenu.addItem(withTitle: "Hide springback", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
	appMenu.addItem(.separator())
	appMenu.addItem(withTitle: "Quit springback", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
	appItem.submenu = appMenu

	let editItem = NSMenuItem()
	main.addItem(editItem)
	let edit = NSMenu(title: "Edit")
	edit.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
	edit.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
	edit.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
	edit.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
	editItem.submenu = edit

	NSApp.mainMenu = main
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
buildMenu()
app.run()
