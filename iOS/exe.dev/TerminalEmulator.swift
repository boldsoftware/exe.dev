import Foundation
import UIKit
import GhosttyVT

// MARK: - Terminal Emulator
//
// Wraps libghostty-vt's C API to manage terminal state. libghostty-vt
// handles all VT parsing, screen state, cursor, styles, scrollback,
// reflow, etc. We just feed it bytes and read back the render state.
//
// This follows the exact pattern from ghostling/main.c:
//   1. ghostty_terminal_new       — create terminal
//   2. ghostty_terminal_vt_write  — feed PTY output
//   3. ghostty_terminal_resize    — handle resize
//   4. ghostty_render_state_update — snapshot for rendering
//   5. iterate rows/cells to read graphemes, colors, styles
//   6. ghostty_key_encoder_encode — encode keyboard input

/// A cell in the terminal grid, extracted from the ghostty render state.
struct TerminalCell {
    var character: String = " "
    var fg: UIColor = .white
    var bg: UIColor? = nil
    var bold: Bool = false
    var italic: Bool = false
    var inverse: Bool = false
}

/// Cursor state for rendering.
struct TerminalCursor {
    var x: Int = 0
    var y: Int = 0
    var visible: Bool = true
}

/// A snapshot of the terminal's visible grid for rendering.
struct TerminalScreenState {
    var cells: [[TerminalCell]] = []
    var cursor: TerminalCursor = TerminalCursor()
    var cols: Int = 80
    var rows: Int = 24
    var bgColor: UIColor = .black
    var fgColor: UIColor = .white
}

/// Terminal emulator backed by libghostty-vt.
///
/// Manages the GhosttyTerminal, GhosttyRenderState, and key encoder.
/// All operations must happen on the main actor.
@MainActor
class TerminalEmulator {
    private(set) var cols: UInt16 = 80
    private(set) var rows: UInt16 = 24

    // libghostty-vt handles
    private var terminal: GhosttyTerminal!
    private var renderState: GhosttyRenderState!
    private var rowIterator: GhosttyRenderStateRowIterator!
    private var rowCells: GhosttyRenderStateRowCells!
    private var keyEncoder: GhosttyKeyEncoder!
    private var keyEvent: GhosttyKeyEvent!

    /// Called when the screen needs redrawing.
    var onRedraw: (() -> Void)?

    init(cols: UInt16 = 80, rows: UInt16 = 24) {
        self.cols = cols
        self.rows = rows

        // Create terminal
        let opts = GhosttyTerminalOptions(cols: cols, rows: rows, max_scrollback: 10000)
        var term: GhosttyTerminal?
        let r1 = ghostty_terminal_new(nil, &term, opts)
        assert(r1 == GHOSTTY_SUCCESS && term != nil)
        terminal = term

        // Create render state + reusable iterators
        var rs: GhosttyRenderState?
        let r2 = ghostty_render_state_new(nil, &rs)
        assert(r2 == GHOSTTY_SUCCESS)
        renderState = rs

        var ri: GhosttyRenderStateRowIterator?
        let r3 = ghostty_render_state_row_iterator_new(nil, &ri)
        assert(r3 == GHOSTTY_SUCCESS)
        rowIterator = ri

        var rc: GhosttyRenderStateRowCells?
        let r4 = ghostty_render_state_row_cells_new(nil, &rc)
        assert(r4 == GHOSTTY_SUCCESS)
        rowCells = rc

        // Create key encoder + reusable event
        var ke: GhosttyKeyEncoder?
        let r5 = ghostty_key_encoder_new(nil, &ke)
        assert(r5 == GHOSTTY_SUCCESS)
        keyEncoder = ke

        var kev: GhosttyKeyEvent?
        let r6 = ghostty_key_event_new(nil, &kev)
        assert(r6 == GHOSTTY_SUCCESS)
        keyEvent = kev
    }

    deinit {
        // Free in reverse order
        if let kev = keyEvent { ghostty_key_event_free(kev) }
        if let ke = keyEncoder { ghostty_key_encoder_free(ke) }
        if let rc = rowCells { ghostty_render_state_row_cells_free(rc) }
        if let ri = rowIterator { ghostty_render_state_row_iterator_free(ri) }
        if let rs = renderState { ghostty_render_state_free(rs) }
        if let t = terminal { ghostty_terminal_free(t) }
    }

