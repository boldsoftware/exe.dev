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
            // Always refresh when the app comes to the foreground.
            // The conversation list may have changed while backgrounded
            // (e.g. new conversations created from the web UI) and the
            // API call is cheap.
            return true
        }
    }

}
