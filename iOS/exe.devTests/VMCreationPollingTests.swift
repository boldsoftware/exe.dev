import Testing
@testable import VMListSupport

@Test func createdVMForcesPollingStart() {
    let action = VMCreationPolling.action(
        hasRunningTask: false,
        observedCreatingVMNames: [],
        reason: .createdVM
    )

    #expect(action == .start)
}

@Test func observedCreatingVMsStartPollingWhenIdle() {
    let action = VMCreationPolling.action(
        hasRunningTask: false,
        observedCreatingVMNames: ["alpha"],
        reason: .observedListChange
    )

    #expect(action == .start)
}

@Test func emptyCreatingListCancelsPolling() {
    let action = VMCreationPolling.action(
        hasRunningTask: true,
        observedCreatingVMNames: [],
        reason: .observedListChange
    )

    #expect(action == .cancel)
}

@Test func vmListReloadOnlyRunsForVMNotifications() {
    #expect(VMListReloadPolicy.shouldReload(for: "vms"))
    #expect(!VMListReloadPolicy.shouldReload(for: "conversation"))
    #expect(!VMListReloadPolicy.shouldReload(for: nil))
}

@Test func detailIdentityChangesWhenCreationStateChanges() {
    #expect(
        VMListReloadPolicy.detailIdentity(vmName: "ridge-pine", isCreating: true)
            != VMListReloadPolicy.detailIdentity(vmName: "ridge-pine", isCreating: false)
    )
}
