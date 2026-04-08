import Foundation
import SwiftData

enum SendStatus: Equatable {
    case sending
    case sent // API accepted; awaiting SSE confirmation
    case failed(String)
}

struct PendingMessage: Identifiable {
    let id = UUID()
    let text: String
    var status: SendStatus = .sending
    let createdAt = Date()
}

@MainActor @Observable
final class ChannelViewModel {
    let vmName: String
    var shelleyURL: String?
    private(set) var conversationID: String?
    private(set) var isLoading = true
    private(set) var error: String?
    private(set) var pendingMessages: [PendingMessage] = []
    private(set) var messages: [StoredMessage] = []
    private(set) var isWorking = false

    private let api: APIClient
    private let syncEngine: SyncEngine
    private let modelContext: ModelContext
    private var observer: (any NSObjectProtocol)?
    private var isDraining = false
    private var isRefreshingConversation = false
    private var lastRefreshAttemptAt: Date?
    private var lastRefreshSuccessAt: Date?
    private var lastRefreshFailureAt: Date?
    /// Incremented each time we start an SSE stream; onDisappear only stops the
    /// stream if the generation hasn't advanced (prevents a late stop from
    /// killing a freshly started stream after a quick navigate-away-and-back).
    private var streamGeneration: UInt64 = 0

    init(vmName: String, shelleyURL: String?, api: APIClient, syncEngine: SyncEngine) {
        self.vmName = vmName
        self.shelleyURL = shelleyURL
        self.api = api
        self.syncEngine = syncEngine
        self.modelContext = ModelContext(syncEngine.modelContainer)

        observer = NotificationCenter.default.addObserver(
            forName: .syncEngineDidSave,
            object: nil,
            queue: .main
        ) { [weak self] notification in
            MainActor.assumeIsolated {
                self?.enqueueSaveNotification(notification)
            }
        }
    }

    deinit {
        MainActor.assumeIsolated {
            if let observer {
                NotificationCenter.default.removeObserver(observer)
            }
        }
    }

    func loadLatestConversation(
        reason: ConversationRefreshReason = .chatBecameVisible,
        forceRefresh: Bool = false
    ) async {
        guard let shelleyURL else {
            isLoading = false
            error = "This VM does not have Shelley enabled."
            return
        }

        error = nil
        await showCachedConversationIfAvailable(shelleyURL: shelleyURL)

        let hasCachedConversation = conversationID != nil
        let refreshState = ConversationRefreshState(
            lastAttemptAt: lastRefreshAttemptAt,
            lastSuccessAt: lastRefreshSuccessAt,
            lastFailureAt: lastRefreshFailureAt
        )

        if !forceRefresh,
           !ConversationRefreshPolicy.shouldRefresh(
                reason: reason,
                hasCachedConversation: hasCachedConversation,
                state: refreshState
           ) {
            isLoading = false
            return
        }

        guard !isRefreshingConversation else { return }
        isRefreshingConversation = true
        lastRefreshAttemptAt = Date()
        isLoading = !hasCachedConversation
        defer {
            isRefreshingConversation = false
        }

        // Refresh from API with retries — shelley may still be starting up.
        let delays: [Duration] = [.seconds(0), .seconds(2), .seconds(3), .seconds(5)]
        for (attempt, delay) in delays.enumerated() {
            if attempt > 0 {
                try? await Task.sleep(for: delay)
                if Task.isCancelled { return }
            }

            do {
                try await refreshFromAPI(shelleyURL: shelleyURL)
                lastRefreshSuccessAt = Date()
                lastRefreshFailureAt = nil
                return // success
            } catch {
                if attempt == delays.count - 1 {
                    lastRefreshFailureAt = Date()
                    // Final attempt failed — show the error.
                    self.error = error.localizedDescription
                    isLoading = false
                }
                // Otherwise retry silently.
            }
        }
    }

    private func refreshFromAPI(shelleyURL: String) async throws {
        let conversations = try await api.listConversations(shelleyURL: shelleyURL)
        if let latest = conversations.first {
            let previousID = conversationID
            if latest.conversationID != conversationID {
                if let old = conversationID {
                    await syncEngine.stopStream(conversationID: old)
                }
                conversationID = latest.conversationID
                messages = []
                isWorking = latest.working ?? false
            }
            SharedConversationStore.saveConversationID(latest.conversationID, for: vmName)
            do {
                try await syncEngine.loadConversation(
                    api: api, shelleyURL: shelleyURL,
                    conversationID: latest.conversationID, vmName: vmName
                )
            } catch {
                // Roll back conversationID if we had no prior conversation,
                // so the error is visible instead of an empty message list.
                if previousID == nil {
                    conversationID = nil
                }
                throw error
            }
            isLoading = false
            // fetchMessages() will be called by the save notification
            streamGeneration &+= 1
            await syncEngine.startStream(
                api: api, shelleyURL: shelleyURL,
                conversationID: latest.conversationID, vmName: vmName
            )
        } else {
            if let conversationID {
                await syncEngine.stopStream(conversationID: conversationID)
            }
            conversationID = nil
            messages = []
            isWorking = false
            isLoading = false
        }
    }

