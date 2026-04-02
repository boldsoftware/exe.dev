import Foundation

nonisolated struct SharedConversationTarget: Codable, Equatable, Sendable {
    let conversationID: String?
    let prefersNewConversation: Bool
    let updatedAt: Date
}

nonisolated enum SharedConversationStore {
    static func loadTarget(for vmName: String) -> SharedConversationTarget? {
        loadTargets()[vmName]
    }

    static func saveConversationID(_ conversationID: String, for vmName: String) {
        updateTarget(
            for: vmName,
            target: SharedConversationTarget(
                conversationID: conversationID,
                prefersNewConversation: false,
                updatedAt: Date()
            )
        )
    }

    static func markNewConversation(for vmName: String) {
        updateTarget(
            for: vmName,
            target: SharedConversationTarget(
                conversationID: nil,
                prefersNewConversation: true,
                updatedAt: Date()
            )
        )
    }

    private static func updateTarget(for vmName: String, target: SharedConversationTarget) {
        var targets = loadTargets()
        targets[vmName] = target
        saveTargets(targets)
    }

    private static func loadTargets() -> [String: SharedConversationTarget] {
        guard let data = SharedAppConfiguration.sharedDefaults.data(
            forKey: SharedAppConfiguration.conversationTargetsDefaultsKey
        ) else {
            return [:]
        }
        return (try? JSONDecoder().decode([String: SharedConversationTarget].self, from: data)) ?? [:]
    }

    private static func saveTargets(_ targets: [String: SharedConversationTarget]) {
        guard let data = try? JSONEncoder().encode(targets) else { return }
        SharedAppConfiguration.sharedDefaults.set(
            data,
            forKey: SharedAppConfiguration.conversationTargetsDefaultsKey
        )
    }
}
