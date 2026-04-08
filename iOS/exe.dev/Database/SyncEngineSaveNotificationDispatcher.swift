import Foundation

extension Notification.Name {
    static let syncEngineDidSave = Notification.Name("SyncEngineDidSave")
}

nonisolated enum SyncEngineSaveNotificationKind: String, Sendable {
    case vms
    case conversation
    case conversationListChanged
}

nonisolated enum SyncEngineSaveNotificationUserInfoKey {
    static let kind = "kind"
    static let conversationID = "conversationID"
    static let messageIDs = "messageIDs"
    static let working = "working"
    static let newerConversationID = "newerConversationID"
}

nonisolated struct SyncEngineSaveNotificationPayload: Sendable, Equatable {
    let kind: SyncEngineSaveNotificationKind
    let conversationID: String?
    let messageIDs: [String]
    let working: Bool?
    let newerConversationID: String?

    static let vms = Self(kind: .vms, conversationID: nil, messageIDs: [], working: nil, newerConversationID: nil)

    static func conversation(
        conversationID: String,
        messageIDs: [String],
        working: Bool?
    ) -> Self {
        Self(
            kind: .conversation,
            conversationID: conversationID,
            messageIDs: messageIDs,
            working: working,
            newerConversationID: nil
        )
    }

    static func conversationListChanged(newerConversationID: String) -> Self {
        Self(
            kind: .conversationListChanged,
            conversationID: nil,
            messageIDs: [],
            working: nil,
            newerConversationID: newerConversationID
        )
    }

    var userInfo: [String: Any] {
        var userInfo: [String: Any] = [
            SyncEngineSaveNotificationUserInfoKey.kind: kind.rawValue,
        ]

        if let conversationID {
            userInfo[SyncEngineSaveNotificationUserInfoKey.conversationID] = conversationID
            userInfo[SyncEngineSaveNotificationUserInfoKey.messageIDs] = messageIDs
        }

        if let working {
            userInfo[SyncEngineSaveNotificationUserInfoKey.working] = working
        }

        if let newerConversationID {
            userInfo[SyncEngineSaveNotificationUserInfoKey.newerConversationID] = newerConversationID
        }

        return userInfo
    }
}

nonisolated enum SyncEngineSaveNotificationDispatcher {
    static func dispatch(_ payload: SyncEngineSaveNotificationPayload) {
        DispatchQueue.main.async {
            NotificationCenter.default.post(
                name: .syncEngineDidSave,
                object: nil,
                userInfo: payload.userInfo
            )
        }
    }
}