    /// Feed raw VT-encoded bytes from the PTY into the terminal.
    func write(_ data: Data) {
        data.withUnsafeBytes { buf in
            guard let ptr = buf.baseAddress?.assumingMemoryBound(to: UInt8.self) else { return }
            ghostty_terminal_vt_write(terminal, ptr, buf.count)
        }
    }

    /// Resize the terminal grid.
    func resize(cols: UInt16, rows: UInt16) {
        guard cols > 0, rows > 0 else { return }
        self.cols = cols
        self.rows = rows
        ghostty_terminal_resize(terminal, cols, rows)
    }

    /// Encode a key press and return the escape sequence bytes to send to the PTY.
    /// Returns nil if the key produces no output.
    func encodeKey(key: GhosttyKey, action: GhosttyKeyAction, mods: GhosttyMods,
                   text: String? = nil, unshiftedCodepoint: UInt32 = 0) -> Data? {
        // Sync encoder options from terminal state (cursor key mode, kitty flags, etc.)
        ghostty_key_encoder_setopt_from_terminal(keyEncoder, terminal)

        ghostty_key_event_set_key(keyEvent, key)
        ghostty_key_event_set_action(keyEvent, action)
        ghostty_key_event_set_mods(keyEvent, mods)
        ghostty_key_event_set_unshifted_codepoint(keyEvent, unshiftedCodepoint)

        // Consumed mods: for printable keys, shift is consumed by the platform
        var consumed: GhosttyMods = 0
        if unshiftedCodepoint != 0 && (mods & UInt16(GHOSTTY_MODS_SHIFT)) != 0 {
            consumed |= UInt16(GHOSTTY_MODS_SHIFT)
        }
        ghostty_key_event_set_consumed_mods(keyEvent, consumed)

        // Attach UTF-8 text if present (only for press/repeat, not release)
        if let text, action != GHOSTTY_KEY_ACTION_RELEASE {
            text.withCString { ptr in
                ghostty_key_event_set_utf8(keyEvent, ptr, strlen(ptr))
            }
        } else {
            ghostty_key_event_set_utf8(keyEvent, nil, 0)
        }

        var buf = [CChar](repeating: 0, count: 128)
        var written: Int = 0
        let res = ghostty_key_encoder_encode(keyEncoder, keyEvent, &buf, buf.count, &written)
        guard res == GHOSTTY_SUCCESS, written > 0 else { return nil }
        return Data(bytes: buf, count: written)
    }

