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

@Test func foregroundSkipsRecentSuccessfulRefresh() {
    let now = Date(timeIntervalSince1970: 1_000)
    let state = ConversationRefreshState(
        lastAttemptAt: now.addingTimeInterval(-5),
        lastSuccessAt: now.addingTimeInterval(-5),
        lastFailureAt: nil
    )

    #expect(
        ConversationRefreshPolicy.shouldRefresh(
            reason: .appBecameActive,
            hasCachedConversation: true,
            state: state,
            now: now
        ) == false
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

@Test func foregroundBacksOffBrieflyAfterFailure() {
    let now = Date(timeIntervalSince1970: 1_000)
    let state = ConversationRefreshState(
        lastAttemptAt: now.addingTimeInterval(-1),
        lastSuccessAt: now.addingTimeInterval(-120),
        lastFailureAt: now.addingTimeInterval(-1)
    )

    #expect(
        ConversationRefreshPolicy.shouldRefresh(
            reason: .appBecameActive,
            hasCachedConversation: true,
            state: state,
            now: now
        ) == false
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
