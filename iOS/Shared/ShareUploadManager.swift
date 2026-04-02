import Foundation
import OSLog

nonisolated enum ShareUploadError: LocalizedError {
    case missingAuth
    case missingShelleyURL
    case invalidResponse
    case badStatus(Int)

    var errorDescription: String? {
        switch self {
        case .missingAuth:
            return "Open the exe.dev app and sign in first."
        case .missingShelleyURL:
            return "That VM is not ready for Shelley yet."
        case .invalidResponse:
            return "Upload completed, but Shelley returned an unexpected response."
        case .badStatus(let code):
            return "Server returned status \(code)"
        }
    }
}

nonisolated struct PendingShareItem: Codable, Identifiable, Equatable, Sendable {
    nonisolated enum Stage: String, Codable, Sendable {
        case uploadPending
        case uploading
        case uploaded
        case sending
        case failed
    }

    let id: String
    let createdAt: Date
    let vmName: String
    let shelleyURL: String
    let prompt: String
    let filename: String
    let mimeType: String
    let localFileName: String
    let multipartBodyFileName: String
    var targetConversationID: String?
    var prefersNewConversation: Bool?
    var backgroundSessionIdentifier: String?
    var uploadTaskIdentifier: Int?
    var uploadedPath: String?
    var stage: Stage
    var lastError: String?

    var uploadBoundary: String {
        "exe-dev-share-\(id)"
    }

    var localFileURL: URL {
        SharedAppConfiguration.pendingSharesDirectoryURL.appendingPathComponent(localFileName)
    }

    var multipartBodyFileURL: URL {
        SharedAppConfiguration.pendingSharesDirectoryURL.appendingPathComponent(multipartBodyFileName)
    }

    var effectiveBackgroundSessionIdentifier: String {
        backgroundSessionIdentifier ?? SharedAppConfiguration.legacyBackgroundUploadSessionIdentifier
    }

    var effectivePrefersNewConversation: Bool {
        prefersNewConversation ?? false
    }

    var chatMessage: String {
        let remotePath = uploadedPath.map { "[\($0)]" } ?? ""
        let trimmedPrompt = prompt.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmedPrompt.isEmpty {
            return remotePath
        }
        return "\(trimmedPrompt)\n\n\(remotePath)"
    }
}

nonisolated enum PendingShareStore {
    static func allItems() -> [PendingShareItem] {
        guard let data = SharedAppConfiguration.sharedDefaults.data(
            forKey: SharedAppConfiguration.queuedShareItemsDefaultsKey
        ) else {
            return []
        }
        return (try? JSONDecoder().decode([PendingShareItem].self, from: data)) ?? []
    }

    static func item(id: String) -> PendingShareItem? {
        allItems().first(where: { $0.id == id })
    }

    static func upsert(_ item: PendingShareItem) {
        var items = allItems()
        if let index = items.firstIndex(where: { $0.id == item.id }) {
            items[index] = item
        } else {
            items.append(item)
        }
        save(items)
    }

    static func update(id: String, _ mutate: (inout PendingShareItem) -> Void) {
        var items = allItems()
        guard let index = items.firstIndex(where: { $0.id == id }) else { return }
        mutate(&items[index])
        save(items)
    }

    @discardableResult
    static func remove(id: String) -> PendingShareItem? {
        var items = allItems()
        guard let index = items.firstIndex(where: { $0.id == id }) else { return nil }
        let removed = items.remove(at: index)
        save(items)
        return removed
    }

    private static func save(_ items: [PendingShareItem]) {
        guard let data = try? JSONEncoder().encode(items) else { return }
        SharedAppConfiguration.sharedDefaults.set(
            data,
            forKey: SharedAppConfiguration.queuedShareItemsDefaultsKey
        )
    }
}

nonisolated private struct UploadResponse: Decodable {
    let path: String
}

