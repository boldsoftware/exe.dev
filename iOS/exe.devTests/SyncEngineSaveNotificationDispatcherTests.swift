import Foundation
import Testing
@testable import SyncNotificationSupport

private final class NotificationFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var fired = false

    func markFired() {
        lock.lock()
        fired = true
        lock.unlock()
    }

    func isFired() -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return fired
    }
}

@Test func dispatchReturnsBeforeMainQueueObserversRun() async throws {
    let flag = NotificationFlag()
    let token = NotificationCenter.default.addObserver(
        forName: .syncEngineDidSave,
        object: nil,
        queue: .main
    ) { _ in
        flag.markFired()
    }
    defer { NotificationCenter.default.removeObserver(token) }

    SyncEngineSaveNotificationDispatcher.dispatch(
        .conversation(conversationID: "c1", messageIDs: ["m1"], working: true)
    )

    #expect(flag.isFired() == false)

    for _ in 0..<100 where !flag.isFired() {
        try await Task.sleep(for: .milliseconds(10))
    }

    #expect(flag.isFired())
}
