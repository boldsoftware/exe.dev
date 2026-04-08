import Foundation
import Testing
@testable import ConversationDeltaReducerSupport

@Test func foregroundRefreshesWhenNoCachedConversationExists() {
    let now = Date(timeIntervalSince1970: 1_000)

    #expect(
        ConversationRefreshPolicy.shouldRefresh(
            reason: .appBecameActive,
            hasCachedConversation: false,
            state: ConversationRefreshState(),
            now: now
        )
    )
}

@Test func foregroundRefreshesWhenCacheHasNeverBeenValidated() {
    let now = Date(timeIntervalSince1970: 1_000)

    #expect(
        ConversationRefreshPolicy.shouldRefresh(
            reason: .appBecameActive,
            hasCachedConversation: true,
            state: ConversationRefreshState(),
            now: now
        )
    )
}

@Test func foregroundAlwaysRefreshesEvenWithRecentSuccess() {
    let now = Date(timeIntervalSince1970: 1_000)
    let state = ConversationRefreshState(
        lastAttemptAt: now.addingTimeInterval(-5),
        lastSuccessAt: now.addingTimeInterval(-5),
        lastFailureAt: nil
    )

    // appBecameActive always refreshes — conversations may have changed
    // while the app was backgrounded.
    #expect(
        ConversationRefreshPolicy.shouldRefresh(
            reason: .appBecameActive,
            hasCachedConversation: true,
            state: state,
            now: now
        )
    )
}

@Test func foregroundRefreshesStaleSuccessfulRefresh() {
    let now = Date(timeIntervalSince1970: 1_000)
    let state = ConversationRefreshState(
        lastAttemptAt: now.addingTimeInterval(-120),
        lastSuccessAt: now.addingTimeInterval(-120),
        lastFailureAt: nil
    )

    #expect(
        ConversationRefreshPolicy.shouldRefresh(
            reason: .appBecameActive,
            hasCachedConversation: true,
            state: state,
            now: now
        )
    )
}

@Test func foregroundRefreshesEvenAfterRecentFailure() {
    let now = Date(timeIntervalSince1970: 1_000)
    let state = ConversationRefreshState(
        lastAttemptAt: now.addingTimeInterval(-1),
        lastSuccessAt: now.addingTimeInterval(-120),
        lastFailureAt: now.addingTimeInterval(-1)
    )

    // appBecameActive always refreshes — the retry logic in
    // loadLatestConversation handles transient failures.
    #expect(
        ConversationRefreshPolicy.shouldRefresh(
            reason: .appBecameActive,
            hasCachedConversation: true,
            state: state,
            now: now
        )
    )
}

@Test func chatVisibilityRefreshesAfterThrottleWindow() {
    let now = Date(timeIntervalSince1970: 1_000)
    let state = ConversationRefreshState(
        lastAttemptAt: now.addingTimeInterval(-10),
        lastSuccessAt: now.addingTimeInterval(-10),
        lastFailureAt: nil
    )

    #expect(
        ConversationRefreshPolicy.shouldRefresh(
            reason: .chatBecameVisible,
            hasCachedConversation: true,
            state: state,
            now: now
        )
    )
}

@Test func chatVisibilitySkipsImmediateDuplicateRefresh() {
    let now = Date(timeIntervalSince1970: 1_000)
    let state = ConversationRefreshState(
        lastAttemptAt: now.addingTimeInterval(-1),
        lastSuccessAt: now.addingTimeInterval(-1),
        lastFailureAt: nil
    )

    #expect(
        ConversationRefreshPolicy.shouldRefresh(
            reason: .chatBecameVisible,
            hasCachedConversation: true,
            state: state,
            now: now
        ) == false
    )
}
