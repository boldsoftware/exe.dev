import Foundation

/// Manages a WebSocket connection to the exe.dev terminal backend.
/// Protocol:
///   Server → Client: {"type": "output", "data": "<base64>"}
///   Client → Server: {"type": "input", "data": "<plaintext>"}
///   Client → Server: {"type": "resize", "cols": N, "rows": N}
final class TerminalConnection: NSObject {
    private let url: URL
    private let token: String?
    private var webSocketTask: URLSessionWebSocketTask?
    private var urlSession: URLSession?

    /// Called when output arrives from the PTY.
    var onOutput: ((Data) -> Void)?
    /// Called when the connection closes.
    var onDisconnect: (() -> Void)?

    /// Creates a terminal connection.
    /// - Parameters:
    ///   - vmName: The VM name (used to construct the xterm subdomain)
    ///   - sessionName: A session identifier (e.g. "ios-term")
    ///   - token: The auth token for cookie-based auth
    init(vmName: String, sessionName: String = "ios-term", token: String?) {
        // Terminal WebSocket URL: wss://<vmname>.xterm.exe.xyz/terminal/ws/0?name=<session>
        let host = "\(vmName).xterm.exe.xyz"
        var components = URLComponents()
        components.scheme = "wss"
        components.host = host
        components.path = "/terminal/ws/0"
        components.queryItems = [URLQueryItem(name: "name", value: sessionName)]
        self.url = components.url!
        self.token = token
        super.init()
    }

    func connect() {
        let config = URLSessionConfiguration.default
        // Set auth cookie for the terminal domain.
        if let token {
            let host = url.host ?? ""
            let cookie = HTTPCookie(properties: [
                .name: "exe-auth",
                .value: token,
                .domain: host,
                .path: "/",
                .secure: "TRUE",
            ])
            if let cookie {
                config.httpCookieStorage?.setCookie(cookie)
            }
        }
        let session = URLSession(configuration: config, delegate: nil, delegateQueue: nil)
        urlSession = session
        let task = session.webSocketTask(with: url)
        self.webSocketTask = task
        task.resume()
        receiveLoop()
    }

    func disconnect() {
        onOutput = nil
        onDisconnect = nil
        webSocketTask?.cancel(with: .normalClosure, reason: nil)
        webSocketTask = nil
        urlSession?.invalidateAndCancel()
        urlSession = nil
    }

    /// Send raw terminal input text to the PTY.
    func sendInput(_ text: String) {
        guard let task = webSocketTask else { return }
        let msg: [String: Any] = ["type": "input", "data": text]
        guard let data = try? JSONSerialization.data(withJSONObject: msg) else { return }
        task.send(.data(data)) { _ in }
    }

    /// Notify the server of a terminal resize.
    func sendResize(cols: UInt16, rows: UInt16) {
        guard let task = webSocketTask else { return }
        let msg: [String: Any] = ["type": "resize", "cols": cols, "rows": rows]
        guard let data = try? JSONSerialization.data(withJSONObject: msg) else { return }
        task.send(.data(data)) { _ in }
    }

    // MARK: - Private

    private func receiveLoop() {
        webSocketTask?.receive { [weak self] result in
            guard let self else { return }
            switch result {
            case .success(let message):
                self.handleMessage(message)
                self.receiveLoop()
            case .failure:
                self.onDisconnect?()
            }
        }
    }

    private func handleMessage(_ message: URLSessionWebSocketTask.Message) {
        let data: Data
        switch message {
        case .data(let d): data = d
        case .string(let s): data = Data(s.utf8)
        @unknown default: return
        }

        // Parse {"type": "output", "data": "<base64>"}
        guard let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = json["type"] as? String, type == "output",
              let b64 = json["data"] as? String,
              let decoded = Data(base64Encoded: b64)
        else { return }

        onOutput?(decoded)
    }
}