    /// Non-blocking send. Appends to the pending queue and returns immediately.
    func send(text: String) {
        guard shelleyURL != nil else { return }
        pendingMessages.append(PendingMessage(text: text))
        drainSendQueue()
    }

    func retry(id: UUID) {
        guard let idx = pendingMessages.firstIndex(where: { $0.id == id }) else { return }
        pendingMessages[idx].status = .sending
        drainSendQueue()
    }

    func newConversation() {
        if let conversationID {
            Task { await syncEngine.stopStream(conversationID: conversationID) }
        }
        conversationID = nil
        SharedConversationStore.markNewConversation(for: vmName)
        pendingMessages = []
        messages = []
        isWorking = false
        error = nil
    }

    func onDisappear() {
        guard let conversationID else { return }
        let gen = streamGeneration
        Task { [weak self] in
            guard let self, self.streamGeneration == gen else { return }
            await syncEngine.stopStream(conversationID: conversationID)
        }
    }

    var hasPendingSends: Bool {
        pendingMessages.contains { $0.status == .sending || $0.status == .sent }
    }

    // MARK: - Private

    private func drainSendQueue() {
        guard !isDraining else { return }
        isDraining = true
        Task { await drainLoop() }
    }

    private func drainLoop() async {
        guard let shelleyURL else {
            isDraining = false
            return
        }

        while let idx = pendingMessages.firstIndex(where: { $0.status == .sending }) {
            let msg = pendingMessages[idx]
            do {
                if let conversationID {
                    _ = try await api.sendMessage(
                        shelleyURL: shelleyURL,
                        conversationID: conversationID,
                        message: msg.text
                    )
                } else {
                    let response = try await api.newConversation(
                        shelleyURL: shelleyURL,
                        message: msg.text
                    )
                    if let newID = response.conversationID {
                        conversationID = newID
                        SharedConversationStore.saveConversationID(newID, for: vmName)
                        try await syncEngine.loadConversation(
                            api: api, shelleyURL: shelleyURL,
                            conversationID: newID, vmName: vmName
                        )
                        streamGeneration &+= 1
                        await syncEngine.startStream(
                            api: api, shelleyURL: shelleyURL,
                            conversationID: newID, vmName: vmName
                        )
                    }
                }
                // Mark sent so the loop doesn't re-send this message.
                // The SSE stream will deliver the confirmed message and
                // reconcilePendingMessages() will remove this entry.
                if let idx = pendingMessages.firstIndex(where: { $0.id == msg.id }) {
                    pendingMessages[idx].status = .sent
                }
            } catch {
                // Mark failed; stop draining so subsequent queued messages wait.
                if let idx = pendingMessages.firstIndex(where: { $0.id == msg.id }) {
                    pendingMessages[idx].status = .failed(error.localizedDescription)
                }
                break
            }
        }

        isDraining = false
    }

    private func fetchMessages() {
        guard let conversationID else {
            messages = []
            isWorking = false
            return
        }

        let descriptor = FetchDescriptor<StoredMessage>(
            predicate: #Predicate { $0.conversationID == conversationID },
            sortBy: [SortDescriptor(\.sequenceID)]
        )
        messages = (try? modelContext.fetch(descriptor)) ?? []

        let convDescriptor = FetchDescriptor<StoredConversation>(
            predicate: #Predicate { $0.conversationID == conversationID }
        )
        isWorking = (try? modelContext.fetch(convDescriptor).first?.working) ?? false

        reconcilePendingMessages()
    }

    private func showCachedConversationIfAvailable(shelleyURL: String) async {
        guard let cachedID = await syncEngine.latestConversationID(for: vmName) else { return }

        if cachedID != conversationID {
            conversationID = cachedID
            messages = []
        }
        SharedConversationStore.saveConversationID(cachedID, for: vmName)

        isLoading = false
        fetchMessages()
        streamGeneration &+= 1
        await syncEngine.startStream(
            api: api,
            shelleyURL: shelleyURL,
            conversationID: cachedID,
            vmName: vmName
        )
    }

    private func fetchMessages(messageIDs: [String], conversationID: String) -> [StoredMessage] {
        let uniqueIDs = orderedUniqueMessageIDs(messageIDs)
        guard !uniqueIDs.isEmpty else { return [] }

        var fetched: [StoredMessage] = []
        fetched.reserveCapacity(uniqueIDs.count)

        for messageID in uniqueIDs {
            let id = messageID
            let activeConversationID = conversationID
            let descriptor = FetchDescriptor<StoredMessage>(
                predicate: #Predicate {
                    $0.messageID == id && $0.conversationID == activeConversationID
                }
            )
            if let message = try? modelContext.fetch(descriptor).first {
                fetched.append(message)
            }
        }

