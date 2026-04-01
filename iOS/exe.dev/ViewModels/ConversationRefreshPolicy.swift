import Foundation

enum ConversationRefreshReason: Sendable {
    case appBecameActive
    case chatBecameVisible
}

struct ConversationRefreshState: Sendable, Equatable {
    var lastAttemptAt: Date?
    var lastSuccessAt: Date?
    var lastFailureAt: Date?
}

nonisolated enum ConversationRefreshPolicy {
    static let staleAfter: TimeInterval = 30
    static let retryAfterFailure: TimeInterval = 3
    static let chatVisibilityThrottle: TimeInterval = 2

    static func shouldRefresh(
        reason: ConversationRefreshReason,
        hasCachedConversation: Bool,
        state: ConversationRefreshState,
        now: Date = Date()
    ) -> Bool {
        if !hasCachedConversation {
            return true
        }

        switch reason {
        case .chatBecameVisible:
            guard let lastAttemptAt = state.lastAttemptAt else { return true }
            return now.timeIntervalSince(lastAttemptAt) >= chatVisibilityThrottle

        case .appBecameActive:
            if hasRecentFailure(state: state) {
                guard let lastFailureAt = state.lastFailureAt else { return false }
                return now.timeIntervalSince(lastFailureAt) >= retryAfterFailure
            }

            guard let lastSuccessAt = state.lastSuccessAt else { return true }
            return now.timeIntervalSince(lastSuccessAt) >= staleAfter
        }
    }

    private static func hasRecentFailure(state: ConversationRefreshState) -> Bool {
        guard let lastFailureAt = state.lastFailureAt else { return false }
        guard let lastSuccessAt = state.lastSuccessAt else { return true }
        return lastFailureAt > lastSuccessAt
    }
}
