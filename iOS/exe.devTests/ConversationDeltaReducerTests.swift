import Foundation
import Testing
@testable import ConversationDeltaReducerSupport

private struct TestMessage: Equatable {
    let messageID: String
    let sequenceID: Int64
    let createdAt: Date
}

@Test func smallDeltaStaysIncremental() {
    #expect(
        ConversationDeltaReducer.shouldReload(
            hasSnapshot: true,
            changedMessageCount: ConversationDeltaReducer.incrementalFetchThreshold
        ) == false
    )
}

@Test func emptySnapshotFallsBackToReload() {
    #expect(
        ConversationDeltaReducer.shouldReload(
            hasSnapshot: false,
            changedMessageCount: 1
        )
    )
}

@Test func oversizedDeltaFallsBackToReload() {
    #expect(
        ConversationDeltaReducer.shouldReload(
            hasSnapshot: true,
            changedMessageCount: ConversationDeltaReducer.incrementalFetchThreshold + 1
        )
    )
}

@Test func mergeUpdatesMessagesAndKeepsSequenceOrder() {
    let t0 = Date(timeIntervalSince1970: 0)
    let t1 = Date(timeIntervalSince1970: 1)
    let t2 = Date(timeIntervalSince1970: 2)

    let current = [
        TestMessage(messageID: "a", sequenceID: 1, createdAt: t0),
        TestMessage(messageID: "c", sequenceID: 3, createdAt: t2),
    ]

    let changed = [
        TestMessage(messageID: "c", sequenceID: 3, createdAt: t2.addingTimeInterval(5)),
        TestMessage(messageID: "b", sequenceID: 2, createdAt: t1),
    ]

    let merged = ConversationDeltaReducer.merge(
        current: current,
        changed: changed,
        id: \.messageID,
        areInIncreasingOrder: { lhs, rhs in
            if lhs.sequenceID != rhs.sequenceID {
                return lhs.sequenceID < rhs.sequenceID
            }
            if lhs.createdAt != rhs.createdAt {
                return lhs.createdAt < rhs.createdAt
            }
            return lhs.messageID < rhs.messageID
        }
    )

    #expect(merged.map(\.messageID) == ["a", "b", "c"])
    #expect(merged.last?.createdAt == t2.addingTimeInterval(5))
}
