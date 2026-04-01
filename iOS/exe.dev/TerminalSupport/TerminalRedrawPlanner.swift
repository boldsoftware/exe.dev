import Foundation

nonisolated enum TerminalDirtyState: Equatable {
    case clean
    case partial
    case full
}

nonisolated struct TerminalRedrawPlan: Equatable {
    let fullRedraw: Bool
    let dirtyRows: [Int]
}

nonisolated enum TerminalRedrawPlanner {
    static func plan(
        dirtyState: TerminalDirtyState,
        rowCount: Int,
        reportedDirtyRows: [Int],
        previousCursorRow: Int?,
        currentCursorRow: Int?,
        forceFullRedraw: Bool = false
    ) -> TerminalRedrawPlan {
        guard rowCount > 0 else {
            return TerminalRedrawPlan(fullRedraw: false, dirtyRows: [])
        }

        if forceFullRedraw || dirtyState == .full {
            return TerminalRedrawPlan(
                fullRedraw: true,
                dirtyRows: Array(0..<rowCount)
            )
        }

        guard dirtyState == .partial else {
            return TerminalRedrawPlan(fullRedraw: false, dirtyRows: [])
        }

        var uniqueRows = Set<Int>()
        uniqueRows.reserveCapacity(reportedDirtyRows.count + 2)

        for row in reportedDirtyRows where row >= 0 && row < rowCount {
            uniqueRows.insert(row)
        }

        if let previousCursorRow, previousCursorRow >= 0 && previousCursorRow < rowCount {
            uniqueRows.insert(previousCursorRow)
        }

        if let currentCursorRow, currentCursorRow >= 0 && currentCursorRow < rowCount {
            uniqueRows.insert(currentCursorRow)
        }

        let dirtyRows = uniqueRows.sorted()
        return TerminalRedrawPlan(
            fullRedraw: dirtyRows.count == rowCount,
            dirtyRows: dirtyRows
        )
    }
}
