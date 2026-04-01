import Testing
@testable import TerminalRedrawSupport

@Test func fullDirtyForcesFullRedraw() {
    let plan = TerminalRedrawPlanner.plan(
        dirtyState: .full,
        rowCount: 4,
        reportedDirtyRows: [1],
        previousCursorRow: 0,
        currentCursorRow: 2
    )

    #expect(plan == TerminalRedrawPlan(fullRedraw: true, dirtyRows: [0, 1, 2, 3]))
}

@Test func partialDirtyIncludesCursorRows() {
    let plan = TerminalRedrawPlanner.plan(
        dirtyState: .partial,
        rowCount: 6,
        reportedDirtyRows: [2, 4],
        previousCursorRow: 1,
        currentCursorRow: 5
    )

    #expect(plan == TerminalRedrawPlan(fullRedraw: false, dirtyRows: [1, 2, 4, 5]))
}

@Test func plannerDeduplicatesAndBoundsRows() {
    let plan = TerminalRedrawPlanner.plan(
        dirtyState: .partial,
        rowCount: 3,
        reportedDirtyRows: [-1, 0, 0, 2, 9],
        previousCursorRow: 2,
        currentCursorRow: 2
    )

    #expect(plan == TerminalRedrawPlan(fullRedraw: false, dirtyRows: [0, 2]))
}

@Test func cleanStateSkipsRedraw() {
    let plan = TerminalRedrawPlanner.plan(
        dirtyState: .clean,
        rowCount: 5,
        reportedDirtyRows: [1, 2],
        previousCursorRow: 1,
        currentCursorRow: 2
    )

    #expect(plan == TerminalRedrawPlan(fullRedraw: false, dirtyRows: []))
}
