import SwiftUI
import UIKit
import GhosttyVT

// MARK: - UIKit Terminal Renderer

/// A UIView that renders the terminal grid using Core Text.
/// This is the core rendering surface — it draws each cell as a
/// monospace character with the correct fg/bg/style.
class TerminalUIView: UIView, UIKeyInput, UITextInputTraits {

    var screenState = TerminalScreenState() {
        didSet { setNeedsDisplay() }
    }

    /// Called when the user types on the software keyboard (raw text).
    var onInput: ((String) -> Void)?

    /// Called to encode a key event through libghostty-vt's key encoder.
    /// Returns the escape sequence Data to send, or nil for no output.
    var onEncodeKey: ((GhosttyKey, GhosttyKeyAction, GhosttyMods, String?, UInt32) -> Data?)?

    /// Called when the view's bounds change (drawer toggle, rotation, etc.)
    /// with the new (cols, rows).
    var onGridResize: ((UInt16, UInt16) -> Void)?

    /// Track the last reported grid size to avoid redundant callbacks.
    private var lastReportedGrid: (cols: Int, rows: Int) = (0, 0)

    /// The monospace font used for rendering.
    /// "Designed for iPad" on Mac scales the UI by ~77%, so we compensate
    /// to match Ghostty's default 13pt appearance on macOS.
    private var termFont: UIFont = {
        let size: CGFloat = ProcessInfo.processInfo.isiOSAppOnMac ? 17 : 13
        if let mono = UIFont(name: "Menlo", size: size) { return mono }
        return UIFont.monospacedSystemFont(ofSize: size, weight: .regular)
    }()

    /// Computed cell dimensions based on the font.
    var cellSize: CGSize {
        let attrs: [NSAttributedString.Key: Any] = [.font: termFont]
        let mSize = ("M" as NSString).size(withAttributes: attrs)
        return CGSize(width: ceil(mSize.width), height: ceil(mSize.height))
    }

    /// How many cols/rows fit in the current view size.
    var gridSize: (cols: Int, rows: Int) {
        let cell = cellSize
        guard cell.width > 0 && cell.height > 0 else { return (80, 24) }
        let cols = max(1, Int(bounds.width / cell.width))
        let rows = max(1, Int(bounds.height / cell.height))
        return (cols, rows)
    }

    override init(frame: CGRect) {
        super.init(frame: frame)
        backgroundColor = .black
        isUserInteractionEnabled = true
    }

    required init?(coder: NSCoder) {
        super.init(coder: coder)
        backgroundColor = .black
        isUserInteractionEnabled = true
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        let (cols, rows) = gridSize
        if cols != lastReportedGrid.cols || rows != lastReportedGrid.rows {
            lastReportedGrid = (cols, rows)
            onGridResize?(UInt16(cols), UInt16(rows))
        }
    }

    // MARK: - Drawing

    override func draw(_ rect: CGRect) {
        guard let ctx = UIGraphicsGetCurrentContext() else { return }
        let cell = cellSize
        let state = screenState

        // Fill background
        ctx.setFillColor(state.bgColor.cgColor)
        ctx.fill(bounds)

        let paragraphStyle = NSMutableParagraphStyle()
        paragraphStyle.lineBreakMode = .byClipping

        for (y, row) in state.cells.enumerated() {
            for (x, termCell) in row.enumerated() {
                let cellRect = CGRect(
                    x: CGFloat(x) * cell.width,
                    y: CGFloat(y) * cell.height,
                    width: cell.width,
                    height: cell.height
                )

                var fg = termCell.fg
                var bg = termCell.bg

                if termCell.inverse {
                    let tmp = fg
                    fg = bg ?? state.bgColor
                    bg = tmp
                }

                // Draw cell background if non-default
                if let bg {
                    ctx.setFillColor(bg.cgColor)
                    ctx.fill(cellRect)
                }

                // Draw cursor
                if state.cursor.visible && x == state.cursor.x && y == state.cursor.y {
                    ctx.setFillColor(UIColor.white.withAlphaComponent(0.5).cgColor)
                    ctx.fill(cellRect)
                    fg = .black
                }

                // Draw character
                let ch = termCell.character
                guard ch != " " || (state.cursor.visible && x == state.cursor.x && y == state.cursor.y) else {
                    continue
                }

                var font = termFont
                if termCell.bold && termCell.italic {
                    font = UIFont(descriptor: termFont.fontDescriptor.withSymbolicTraits([.traitBold, .traitItalic]) ?? termFont.fontDescriptor, size: termFont.pointSize)
                } else if termCell.bold {
                    font = UIFont(descriptor: termFont.fontDescriptor.withSymbolicTraits(.traitBold) ?? termFont.fontDescriptor, size: termFont.pointSize)
                } else if termCell.italic {
                    font = UIFont(descriptor: termFont.fontDescriptor.withSymbolicTraits(.traitItalic) ?? termFont.fontDescriptor, size: termFont.pointSize)
                }

                let attrs: [NSAttributedString.Key: Any] = [
                    .font: font,
                    .foregroundColor: fg,
                    .paragraphStyle: paragraphStyle,
                ]
                let str = NSAttributedString(string: ch, attributes: attrs)
                str.draw(in: cellRect)
            }
        }
    }