    /// Snapshot the terminal into a TerminalScreenState for rendering.
    func getScreenState() -> TerminalScreenState {
        // Update render state from terminal
        ghostty_render_state_update(renderState, terminal)

        // Read colors (zero-init + set size, matching GHOSTTY_INIT_SIZED)
        var colors = GhosttyRenderStateColors()
        withUnsafeMutablePointer(to: &colors) { ptr in
            let raw = UnsafeMutableRawPointer(ptr)
            raw.initializeMemory(as: UInt8.self, repeating: 0, count: MemoryLayout<GhosttyRenderStateColors>.size)
        }
        colors.size = MemoryLayout<GhosttyRenderStateColors>.size
        ghostty_render_state_colors_get(renderState, &colors)

        let defaultFG = uiColor(colors.foreground)
        let defaultBG = uiColor(colors.background)

        // Populate row iterator from render state
        ghostty_render_state_get(renderState, GHOSTTY_RENDER_STATE_DATA_ROW_ITERATOR, &rowIterator)

        var allRows: [[TerminalCell]] = []

        while ghostty_render_state_row_iterator_next(rowIterator) {
            // Get cells for this row
            ghostty_render_state_row_get(rowIterator, GHOSTTY_RENDER_STATE_ROW_DATA_CELLS, &rowCells)

            var row: [TerminalCell] = []
            while ghostty_render_state_row_cells_next(rowCells) {
                var cell = TerminalCell()

                // Read grapheme length
                var graphemeLen: UInt32 = 0
                ghostty_render_state_row_cells_get(rowCells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_GRAPHEMES_LEN, &graphemeLen)

                if graphemeLen > 0 {
                    // Read codepoints
                    var codepoints = [UInt32](repeating: 0, count: Int(min(graphemeLen, 16)))
                    ghostty_render_state_row_cells_get(rowCells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_GRAPHEMES_BUF, &codepoints)

                    // Build string from codepoints
                    var str = ""
                    for i in 0..<Int(min(graphemeLen, 16)) {
                        if let scalar = Unicode.Scalar(codepoints[i]) {
                            str.append(Character(scalar))
                        }
                    }
                    cell.character = str.isEmpty ? " " : str
                }

                // Read foreground color
                var fgRgb = GhosttyColorRgb(r: 0, g: 0, b: 0)
                if ghostty_render_state_row_cells_get(rowCells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_FG_COLOR, &fgRgb) == GHOSTTY_SUCCESS {
                    cell.fg = uiColor(fgRgb)
                } else {
                    cell.fg = defaultFG
                }

                // Read background color
                var bgRgb = GhosttyColorRgb(r: 0, g: 0, b: 0)
                if ghostty_render_state_row_cells_get(rowCells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_BG_COLOR, &bgRgb) == GHOSTTY_SUCCESS {
                    cell.bg = uiColor(bgRgb)
                }

                // Read style flags (zero-init + set size, matching GHOSTTY_INIT_SIZED)
                var style = GhosttyStyle()
                withUnsafeMutablePointer(to: &style) { ptr in
                    let raw = UnsafeMutableRawPointer(ptr)
                    raw.initializeMemory(as: UInt8.self, repeating: 0, count: MemoryLayout<GhosttyStyle>.size)
                }
                style.size = MemoryLayout<GhosttyStyle>.size
                if ghostty_render_state_row_cells_get(rowCells, GHOSTTY_RENDER_STATE_ROW_CELLS_DATA_STYLE, &style) == GHOSTTY_SUCCESS {
                    cell.bold = style.bold
                    cell.italic = style.italic
                    cell.inverse = style.inverse
                }

                row.append(cell)
            }

            // Clear row dirty flag
            var clean = false
            ghostty_render_state_row_set(rowIterator, GHOSTTY_RENDER_STATE_ROW_OPTION_DIRTY, &clean)

            allRows.append(row)
        }

        // Read cursor
        var cursorVisible = false
        ghostty_render_state_get(renderState, GHOSTTY_RENDER_STATE_DATA_CURSOR_VISIBLE, &cursorVisible)
        var cursorInViewport = false
        ghostty_render_state_get(renderState, GHOSTTY_RENDER_STATE_DATA_CURSOR_VIEWPORT_HAS_VALUE, &cursorInViewport)

        var cursor = TerminalCursor()
        if cursorVisible && cursorInViewport {
            var cx: UInt16 = 0
            var cy: UInt16 = 0
            ghostty_render_state_get(renderState, GHOSTTY_RENDER_STATE_DATA_CURSOR_VIEWPORT_X, &cx)
            ghostty_render_state_get(renderState, GHOSTTY_RENDER_STATE_DATA_CURSOR_VIEWPORT_Y, &cy)
            cursor = TerminalCursor(x: Int(cx), y: Int(cy), visible: true)
        }

        // Reset global dirty
        var cleanState = GHOSTTY_RENDER_STATE_DIRTY_FALSE
        ghostty_render_state_set(renderState, GHOSTTY_RENDER_STATE_OPTION_DIRTY, &cleanState)

        return TerminalScreenState(
            cells: allRows,
            cursor: cursor,
            cols: Int(cols),
            rows: Int(rows),
            bgColor: defaultBG,
            fgColor: defaultFG
        )
    }

    // MARK: - Helpers

    private func uiColor(_ rgb: GhosttyColorRgb) -> UIColor {
        UIColor(
            red: CGFloat(rgb.r) / 255.0,
            green: CGFloat(rgb.g) / 255.0,
            blue: CGFloat(rgb.b) / 255.0,
            alpha: 1.0
        )
    }
}
