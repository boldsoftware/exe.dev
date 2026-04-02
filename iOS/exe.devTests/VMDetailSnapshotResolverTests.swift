import Testing
@testable import VMListSupport

private struct TestSnapshot: VMNamedSnapshot, Equatable {
    let vmName: String
    let status: String
}

@Test func refreshedSnapshotReplacesCreatingSnapshotForSameVM() {
    let creating = TestSnapshot(vmName: "ridge-pine", status: "creating")
    let running = TestSnapshot(vmName: "ridge-pine", status: "running")

    let resolved = VMDetailSnapshotResolver.resolveCurrent(
        current: creating,
        refreshed: running
    )

    #expect(resolved == running)
}

@Test func missingOrMismatchedSnapshotKeepsCurrentVM() {
    let current = TestSnapshot(vmName: "ridge-pine", status: "creating")
    let other = TestSnapshot(vmName: "anchor-echo", status: "running")

    #expect(
        VMDetailSnapshotResolver.resolveCurrent(
            current: current,
            refreshed: nil
        ) == current
    )
    #expect(
        VMDetailSnapshotResolver.resolveCurrent(
            current: current,
            refreshed: other
        ) == current
    )
}