    // MARK: - UIKeyInput

    override var canBecomeFirstResponder: Bool { true }

    var hasText: Bool { true }

    // These are UITextInputTraits properties.
    var autocapitalizationType: UITextAutocapitalizationType = .none
    var autocorrectionType: UITextAutocorrectionType = .no
    var smartQuotesType: UITextSmartQuotesType = .no
    var smartDashesType: UITextSmartDashesType = .no
    var smartInsertDeleteType: UITextSmartInsertDeleteType = .no
    var spellCheckingType: UITextSpellCheckingType = .no
    var keyboardType: UIKeyboardType = .asciiCapable

    func insertText(_ text: String) {
        onInput?(text)
    }

    func deleteBackward() {
        sendKey(GHOSTTY_KEY_BACKSPACE, text: "\u{7f}", codepoint: 0x08)
    }

    // MARK: - Hardware keyboard support

    override var keyCommands: [UIKeyCommand]? {
        var commands: [UIKeyCommand] = []

        // Arrow keys
        commands.append(UIKeyCommand(input: UIKeyCommand.inputUpArrow, modifierFlags: [], action: #selector(handleArrowUp)))
        commands.append(UIKeyCommand(input: UIKeyCommand.inputDownArrow, modifierFlags: [], action: #selector(handleArrowDown)))
        commands.append(UIKeyCommand(input: UIKeyCommand.inputLeftArrow, modifierFlags: [], action: #selector(handleArrowLeft)))
        commands.append(UIKeyCommand(input: UIKeyCommand.inputRightArrow, modifierFlags: [], action: #selector(handleArrowRight)))
        commands.append(UIKeyCommand(input: UIKeyCommand.inputEscape, modifierFlags: [], action: #selector(handleEscape)))

        // Tab
        let tabCmd = UIKeyCommand(input: "\t", modifierFlags: [], action: #selector(handleTab))
        tabCmd.wantsPriorityOverSystemBehavior = true
        commands.append(tabCmd)

        // Ctrl+C, Ctrl+D, Ctrl+Z, Ctrl+L, Ctrl+A, Ctrl+E, Ctrl+K, Ctrl+U
        for ch in ["c", "d", "z", "l", "a", "e", "k", "u", "w", "r", "p", "n", "b", "f"] {
            let cmd = UIKeyCommand(input: ch, modifierFlags: .control, action: #selector(handleCtrl(_:)))
            cmd.wantsPriorityOverSystemBehavior = true
            commands.append(cmd)
        }

        return commands
    }

    @objc private func handleArrowUp() { sendKey(GHOSTTY_KEY_ARROW_UP) }
    @objc private func handleArrowDown() { sendKey(GHOSTTY_KEY_ARROW_DOWN) }
    @objc private func handleArrowRight() { sendKey(GHOSTTY_KEY_ARROW_RIGHT) }
    @objc private func handleArrowLeft() { sendKey(GHOSTTY_KEY_ARROW_LEFT) }
    @objc private func handleEscape() { sendKey(GHOSTTY_KEY_ESCAPE) }
    @objc private func handleTab() { sendKey(GHOSTTY_KEY_TAB, text: "\t", codepoint: 0x09) }

    @objc private func handleCtrl(_ command: UIKeyCommand) {
        guard let input = command.input, let scalar = input.unicodeScalars.first else { return }
        let ctrlChar = scalar.value - UInt32(Character("a").asciiValue!) + 1
        onInput?(String(UnicodeScalar(ctrlChar)!))
    }

    // Handle Enter and Ctrl+letter via pressesBegan for hardware keyboards.
    // pressesBegan fires before UIKeyCommand, making it more reliable for
    // intercepting keys that the system would otherwise swallow (e.g. Ctrl+Z
    // as undo on "Designed for iPad" on Mac).
    override func pressesBegan(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        for press in presses {
            guard let key = press.key else { continue }
            if key.keyCode == .keyboardReturnOrEnter {
                sendKey(GHOSTTY_KEY_ENTER, text: "\r", codepoint: 0x0D)
                return
            }
            // Ctrl+letter: send the raw control character directly to the PTY,
            // bypassing the ghostty key encoder. The encoder may produce kitty
            // protocol CSI sequences that won't trigger PTY signal processing
            // (e.g. SIGTSTP for Ctrl+Z). Raw bytes 0x01-0x1A are what the PTY
            // line discipline expects.
            if key.modifierFlags.contains(.control),
               let keyChar = hidUsageToLetter(key.keyCode) {
                let ctrlChar = keyChar.value - UInt32(Character("a").asciiValue!) + 1
                onInput?(String(UnicodeScalar(ctrlChar)!))
                return
            }
        }
        super.pressesBegan(presses, with: event)
    }

    // Also need to swallow pressesEnded for keys we handle.
    override func pressesEnded(_ presses: Set<UIPress>, with event: UIPressesEvent?) {
        for press in presses {
            guard let key = press.key else { continue }
            if key.keyCode == .keyboardReturnOrEnter { return }
            if key.modifierFlags.contains(.control),
               hidUsageToLetter(key.keyCode) != nil { return }
        }
        super.pressesEnded(presses, with: event)
    }

    /// Map a HID keyboard usage code to a lowercase letter scalar, or nil.
    private func hidUsageToLetter(_ code: UIKeyboardHIDUsage) -> Unicode.Scalar? {
        let raw = code.rawValue
        // HID usage 0x04 = 'a', 0x05 = 'b', ..., 0x1D = 'z'
        guard raw >= 0x04, raw <= 0x1D else { return nil }
        return Unicode.Scalar(UInt32(Character("a").asciiValue!) + UInt32(raw - 0x04))
    }

    // MARK: - Key encoding helpers

    private func sendKey(_ key: GhosttyKey, mods: GhosttyMods = 0,
                         text: String? = nil, codepoint: UInt32 = 0) {
        if let data = onEncodeKey?(key, GHOSTTY_KEY_ACTION_PRESS, mods, text, codepoint),
           let str = String(data: data, encoding: .utf8) {
            onInput?(str)
        }
    }

    /// Map a lowercase letter scalar to a GhosttyKey.
    private func ghosttyKeyForLetter(_ scalar: Unicode.Scalar) -> GhosttyKey {
        let base = GHOSTTY_KEY_A.rawValue
        let offset = scalar.value - UInt32(Character("a").asciiValue!)
        return GhosttyKey(rawValue: base + offset)
    }
}

// MARK: - SwiftUI Bridge

/// SwiftUI wrapper for the terminal UIView.
struct TerminalViewRepresentable: UIViewRepresentable {
    let screenState: TerminalScreenState
    let onInput: (String) -> Void
    let onEncodeKey: (GhosttyKey, GhosttyKeyAction, GhosttyMods, String?, UInt32) -> Data?
    let onResize: (UInt16, UInt16) -> Void

    func makeUIView(context: Context) -> TerminalUIView {
        let view = TerminalUIView()
        view.onInput = onInput
        view.onEncodeKey = onEncodeKey
        view.onGridResize = onResize
        // Become first responder to receive keyboard input.
        DispatchQueue.main.async { view.becomeFirstResponder() }
        return view
    }

    func updateUIView(_ view: TerminalUIView, context: Context) {
        view.screenState = screenState
        view.onInput = onInput
        view.onEncodeKey = onEncodeKey
        view.onGridResize = onResize
    }
}

// MARK: - Output Coalescing

/// Thread-safe buffer that coalesces terminal output from the WebSocket
/// background queue and flushes it to the main actor at most once per
/// scheduled task, preventing main-actor flooding.
final class TerminalOutputBuffer: @unchecked Sendable {
    private let lock = NSLock()
    private var buffer = Data()
    private var flushScheduled = false

    /// Append data (called from any thread) and schedule a coalesced flush.
    /// The flush closure runs on the main actor at most once per scheduling cycle.
    func append(_ data: Data, flush: @escaping @MainActor @Sendable () -> Void) {
        lock.lock()
        buffer.append(data)
        let needsSchedule = !flushScheduled
        flushScheduled = true
        lock.unlock()

        if needsSchedule {
            Task { @MainActor in flush() }
        }
    }

    /// Take all buffered data, resetting the buffer and allowing new flushes.
    func take() -> Data {
        lock.lock()
        let data = buffer
        buffer = Data()
        flushScheduled = false
        lock.unlock()
        return data
    }

    func reset() {
        lock.lock()
        buffer = Data()
        flushScheduled = false
        lock.unlock()
    }
}

// MARK: - VM Terminal View

/// ViewModel that owns the terminal emulator and connection.
@MainActor @Observable
class TerminalViewModel {
    var screenState = TerminalScreenState()
    var isConnected = false
    var connectionError: String?

    private let emulator = TerminalEmulator()
    private var connection: TerminalConnection?
    private let outputBuffer = TerminalOutputBuffer()
    /// Incremented on each connect/disconnect to let stale callbacks bail out.
    private var generation = 0

    func connect(vmName: String, token: String?) {
        disconnect()
        connectionError = nil

        let conn = TerminalConnection(vmName: vmName, token: token)
        let gen = generation
        let buf = outputBuffer

        // Buffer output from the background WebSocket queue; flush coalesced
        // on the main actor so we do at most one write+render per batch.
        conn.onOutput = { [weak self] data in
            buf.append(data) { [weak self] in
                guard let self, self.generation == gen else { return }
                self.flushOutput()
            }
        }

        conn.onDisconnect = { [weak self] in
            Task { @MainActor [weak self] in
                guard let self, self.generation == gen else { return }
                self.isConnected = false
                self.connectionError = "Disconnected from terminal"
            }
        }

        conn.connect()
        connection = conn
        isConnected = true

        // Send initial resize after a brief delay.
        Task { @MainActor [weak self] in
            try? await Task.sleep(for: .milliseconds(100))
            guard let self, self.generation == gen else { return }
            conn.sendResize(cols: self.emulator.cols, rows: self.emulator.rows)
        }
    }

    func disconnect() {
        generation += 1
        outputBuffer.reset()
        connection?.disconnect()
        connection = nil
        isConnected = false
    }

    func sendInput(_ text: String) {
        connection?.sendInput(text)
    }

    /// Encode a key event through the ghostty key encoder.
    func encodeKey(key: GhosttyKey, action: GhosttyKeyAction, mods: GhosttyMods,
                   text: String?, codepoint: UInt32) -> Data? {
        emulator.encodeKey(key: key, action: action, mods: mods,
                          text: text, unshiftedCodepoint: codepoint)
    }

    func resize(cols: UInt16, rows: UInt16) {
        guard cols != emulator.cols || rows != emulator.rows else { return }
        emulator.resize(cols: cols, rows: rows)
        connection?.sendResize(cols: cols, rows: rows)
        screenState = emulator.getScreenState()
    }

    private func flushOutput() {
        let data = outputBuffer.take()
        guard !data.isEmpty else { return }
        emulator.write(data)
        screenState = emulator.getScreenState()
    }
}

/// The terminal tab view shown inside VMDetailView.
struct VMTerminalView: View {
    let vm: StoredVM
    let token: String?

    @State private var viewModel = TerminalViewModel()

    var body: some View {
        ZStack {
            Color.black

            if let error = viewModel.connectionError {
                VStack(spacing: 12) {
                    Image(systemName: "terminal")
                        .font(.system(size: 40))
                        .foregroundStyle(.secondary)
                    Text("Connection Error")
                        .font(.headline)
                        .foregroundStyle(.primary)
                    Text(error)
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                        .multilineTextAlignment(.center)
                    Button("Retry") {
                        viewModel.connect(vmName: vm.vmName, token: token)
                    }
                    .buttonStyle(.bordered)
                }
                .padding()
            } else if !viewModel.isConnected {
                VStack(spacing: 12) {
                    ProgressView()
                        .controlSize(.large)
                    Text("Connecting to \(vm.vmName)...")
                        .font(.subheadline)
                        .foregroundStyle(.secondary)
                }
            } else {
                TerminalViewRepresentable(
                    screenState: viewModel.screenState,
                    onInput: { text in
                        viewModel.sendInput(text)
                    },
                    onEncodeKey: { key, action, mods, text, codepoint in
                        viewModel.encodeKey(key: key, action: action, mods: mods,
                                            text: text, codepoint: codepoint)
                    },
                    onResize: { cols, rows in
                        viewModel.resize(cols: cols, rows: rows)
                    }
                )
            }
        }
        .onAppear {
            viewModel.connect(vmName: vm.vmName, token: token)
        }
        .onDisappear {
            viewModel.disconnect()
        }
    }
}