        return fetched
    }

    private func enqueueSaveNotification(_ notification: Notification) {
        guard let conversationID else { return }
        guard let save = saveNotification(from: notification, activeConversationID: conversationID) else {
            return
        }
        let activeConversationID = conversationID

        Task { @MainActor [weak self, save, activeConversationID] in
            guard let self, self.conversationID == activeConversationID else { return }
            self.handleSaveNotification(save)
        }
    }

    private func handleSaveNotification(_ save: SaveNotification) {
        if case .newerConversationAvailable = save {
            Task {
                await loadLatestConversation(reason: .chatBecameVisible, forceRefresh: true)
            }
            return
        }

        guard let conversationID else { return }

        switch save {
        case .fullReload:
            fetchMessages()

        case .delta(let delta):
            if let working = delta.working {
                isWorking = working
            }

            let uniqueMessageIDs = orderedUniqueMessageIDs(delta.messageIDs)
            guard !uniqueMessageIDs.isEmpty else {
                reconcilePendingMessages()
                return
            }

            if ConversationDeltaReducer.shouldReload(
                hasSnapshot: !messages.isEmpty,
                changedMessageCount: uniqueMessageIDs.count
            ) {
                fetchMessages()
                return
            }

            let changedMessages = fetchMessages(
                messageIDs: uniqueMessageIDs,
                conversationID: conversationID
            )

            guard changedMessages.count == uniqueMessageIDs.count else {
                fetchMessages()
                return
            }

            messages = ConversationDeltaReducer.merge(
                current: messages,
                changed: changedMessages,
                id: \.messageID,
                areInIncreasingOrder: Self.isMessageBefore(_:_:)
            )
            reconcilePendingMessages()

        case .newerConversationAvailable:
            break // Handled above, before the guard
        }
    }

    private enum SaveNotification: Sendable {
        case fullReload
        case delta(ConversationDelta)
        case newerConversationAvailable
    }

    private struct ConversationDelta: Sendable {
        let messageIDs: [String]
        let working: Bool?
    }

    private func saveNotification(
        from notification: Notification,
        activeConversationID: String
    ) -> SaveNotification? {
        guard let userInfo = notification.userInfo,
              let kindRaw = userInfo[SyncEngineSaveNotificationUserInfoKey.kind] as? String,
              let kind = SyncEngineSaveNotificationKind(rawValue: kindRaw)
        else {
            return .fullReload
        }

        switch kind {
        case .vms:
            return nil
        case .conversation:
            guard let changedConversationID =
                userInfo[SyncEngineSaveNotificationUserInfoKey.conversationID] as? String
            else {
                return .fullReload
            }
            guard changedConversationID == activeConversationID else { return nil }
            return .delta(
                ConversationDelta(
                    messageIDs:
                        userInfo[SyncEngineSaveNotificationUserInfoKey.messageIDs] as? [String] ?? [],
                    working: userInfo[SyncEngineSaveNotificationUserInfoKey.working] as? Bool
                )
            )
        case .conversationListChanged:
            return .newerConversationAvailable
        }
    }

    private func orderedUniqueMessageIDs(_ messageIDs: [String]) -> [String] {
        var seen = Set<String>()
        var unique: [String] = []
        unique.reserveCapacity(messageIDs.count)

        for messageID in messageIDs where seen.insert(messageID).inserted {
            unique.append(messageID)
        }

        return unique
    }

    private static func isMessageBefore(_ lhs: StoredMessage, _ rhs: StoredMessage) -> Bool {
        if lhs.sequenceID != rhs.sequenceID {
            return lhs.sequenceID < rhs.sequenceID
        }
        if lhs.createdAt != rhs.createdAt {
            return lhs.createdAt < rhs.createdAt
        }
        return lhs.messageID < rhs.messageID
    }

    /// Remove pending messages that the server has confirmed via SSE.
    /// Matches by text against the latest user messages from the server.
    private func reconcilePendingMessages() {
        guard !pendingMessages.isEmpty else { return }

        // Collect the most recent user message texts (up to pending count)
        let userMessages = messages.filter { $0.type == "user" }.suffix(pendingMessages.count)
        var confirmedTexts = userMessages.map { $0.displayText }

        pendingMessages.removeAll { pending in
            guard pending.status == .sending || pending.status == .sent else { return false }
            if let idx = confirmedTexts.firstIndex(of: pending.text) {
                confirmedTexts.remove(at: idx)
                return true
            }
            return false
        }
    }
}
