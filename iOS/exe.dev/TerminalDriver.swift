import Foundation
import GhosttyVT

actor TerminalDriver {
    private let emulator = TerminalEmulator()
    private var latestScreenState = TerminalScreenState()

    func applyOutput(_ data: Data) -> TerminalScreenState? {
        emulator.write(data)
        return snapshot()
    }

    func resize(cols: UInt16, rows: UInt16) -> TerminalScreenState? {
        resize(cols: cols, rows: rows, cellWidthPx: 0, cellHeightPx: 0)
    }

    func resize(
        cols: UInt16,
        rows: UInt16,
        cellWidthPx: UInt32,
        cellHeightPx: UInt32
    ) -> TerminalScreenState? {
        guard cols != emulator.cols || rows != emulator.rows else { return nil }
        emulator.resize(
            cols: cols,
            rows: rows,
            cellWidthPx: cellWidthPx,
            cellHeightPx: cellHeightPx
        )
        return snapshot()
    }

    func encodeKey(
        key: GhosttyKey,
        action: GhosttyKeyAction,
        mods: GhosttyMods,
        text: String?,
        codepoint: UInt32
    ) -> String? {
        guard let data = emulator.encodeKey(
            key: key,
            action: action,
            mods: mods,
            text: text,
            unshiftedCodepoint: codepoint
        ) else {
            return nil
        }
        return String(data: data, encoding: .utf8)
    }

    func currentGridSize() -> (cols: UInt16, rows: UInt16) {
        (emulator.cols, emulator.rows)
    }

    private func snapshot() -> TerminalScreenState? {
        guard let updated = emulator.getScreenState(previous: latestScreenState) else {
            return nil
        }
        latestScreenState = updated
        return updated
    }
}
