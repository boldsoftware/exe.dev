import Foundation
import SwiftData

enum SendStatus: Equatable {
    case sending
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
    let shelleyURL: String?
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
        ) { [weak self] _ in
            MainActor.assumeIsolated {
                self?.fetchMessages()
            }
        }
    }

    func loadLatestConversation() async {
        guard let shelleyURL else {
            isLoading = false
            error = "This VM does not have Shelley enabled."
            return
        }

        error = nil
        isLoading = conversationID == nil

        // Show cached conversation immediately if available
        if let cachedID = await syncEngine.latestConversationID(for: vmName) {
            conversationID = cachedID
            isLoading = false
            fetchMessages()
            await syncEngine.startStream(
                api: api, shelleyURL: shelleyURL,
                conversationID: cachedID, vmName: vmName
            )
        }

        // Refresh from API
        do {
            let conversations = try await api.listConversations(shelleyURL: shelleyURL)
            if let latest = conversations.first {
                let previousID = conversationID
                if latest.conversationID != conversationID {
                    if let old = conversationID {
                        await syncEngine.stopStream(conversationID: old)
                    }
                    conversationID = latest.conversationID
                }
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
                await syncEngine.startStream(
                    api: api, shelleyURL: shelleyURL,
                    conversationID: latest.conversationID, vmName: vmName
                )
            } else if conversationID == nil {
                isLoading = false
            }
        } catch {
            self.error = error.localizedDescription
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
        pendingMessages = []
        messages = []
        isWorking = false
        error = nil
    }

    func onDisappear() {
        if let conversationID {
            Task { await syncEngine.stopStream(conversationID: conversationID) }
        }
    }

    var hasPendingSends: Bool {
        pendingMessages.contains { $0.status == .sending }
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
                        try await syncEngine.loadConversation(
                            api: api, shelleyURL: shelleyURL,
                            conversationID: newID, vmName: vmName
                        )
                        await syncEngine.startStream(
                            api: api, shelleyURL: shelleyURL,
                            conversationID: newID, vmName: vmName
                        )
                    }
                }
                // Don't remove yet — the SSE stream will deliver the real message,
                // and reconcilePendingMessages() will remove this one.
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

    /// Remove pending messages that the server has confirmed via SSE.
    /// Matches by text against the latest user messages from the server.
    private func reconcilePendingMessages() {
        guard !pendingMessages.isEmpty else { return }

        // Collect the most recent user message texts (up to pending count)
        let userMessages = messages.filter { $0.type == "user" }.suffix(pendingMessages.count)
        var confirmedTexts = userMessages.map { $0.displayText }

        pendingMessages.removeAll { pending in
            guard pending.status == .sending else { return false }
            if let idx = confirmedTexts.firstIndex(of: pending.text) {
                confirmedTexts.remove(at: idx)
                return true
            }
            return false
        }
    }
}
