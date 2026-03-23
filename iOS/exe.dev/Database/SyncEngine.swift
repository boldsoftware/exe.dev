import Foundation
import SwiftData

extension Notification.Name {
    static let syncEngineDidSave = Notification.Name("SyncEngineDidSave")
}

@ModelActor
actor SyncEngine {
    private var streamTasks: [String: Task<Void, Never>] = [:]

    // MARK: - VM Sync

    func refreshVMs(api: APIClient) async throws {
        let vms = try await api.listVMs()
        let existingNames = Set(vms.map(\.vmName))

        let stored = try modelContext.fetch(FetchDescriptor<StoredVM>())
        let storedByName = Dictionary(uniqueKeysWithValues: stored.map { ($0.vmName, $0) })

        for vm in stored where !existingNames.contains(vm.vmName) {
            // Don't delete placeholders for VMs still being created — the server
            // may not have inserted the DB row yet.
            if vm.isCreating { continue }
            modelContext.delete(vm)
        }

        for vm in vms {
            if let existing = storedByName[vm.vmName] {
                existing.update(from: vm)
            } else {
                modelContext.insert(StoredVM(from: vm))
            }
        }

        try saveAndNotify()
    }

    // MARK: - Conversation Load

    func loadConversation(api: APIClient, shelleyURL: String,
                          conversationID: String, vmName: String) async throws {
        let data = try await api.getConversation(shelleyURL: shelleyURL, id: conversationID)

        if let conv = data.conversation {
            upsertConversation(conv, vmName: vmName,
                               working: data.conversationState?.working ?? false)
        }

        if let messages = data.messages {
            for msg in messages {
                upsertMessage(msg)
            }
        }

        try saveAndNotify()
    }

    // MARK: - Cached Conversation Lookup

    func latestConversationID(for vmName: String) -> String? {
        var descriptor = FetchDescriptor<StoredConversation>(
            predicate: #Predicate { $0.vmName == vmName },
            sortBy: [SortDescriptor(\.updatedAt, order: .reverse)]
        )
        descriptor.fetchLimit = 1
        return try? modelContext.fetch(descriptor).first?.conversationID
    }

    // MARK: - SSE Streaming

    func startStream(api: APIClient, shelleyURL: String,
                     conversationID: String, vmName: String) {
        stopStream(conversationID: conversationID)

        streamTasks[conversationID] = Task {
            let lastSeq = lastSequenceID(for: conversationID)
            let stream = await MainActor.run {
                api.streamConversation(
                    shelleyURL: shelleyURL,
                    conversationID: conversationID,
                    lastSequenceID: lastSeq
                )
            }

            do {
                var pending: [ShelleyMessage] = []
                var pendingWorking: Bool?
                var lastFlush = ContinuousClock.now

                for try await event in stream {
                    if Task.isCancelled { break }

                    if let state = event.conversationState {
                        pendingWorking = state.working
                    }

                    if event.heartbeat == true {
                        if let working = pendingWorking {
                            updateWorking(conversationID: conversationID, working: working)
                            try saveAndNotify()
                            pendingWorking = nil
                        }
                        continue
                    }

                    if let msgs = event.messages {
                        pending.append(contentsOf: msgs)
                    }

                    let now = ContinuousClock.now
                    if !pending.isEmpty &&
                        (now - lastFlush > .milliseconds(100) || pending.count >= 20) {
                        for msg in pending { upsertMessage(msg) }
                        if let working = pendingWorking {
                            updateWorking(conversationID: conversationID, working: working)
                            pendingWorking = nil
                        }
                        try saveAndNotify()
                        pending.removeAll(keepingCapacity: true)
                        lastFlush = now
                    }
                }

                // Final flush
                if !pending.isEmpty || pendingWorking != nil {
                    for msg in pending { upsertMessage(msg) }
                    if let working = pendingWorking {
                        updateWorking(conversationID: conversationID, working: working)
                    }
                    try? saveAndNotify()
                }
            } catch {
                if !Task.isCancelled {
                    try? await Task.sleep(for: .seconds(2))
                    if !Task.isCancelled {
                        startStream(api: api, shelleyURL: shelleyURL,
                                    conversationID: conversationID, vmName: vmName)
                    }
                }
            }
        }
    }

    func stopStream(conversationID: String) {
        streamTasks[conversationID]?.cancel()
        streamTasks[conversationID] = nil
    }

    func stopAll() {
        for (_, task) in streamTasks {
            task.cancel()
        }
        streamTasks.removeAll()
    }

    // MARK: - Placeholder VM for creation

    /// Inserts a placeholder VM with status "creating" so it appears in the list immediately.
    func insertCreatingVM(hostname: String) {
        let name = hostname
        let predicate = #Predicate<StoredVM> { $0.vmName == name }
        if (try? modelContext.fetch(FetchDescriptor(predicate: predicate)).first) != nil {
            return // Already exists
        }
        let placeholder = StoredVM(creating: hostname)
        modelContext.insert(placeholder)
        try? saveAndNotify()
    }

    // MARK: - Unread Tracking

    struct VMUnreadInfo: Sendable {
        let vmName: String
        let shelleyURL: String
        let lastViewedAt: Date?
    }

    /// Returns a lightweight snapshot of running VMs that have shelley — no networking.
    func runningVMsWithShelley() -> [VMUnreadInfo] {
        let vms = (try? modelContext.fetch(FetchDescriptor<StoredVM>())) ?? []
        return vms.compactMap { vm in
            guard vm.isRunning, let url = vm.shelleyURL else { return nil }
            return VMUnreadInfo(vmName: vm.vmName, shelleyURL: url, lastViewedAt: vm.lastViewedAt)
        }
    }

    /// Writes pre-computed unread counts into the database. Negative values are skipped (keep existing).
    func applyUnreadCounts(_ counts: [(String, Int)]) {
        for (vmName, count) in counts {
            guard count >= 0 else { continue }
            let name = vmName
            let predicate = #Predicate<StoredVM> { $0.vmName == name }
            if let vm = try? modelContext.fetch(FetchDescriptor(predicate: predicate)).first {
                vm.unreadCount = count
            }
        }
        try? saveAndNotify()
    }

    func markVMAsRead(vmName: String) {
        let predicate = #Predicate<StoredVM> { $0.vmName == vmName }
        if let vm = try? modelContext.fetch(FetchDescriptor(predicate: predicate)).first {
            vm.lastViewedAt = Date()
            vm.unreadCount = 0
            try? saveAndNotify()
        }
    }

    // MARK: - Private Helpers

    private func saveAndNotify() throws {
        try modelContext.save()
        postSaveNotification()
    }

    nonisolated private func postSaveNotification() {
        NotificationCenter.default.post(name: Notification.Name("SyncEngineDidSave"), object: nil)
    }

    private func lastSequenceID(for conversationID: String) -> Int64? {
        var descriptor = FetchDescriptor<StoredMessage>(
            predicate: #Predicate { $0.conversationID == conversationID },
            sortBy: [SortDescriptor(\.sequenceID, order: .reverse)]
        )
        descriptor.fetchLimit = 1
        return try? modelContext.fetch(descriptor).first?.sequenceID
    }

    private func upsertMessage(_ msg: ShelleyMessage) {
        let id = msg.messageID
        let predicate = #Predicate<StoredMessage> { $0.messageID == id }
        if let existing = try? modelContext.fetch(FetchDescriptor(predicate: predicate)).first {
            existing.update(from: msg)
        } else {
            modelContext.insert(StoredMessage(from: msg))
        }
    }

    private func upsertConversation(_ conv: Conversation, vmName: String, working: Bool) {
        let id = conv.conversationID
        let predicate = #Predicate<StoredConversation> { $0.conversationID == id }
        if let existing = try? modelContext.fetch(FetchDescriptor(predicate: predicate)).first {
            existing.update(from: conv, working: working)
        } else {
            modelContext.insert(StoredConversation(from: conv, vmName: vmName, working: working))
        }
    }

    private func updateWorking(conversationID: String, working: Bool) {
        let predicate = #Predicate<StoredConversation> { $0.conversationID == conversationID }
        if let existing = try? modelContext.fetch(FetchDescriptor(predicate: predicate)).first {
            existing.working = working
        }
    }
}