nonisolated final class ShareUploadManager: NSObject, URLSessionDataDelegate, URLSessionTaskDelegate, @unchecked Sendable {
    static let shared = ShareUploadManager()

    private let lock = NSLock()
    private let logger = Logger(
        subsystem: SharedAppConfiguration.loggingSubsystem,
        category: "ShareUpload"
    )
    private var responseDataByTaskKey: [String: Data] = [:]
    private var backgroundCompletionHandlersByIdentifier: [String: () -> Void] = [:]
    private var sessionsByIdentifier: [String: URLSession] = [:]

    func registerBackgroundCompletionHandler(
        _ completionHandler: @escaping () -> Void,
        for identifier: String
    ) {
        lock.lock()
        backgroundCompletionHandlersByIdentifier[identifier] = completionHandler
        lock.unlock()
        logger.debug("Registered background completion handler for \(identifier, privacy: .public)")
        _ = session(for: identifier)
    }

    func enqueueShare(
        vm: SharedVMSummary,
        prompt: String,
        imageData: Data,
        filename: String,
        mimeType: String
    ) async throws {
        guard let token = AuthTokenStore.loadToken(), !token.isEmpty else {
            throw ShareUploadError.missingAuth
        }
        guard let shelleyURL = vm.shelleyURL, !shelleyURL.isEmpty else {
            throw ShareUploadError.missingShelleyURL
        }

        let sessionIdentifier = SharedAppConfiguration.currentBackgroundUploadSessionIdentifier
        let conversationTarget = SharedConversationStore.loadTarget(for: vm.vmName)

        var item = try await Task.detached(priority: .userInitiated) {
            let directoryURL = try SharedAppConfiguration.ensurePendingSharesDirectory()
            let shareID = UUID().uuidString.lowercased()
            let sanitizedFilename = Self.sanitizedFilename(filename, fallbackID: shareID)
            let localFileURL = directoryURL.appendingPathComponent("\(shareID)-\(sanitizedFilename)")
            let multipartBodyFileURL = directoryURL.appendingPathComponent("\(shareID).upload")

            try imageData.write(to: localFileURL, options: .atomic)

            let item = PendingShareItem(
                id: shareID,
                createdAt: Date(),
                vmName: vm.vmName,
                shelleyURL: shelleyURL,
                prompt: prompt,
                filename: sanitizedFilename,
                mimeType: mimeType,
                localFileName: localFileURL.lastPathComponent,
                multipartBodyFileName: multipartBodyFileURL.lastPathComponent,
                targetConversationID: conversationTarget?.conversationID,
                prefersNewConversation: conversationTarget?.prefersNewConversation,
                backgroundSessionIdentifier: sessionIdentifier,
                uploadTaskIdentifier: nil,
                uploadedPath: nil,
                stage: .uploadPending,
                lastError: nil
            )

            try Self.writeMultipartBody(for: item, imageData: imageData)
            return item
        }.value

        PendingShareStore.upsert(item)

        let taskIdentifier = try scheduleUpload(for: item, token: token)
        item.uploadTaskIdentifier = taskIdentifier
        item.stage = .uploading
        PendingShareStore.upsert(item)
        SharedVMStore.saveLastSharedVMName(vm.vmName)
        logger.info(
            "Queued share \(item.id, privacy: .public) for \(vm.vmName, privacy: .public) on \(sessionIdentifier, privacy: .public)"
        )
    }

    func resumePendingShares() async {
        let currentSessionIdentifier = SharedAppConfiguration.currentBackgroundUploadSessionIdentifier
        let activeTaskIDs = await activeBackgroundTaskIDs(for: currentSessionIdentifier)
        let token = AuthTokenStore.loadToken()

        for item in PendingShareStore.allItems() {
            if item.uploadedPath != nil {
                await sendConversationIfReady(itemID: item.id)
                continue
            }

            if !shouldRetryUpload(
                item: item,
                currentSessionIdentifier: currentSessionIdentifier,
                activeTaskIDs: activeTaskIDs
            ) {
                continue
            }

            guard let token, !token.isEmpty else {
                PendingShareStore.update(id: item.id) {
                    $0.stage = .failed
                    $0.lastError = ShareUploadError.missingAuth.localizedDescription
                }
                continue
            }

            do {
                let taskIdentifier = try scheduleUpload(
                    for: item,
                    token: token,
                    sessionIdentifier: currentSessionIdentifier
                )
                PendingShareStore.update(id: item.id) {
                    $0.backgroundSessionIdentifier = currentSessionIdentifier
                    $0.uploadTaskIdentifier = taskIdentifier
                    $0.stage = .uploading
                    $0.lastError = nil
                }
                logger.info(
                    "Rescheduled share \(item.id, privacy: .public) on \(currentSessionIdentifier, privacy: .public)"
                )
            } catch {
                PendingShareStore.update(id: item.id) {
                    $0.stage = .failed
                    $0.lastError = error.localizedDescription
                    $0.uploadTaskIdentifier = nil
                }
                logger.error(
                    "Failed to reschedule share \(item.id, privacy: .public): \(error.localizedDescription, privacy: .public)"
                )
            }
        }
    }

    private func shouldRetryUpload(
        item: PendingShareItem,
        currentSessionIdentifier: String,
        activeTaskIDs: Set<String>
    ) -> Bool {
        switch item.stage {
        case .uploadPending, .failed:
            return true
        case .uploading:
            if item.effectiveBackgroundSessionIdentifier == currentSessionIdentifier {
                return !activeTaskIDs.contains(item.id)
            }
            return item.effectiveBackgroundSessionIdentifier
                == SharedAppConfiguration.legacyBackgroundUploadSessionIdentifier
        case .uploaded, .sending:
            return false
        }
    }

    private func scheduleUpload(
        for item: PendingShareItem,
        token: String,
        sessionIdentifier: String? = nil
    ) throws -> Int {
        let sessionIdentifier = sessionIdentifier ?? item.effectiveBackgroundSessionIdentifier
        var request = URLRequest(url: URL(string: "\(item.shelleyURL)/api/upload")!)
        request.httpMethod = "POST"
        request.setValue(
            "multipart/form-data; boundary=\(item.uploadBoundary)",
            forHTTPHeaderField: "Content-Type"
        )
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue(item.id, forHTTPHeaderField: "X-Exe-Share-Item-ID")

        let task = session(for: sessionIdentifier).uploadTask(
            with: request,
            fromFile: item.multipartBodyFileURL
        )
        task.taskDescription = item.id
        task.resume()
        logger.debug(
            "Scheduled upload task \(task.taskIdentifier) for share \(item.id, privacy: .public) on \(sessionIdentifier, privacy: .public)"
        )
        return task.taskIdentifier
    }

    private func activeBackgroundTaskIDs(for identifier: String) async -> Set<String> {
        await withCheckedContinuation { continuation in
            session(for: identifier).getAllTasks { tasks in
                continuation.resume(returning: Set(tasks.compactMap { self.itemID(for: $0) }))
            }
        }
    }

    private func session(for identifier: String) -> URLSession {
        lock.lock()
        defer { lock.unlock() }

        if let session = sessionsByIdentifier[identifier] {
            return session
        }

        let config = URLSessionConfiguration.background(withIdentifier: identifier)
        config.sharedContainerIdentifier = SharedAppConfiguration.appGroupIdentifier
        config.sessionSendsLaunchEvents = true
        config.waitsForConnectivity = true
        config.isDiscretionary = false

        let session = URLSession(configuration: config, delegate: self, delegateQueue: nil)
        sessionsByIdentifier[identifier] = session
        return session
    }

    private func sendConversationIfReady(itemID: String) async {
        guard let item = PendingShareStore.item(id: itemID),
              item.uploadedPath != nil
        else {
            return
        }

        guard let token = AuthTokenStore.loadToken(), !token.isEmpty else {
            PendingShareStore.update(id: itemID) {
                $0.stage = .failed
                $0.lastError = ShareUploadError.missingAuth.localizedDescription
            }
            return
        }

        PendingShareStore.update(id: itemID) {
            $0.stage = .sending
            $0.lastError = nil
        }

        do {
            switch try await resolveConversationDestination(for: item, token: token) {
            case .existing(let conversationID):
                try await ShareNetworkClient.sendMessage(
                    shelleyURL: item.shelleyURL,
                    token: token,
                    conversationID: conversationID,
                    message: item.chatMessage
                )
                SharedConversationStore.saveConversationID(conversationID, for: item.vmName)
                logger.info(
                    "Appended share \(itemID, privacy: .public) to conversation \(conversationID, privacy: .public)"
                )
            case .new:
                let conversationID = try await ShareNetworkClient.sendNewConversation(
                    shelleyURL: item.shelleyURL,
                    token: token,
                    message: item.chatMessage
                )
                if let conversationID {
                    SharedConversationStore.saveConversationID(conversationID, for: item.vmName)
                }
                logger.info("Started a new conversation for share \(itemID, privacy: .public)")
            }
            cleanupFiles(for: item)
            _ = PendingShareStore.remove(id: itemID)
            logger.info("Completed share \(itemID, privacy: .public)")
        } catch {
            PendingShareStore.update(id: itemID) {
                $0.stage = .failed
                $0.lastError = error.localizedDescription
            }
            logger.error(
                "Failed to send conversation for share \(itemID, privacy: .public): \(error.localizedDescription, privacy: .public)"
            )
        }
    }

    private func cleanupFiles(for item: PendingShareItem) {
        try? FileManager.default.removeItem(at: item.localFileURL)
        try? FileManager.default.removeItem(at: item.multipartBodyFileURL)
    }

    private enum ConversationDestination {
        case existing(String)
        case new
    }

    private func resolveConversationDestination(
        for item: PendingShareItem,
        token: String
    ) async throws -> ConversationDestination {
        if item.effectivePrefersNewConversation {
            logger.debug("Share \(item.id, privacy: .public) prefers a new conversation")
            return .new
        }

        if let targetConversationID = item.targetConversationID {
            do {
                let conversations = try await ShareNetworkClient.listConversations(
                    shelleyURL: item.shelleyURL,
                    token: token
                )
                if conversations.contains(where: { $0.conversationID == targetConversationID && !$0.archived }) {
                    return .existing(targetConversationID)
                }
                if let latestConversationID = conversations.first(where: { !$0.archived })?.conversationID {
                    logger.debug(
                        "Stored conversation \(targetConversationID, privacy: .public) was unavailable; using latest \(latestConversationID, privacy: .public)"
                    )
                    return .existing(latestConversationID)
                }
                return .new
            } catch {
                logger.debug(
                    "Failed to resolve conversation target for share \(item.id, privacy: .public); using stored conversation \(targetConversationID, privacy: .public)"
                )
                return .existing(targetConversationID)
            }
        }

        let conversations = try await ShareNetworkClient.listConversations(
            shelleyURL: item.shelleyURL,
            token: token
        )
        if let latestConversationID = conversations.first(where: { !$0.archived })?.conversationID {
            return .existing(latestConversationID)
        }
        return .new
    }

    private static func sanitizedFilename(_ filename: String, fallbackID: String) -> String {
        let trimmed = filename.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty {
            return trimmed.replacingOccurrences(of: "/", with: "-")
        }
        return "\(fallbackID).png"
    }

    private static func writeMultipartBody(for item: PendingShareItem, imageData: Data) throws {
        var body = Data()
        body.append(Data("--\(item.uploadBoundary)\r\n".utf8))
        body.append(
            Data(
                "Content-Disposition: form-data; name=\"file\"; filename=\"\(item.filename)\"\r\n"
                    .utf8
            )
        )
        body.append(Data("Content-Type: \(item.mimeType)\r\n\r\n".utf8))
        body.append(imageData)
        body.append(Data("\r\n--\(item.uploadBoundary)--\r\n".utf8))
        try body.write(to: item.multipartBodyFileURL, options: .atomic)
    }

    private func appendResponseData(_ data: Data, session: URLSession, taskID: Int) {
        let key = responseDataKey(session: session, taskID: taskID)
        lock.lock()
        responseDataByTaskKey[key, default: Data()].append(data)
        lock.unlock()
    }

    private func takeResponseData(session: URLSession, taskID: Int) -> Data {
        let key = responseDataKey(session: session, taskID: taskID)
        lock.lock()
        defer { lock.unlock() }
        let data = responseDataByTaskKey.removeValue(forKey: key) ?? Data()
        return data
    }

    private func takeBackgroundCompletionHandler(for identifier: String) -> (() -> Void)? {
        lock.lock()
        defer { lock.unlock() }
        let completionHandler = backgroundCompletionHandlersByIdentifier[identifier]
        backgroundCompletionHandlersByIdentifier[identifier] = nil
        return completionHandler
    }

    private func takeSession(for identifier: String) -> URLSession? {
        lock.lock()
        defer { lock.unlock() }
        return sessionsByIdentifier.removeValue(forKey: identifier)
    }

    private func responseDataKey(session: URLSession, taskID: Int) -> String {
        let identifier = session.configuration.identifier ?? "unknown"
        return "\(identifier)::\(taskID)"
    }

    private func itemID(for task: URLSessionTask) -> String? {
        task.taskDescription
            ?? task.originalRequest?.value(forHTTPHeaderField: "X-Exe-Share-Item-ID")
            ?? task.currentRequest?.value(forHTTPHeaderField: "X-Exe-Share-Item-ID")
    }

    func urlSession(_ session: URLSession, dataTask: URLSessionDataTask, didReceive data: Data) {
        appendResponseData(data, session: session, taskID: dataTask.taskIdentifier)
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        guard let itemID = itemID(for: task) else {
            logger.error("Missing share item identifier for completed upload task")
            return
        }

        if let error {
            _ = takeResponseData(session: session, taskID: task.taskIdentifier)
            PendingShareStore.update(id: itemID) {
                $0.uploadTaskIdentifier = nil
                $0.stage = .failed
                $0.lastError = error.localizedDescription
            }
            logger.error(
                "Upload failed for share \(itemID, privacy: .public): \(error.localizedDescription, privacy: .public)"
            )
            return
        }

        let responseData = takeResponseData(session: session, taskID: task.taskIdentifier)
        guard let http = task.response as? HTTPURLResponse else {
            PendingShareStore.update(id: itemID) {
                $0.uploadTaskIdentifier = nil
                $0.stage = .failed
                $0.lastError = ShareUploadError.invalidResponse.localizedDescription
            }
            logger.error("Upload response missing HTTP metadata for share \(itemID, privacy: .public)")
            return
        }

        guard (200..<300).contains(http.statusCode) else {
            let body = String(data: responseData, encoding: .utf8)
            PendingShareStore.update(id: itemID) {
                $0.uploadTaskIdentifier = nil
                $0.stage = .failed
                $0.lastError = body?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false
                    ? body
                    : ShareUploadError.badStatus(http.statusCode).localizedDescription
            }
            logger.error(
                "Upload returned status \(http.statusCode) for share \(itemID, privacy: .public): \(body ?? "", privacy: .public)"
            )
            return
        }

        guard let upload = try? JSONDecoder().decode(UploadResponse.self, from: responseData) else {
            PendingShareStore.update(id: itemID) {
                $0.uploadTaskIdentifier = nil
                $0.stage = .failed
                $0.lastError = ShareUploadError.invalidResponse.localizedDescription
            }
            logger.error("Upload response JSON was invalid for share \(itemID, privacy: .public)")
            return
        }

        PendingShareStore.update(id: itemID) {
            $0.uploadTaskIdentifier = nil
            $0.uploadedPath = upload.path
            $0.stage = .uploaded
            $0.lastError = nil
        }
        logger.info("Upload finished for share \(itemID, privacy: .public)")

        Task {
            await sendConversationIfReady(itemID: itemID)
        }
    }

    func urlSessionDidFinishEvents(forBackgroundURLSession session: URLSession) {
        guard let identifier = session.configuration.identifier else { return }
        takeBackgroundCompletionHandler(for: identifier)?()
        if identifier != SharedAppConfiguration.currentBackgroundUploadSessionIdentifier,
           let retiredSession = takeSession(for: identifier) {
            retiredSession.finishTasksAndInvalidate()
        }
    }
}
