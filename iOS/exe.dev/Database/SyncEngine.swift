import Foundation
import SwiftData
import UIKit

@ModelActor
actor SyncEngine {
    private var streamTasks: [String: Task<Void, Never>] = [:]
    private enum SaveNotificationKind: Sendable {
        case vms
        case conversation(
            conversationID: String,
            messageIDs: [String],
            working: Bool?
        )
        case conversationListChanged(newerConversationID: String)
    }

    private struct ConversationUpsertResult {
        let didChange: Bool
        let workingChanged: Bool
    }

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

        try saveAndNotify(kind: .vms)
        updateAppBadge()
    }

    // MARK: - Conversation Load

    func loadConversation(api: APIClient, shelleyURL: String,
                          conversationID: String, vmName: String) async throws {
        let data = try await api.getConversation(shelleyURL: shelleyURL, id: conversationID)

        let working = data.conversationState?.working ?? false
        var didChange = false
        var workingChanged = false

        if let conv = data.conversation {
            let result = upsertConversation(conv, vmName: vmName, working: working)
            didChange = didChange || result.didChange
            workingChanged = result.workingChanged
        }

        var changedMessageIDs: [String] = []
        if let messages = data.messages {
            for msg in messages {
                if upsertMessage(msg) {
                    changedMessageIDs.append(msg.messageID)
                    didChange = true
                }
            }
        }

        if didChange {
            try saveAndNotify(
                kind: .conversation(
                    conversationID: conversationID,
                    messageIDs: orderedUniqueStrings(changedMessageIDs),
                    working: workingChanged ? working : nil
                )
            )
        }
    }

    // MARK: - Cached Conversation Lookup

    func latestConversationID(for vmName: String) -> String? {
        let descriptor = FetchDescriptor<StoredConversation>(
            predicate: #Predicate { $0.vmName == vmName },
            sortBy: [SortDescriptor(\.updatedAt, order: .reverse)]
        )
        guard let stored = try? modelContext.fetch(descriptor), !stored.isEmpty else { return nil }
        return stored.first(where: { !$0.archived })?.conversationID ?? stored.first?.conversationID
    }

    // MARK: - SSE Streaming

    func startStream(api: APIClient, shelleyURL: String,
                     conversationID: String, vmName: String) {
        stopStream(conversationID: conversationID)

        streamTasks[conversationID] = Task {
            let lastSeq = lastSequenceID(for: conversationID)
            let stream = await api.streamConversation(
                shelleyURL: shelleyURL,
                conversationID: conversationID,
                lastSequenceID: lastSeq
            )

            do {
                var pending: [ShelleyMessage] = []
                var pendingWorking: Bool?
                var lastFlush = ContinuousClock.now

                for try await event in stream {
                    if Task.isCancelled { break }

                    // When the server reports a different conversation was
                    // created or updated, tell the UI so it can switch.
                    if let listUpdate = event.conversationListUpdate,
                       listUpdate.type == "update",
                       let updated = listUpdate.conversation,
                       !updated.archived,
                       updated.parentConversationID == nil,
                       updated.conversationID != conversationID,
                       updated.updatedAt > (self.conversationUpdatedAt(for: conversationID) ?? .distantPast) {
                        SyncEngineSaveNotificationDispatcher.dispatch(
                            .conversationListChanged(newerConversationID: updated.conversationID)
                        )
                    }

                    if let state = event.conversationState {
                        pendingWorking = state.working
                    }

                    if event.heartbeat == true {
                        if let working = pendingWorking,
                           updateWorking(conversationID: conversationID, working: working) {
                            try saveAndNotify(
                                kind: .conversation(
                                    conversationID: conversationID,
                                    messageIDs: [],
                                    working: working
                                )
                            )
                        }
                        pendingWorking = nil
                        continue
                    }

                    if let msgs = event.messages {
                        pending.append(contentsOf: msgs)
                    }

                    let now = ContinuousClock.now
                    if !pending.isEmpty &&
                        (now - lastFlush > .milliseconds(100) || pending.count >= 20) {
                        var changedMessageIDs: [String] = []
                        for msg in pending where upsertMessage(msg) {
                            changedMessageIDs.append(msg.messageID)
                        }

                        var changedWorking: Bool?
                        if let working = pendingWorking,
                           updateWorking(conversationID: conversationID, working: working) {
                            changedWorking = working
                        }

                        if !changedMessageIDs.isEmpty || changedWorking != nil {
                            try saveAndNotify(
                                kind: .conversation(
                                    conversationID: conversationID,
                                    messageIDs: orderedUniqueStrings(changedMessageIDs),
                                    working: changedWorking
                                )
                            )
                        }

                        pendingWorking = nil
                        pending.removeAll(keepingCapacity: true)
                        lastFlush = now
                    }
                }

                // Final flush
                if !pending.isEmpty || pendingWorking != nil {
                    var changedMessageIDs: [String] = []
                    for msg in pending where upsertMessage(msg) {
                        changedMessageIDs.append(msg.messageID)
                    }

                    var changedWorking: Bool?
                    if let working = pendingWorking,
                       updateWorking(conversationID: conversationID, working: working) {
                        changedWorking = working
                    }

                    if !changedMessageIDs.isEmpty || changedWorking != nil {
                        try? saveAndNotify(
                            kind: .conversation(
                                conversationID: conversationID,
                                messageIDs: orderedUniqueStrings(changedMessageIDs),
                                working: changedWorking
                            )
                        )
                    }
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
        try? saveAndNotify(kind: .vms)
    }

    func creatingVMNames() -> [String] {
        let vms = (try? modelContext.fetch(FetchDescriptor<StoredVM>())) ?? []
        return vms.filter(\.isCreating).map(\.vmName).sorted()
    }

    func vmListItems() -> [VMListItem] {
        let descriptor = FetchDescriptor<StoredVM>(
            sortBy: [SortDescriptor(\.vmName)]
        )
        let vms = (try? modelContext.fetch(descriptor)) ?? []
        return vms.map(VMListItem.init(from:))
    }

    func vmListItem(named vmName: String) -> VMListItem? {
        let name = vmName
        let predicate = #Predicate<StoredVM> { $0.vmName == name }
        guard let vm = try? modelContext.fetch(FetchDescriptor(predicate: predicate)).first else {
            return nil
        }
        return VMListItem(from: vm)
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
        try? saveAndNotify(kind: .vms)
        updateAppBadge()
    }

    func markVMAsRead(vmName: String) {
        let predicate = #Predicate<StoredVM> { $0.vmName == vmName }
        if let vm = try? modelContext.fetch(FetchDescriptor(predicate: predicate)).first {
            vm.lastViewedAt = Date()
            vm.unreadCount = 0
            try? saveAndNotify(kind: .vms)
            updateAppBadge()
        }
    }

    // MARK: - Private Helpers

    private func updateAppBadge() {
        let vms = (try? modelContext.fetch(FetchDescriptor<StoredVM>())) ?? []
        let total = vms.reduce(0) { $0 + $1.unreadCount }
        let badgeCount = total
        // Use applicationIconBadgeNumber — it works without notification
        // authorization, unlike UNUserNotificationCenter.setBadgeCount().
        Task { @MainActor in
            UIApplication.shared.applicationIconBadgeNumber = badgeCount
        }
    }

    private func saveAndNotify(kind: SaveNotificationKind) throws {
        try modelContext.save()
        SyncEngineSaveNotificationDispatcher.dispatch(notificationPayload(for: kind))
    }

    private func notificationPayload(for kind: SaveNotificationKind)
        -> SyncEngineSaveNotificationPayload {
        switch kind {
        case .vms:
            return .vms
        case .conversation(let conversationID, let messageIDs, let working):
            return .conversation(
                conversationID: conversationID,
                messageIDs: messageIDs,
                working: working
            )
        case .conversationListChanged(let newerConversationID):
            return .conversationListChanged(newerConversationID: newerConversationID)
        }
    }

    private func conversationUpdatedAt(for conversationID: String) -> Date? {
        let id = conversationID
        let predicate = #Predicate<StoredConversation> { $0.conversationID == id }
        return try? modelContext.fetch(FetchDescriptor(predicate: predicate)).first?.updatedAt
    }

    private func lastSequenceID(for conversationID: String) -> Int64? {
        var descriptor = FetchDescriptor<StoredMessage>(
            predicate: #Predicate { $0.conversationID == conversationID },
            sortBy: [SortDescriptor(\.sequenceID, order: .reverse)]
        )
        descriptor.fetchLimit = 1
        return try? modelContext.fetch(descriptor).first?.sequenceID
    }

    private func upsertMessage(_ msg: ShelleyMessage) -> Bool {
        let id = msg.messageID
        let predicate = #Predicate<StoredMessage> { $0.messageID == id }
        if let existing = try? modelContext.fetch(FetchDescriptor(predicate: predicate)).first {
            guard existing.sequenceID != msg.sequenceID ||
                existing.type != msg.type ||
                existing.llmData != msg.llmData ||
                existing.userData != msg.userData ||
                existing.usageData != msg.usageData ||
                existing.createdAt != msg.createdAt ||
                existing.displayData != msg.displayData ||
                existing.endOfTurn != msg.endOfTurn ||
                existing.displayText != msg.displayText ||
                existing.isToolUse != msg.isToolUse ||
                existing.toolName != msg.toolName ||
                existing.toolInputSummary != msg.toolInputSummary ||
                existing.toolUseID != msg.toolUseID ||
                existing.toolResultText != msg.toolResultText ||
                existing.screenshotPath != msg.screenshotPath
            else {
                return false
            }
            existing.update(from: msg)
            return true
        } else {
            modelContext.insert(StoredMessage(from: msg))
            return true
        }
    }

    private func upsertConversation(
        _ conv: Conversation,
        vmName: String,
        working: Bool
    ) -> ConversationUpsertResult {
        let id = conv.conversationID
        let predicate = #Predicate<StoredConversation> { $0.conversationID == id }
        if let existing = try? modelContext.fetch(FetchDescriptor(predicate: predicate)).first {
            let didChange = existing.slug != conv.slug ||
                existing.userInitiated != conv.userInitiated ||
                existing.updatedAt != conv.updatedAt ||
                existing.cwd != conv.cwd ||
                existing.archived != conv.archived ||
                existing.parentConversationID != conv.parentConversationID ||
                existing.model != conv.model ||
                existing.working != working
            let workingChanged = existing.working != working

            guard didChange else {
                return ConversationUpsertResult(didChange: false, workingChanged: false)
            }

            existing.update(from: conv, working: working)
            return ConversationUpsertResult(didChange: true, workingChanged: workingChanged)
        } else {
            modelContext.insert(StoredConversation(from: conv, vmName: vmName, working: working))
            return ConversationUpsertResult(didChange: true, workingChanged: true)
        }
    }

    private func updateWorking(conversationID: String, working: Bool) -> Bool {
        let predicate = #Predicate<StoredConversation> { $0.conversationID == conversationID }
        if let existing = try? modelContext.fetch(FetchDescriptor(predicate: predicate)).first {
            guard existing.working != working else { return false }
            existing.working = working
            return true
        }
        return false
    }

    private func orderedUniqueStrings(_ values: [String]) -> [String] {
        var seen = Set<String>()
        var unique: [String] = []
        unique.reserveCapacity(values.count)

        for value in values where seen.insert(value).inserted {
            unique.append(value)
        }

        return unique
    }
}
