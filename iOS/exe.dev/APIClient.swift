import Foundation

final class APIClient: Sendable {
    let baseURL: String
    private let auth: AuthManager

    private var token: String? { auth.token }

    private static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let str = try container.decode(String.self)

            // Try ISO8601 with fractional seconds first, then without
            let formatters: [ISO8601DateFormatter] = {
                let withFrac = ISO8601DateFormatter()
                withFrac.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
                let without = ISO8601DateFormatter()
                without.formatOptions = [.withInternetDateTime]
                return [withFrac, without]
            }()

            for formatter in formatters {
                if let date = formatter.date(from: str) {
                    return date
                }
            }
            throw DecodingError.dataCorruptedError(
                in: container, debugDescription: "Cannot decode date: \(str)"
            )
        }
        return d
    }()

    init(baseURL: String, auth: AuthManager) {
        self.baseURL = baseURL
        self.auth = auth
    }

    // MARK: - VM List

    func listVMs() async throws -> [VM] {
        var request = URLRequest(url: URL(string: "\(baseURL)/exec")!)
        request.httpMethod = "POST"
        request.httpBody = Data("ls --json".utf8)
        addAuth(&request)

        let (data, response) = try await URLSession.shared.data(for: request)
        try Self.checkStatus(response)
        let decoded = try Self.decoder.decode(VMListResponse.self, from: data)

        var vms = decoded.vms
        if let teamVMs = decoded.teamVMs {
            vms.append(contentsOf: teamVMs)
        }
        return vms
    }

    // MARK: - Conversations

    func listConversations(shelleyURL: String) async throws -> [ConversationWithState] {
        var request = URLRequest(url: URL(string: "\(shelleyURL)/api/conversations")!)
        addAuth(&request)

        let (data, response) = try await URLSession.shared.data(for: request)
        try Self.checkStatus(response)
        return try Self.decoder.decode([ConversationWithState].self, from: data)
    }

    func getConversation(shelleyURL: String, id: String) async throws -> StreamResponse {
        var request = URLRequest(url: URL(string: "\(shelleyURL)/api/conversation/\(id)")!)
        addAuth(&request)
        request.setValue("gzip", forHTTPHeaderField: "Accept-Encoding")

        let (data, response) = try await URLSession.shared.data(for: request)
        try Self.checkStatus(response)
        return try Self.decoder.decode(StreamResponse.self, from: data)
    }

    // MARK: - Chat

    func sendMessage(
        shelleyURL: String,
        conversationID: String,
        message: String
    ) async throws -> ChatResponse {
        var request = URLRequest(
            url: URL(string: "\(shelleyURL)/api/conversation/\(conversationID)/chat")!
        )
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        addAuth(&request)

        let body = ChatRequest(message: message, model: nil, cwd: nil)
        request.httpBody = try JSONEncoder().encode(body)

        let (data, response) = try await URLSession.shared.data(for: request)
        try Self.checkStatus(response)
        return try Self.decoder.decode(ChatResponse.self, from: data)
    }

    func newConversation(
        shelleyURL: String,
        message: String
    ) async throws -> ChatResponse {
        var request = URLRequest(url: URL(string: "\(shelleyURL)/api/conversations/new")!)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        addAuth(&request)

        let body = ChatRequest(message: message, model: nil, cwd: nil)
        request.httpBody = try JSONEncoder().encode(body)

        let (data, response) = try await URLSession.shared.data(for: request)
        try Self.checkStatus(response)
        return try Self.decoder.decode(ChatResponse.self, from: data)
    }

    // MARK: - SSE Streaming

    func streamConversation(
        shelleyURL: String,
        conversationID: String,
        lastSequenceID: Int64? = nil
    ) -> AsyncThrowingStream<StreamResponse, Error> {
        AsyncThrowingStream { continuation in
            let task = Task {
                do {
                    var urlString = "\(shelleyURL)/api/conversation/\(conversationID)/stream"
                    if let lastSequenceID {
                        urlString += "?last_sequence_id=\(lastSequenceID)"
                    }
                    var request = URLRequest(url: URL(string: urlString)!)
                    request.timeoutInterval = 300
                    addAuth(&request)

                    let (bytes, response) = try await URLSession.shared.bytes(for: request)
                    guard let httpResponse = response as? HTTPURLResponse,
                          httpResponse.statusCode == 200
                    else {
                        throw APIError.badStatus(
                            (response as? HTTPURLResponse)?.statusCode ?? 0
                        )
                    }

                    for try await line in bytes.lines {
                        if Task.isCancelled { break }

                        // SSE format: "data: {json}"
                        guard line.hasPrefix("data: ") else { continue }
                        let jsonString = String(line.dropFirst(6))
                        guard let jsonData = jsonString.data(using: .utf8) else { continue }

                        if let event = try? Self.decoder.decode(
                            StreamResponse.self, from: jsonData
                        ) {
                            continuation.yield(event)
                        }
                    }
                    continuation.finish()
                } catch {
                    if !Task.isCancelled {
                        continuation.finish(throwing: error)
                    }
                }
            }
            continuation.onTermination = { _ in task.cancel() }
        }
    }

    // MARK: - VM Creation

    /// Checks if a hostname is valid and available via POST /check-hostname.
    func checkHostname(_ hostname: String) async throws -> HostnameCheckResponse {
        var request = URLRequest(url: URL(string: "\(baseURL)/check-hostname")!)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        addAuth(&request)

        request.httpBody = try JSONEncoder().encode(["hostname": hostname])

        let (data, response) = try await URLSession.shared.data(for: request)
        try Self.checkStatus(response)
        return try Self.decoder.decode(HostnameCheckResponse.self, from: data)
    }

    /// Creates a VM via POST /create-vm (form-encoded, same as web UI).
    /// The server starts async creation and redirects; we don't follow the redirect.
    func createVM(hostname: String, prompt: String) async throws {
        var request = URLRequest(url: URL(string: "\(baseURL)/create-vm")!)
        request.httpMethod = "POST"
        request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        addAuth(&request)

        var parts = [URLQueryItem(name: "hostname", value: hostname)]
        if !prompt.isEmpty {
            parts.append(URLQueryItem(name: "prompt", value: prompt))
        }
        var components = URLComponents()
        components.queryItems = parts
        request.httpBody = components.percentEncodedQuery?.data(using: .utf8)

        // Use a session that doesn't follow redirects so we can treat 303 as success.
        let delegate = NoRedirectDelegate()
        let session = URLSession(configuration: .default, delegate: delegate, delegateQueue: nil)
        defer { session.finishTasksAndInvalidate() }

        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw APIError.noData }

        if http.statusCode == 303 {
            let location = http.value(forHTTPHeaderField: "Location") ?? ""
            if location.contains("billing") {
                throw APIError.serverMessage("Your plan does not allow VM creation. Please upgrade at exe.dev.")
            }
            if location.contains("error=vm_limit") {
                throw APIError.serverMessage("You've reached your VM limit.")
            }
            // Success — server redirected to dashboard.
            return
        }

        // If the server returned a non-redirect error, try to parse it.
        if http.statusCode >= 400 {
            if let body = try? JSONDecoder().decode([String: String].self, from: data),
               let message = body["error"] {
                throw APIError.serverMessage(message)
            }
            throw APIError.badStatus(http.statusCode)
        }
    }

    // MARK: - Push Tokens

    func registerPushToken(_ token: String, platform: String = "apns", environment: String = "production") async throws {
        var request = URLRequest(url: URL(string: "\(baseURL)/api/push-tokens")!)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        addAuth(&request)

        let body = ["token": token, "platform": platform, "environment": environment]
        request.httpBody = try JSONEncoder().encode(body)

        let (_, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            throw APIError.badStatus((response as? HTTPURLResponse)?.statusCode ?? 0)
        }
    }

    // MARK: - SSH Exec

    /// Execute an SSH command via POST /exec and return the raw output.
    func exec(_ command: String) async throws -> Data {
        var request = URLRequest(url: URL(string: "\(baseURL)/exec")!)
        request.httpMethod = "POST"
        request.httpBody = Data(command.utf8)
        addAuth(&request)

        let (data, response) = try await URLSession.shared.data(for: request)
        try Self.checkStatus(response)
        return data
    }

    /// Execute an SSH command and decode the JSON response.
    func execJSON<T: Decodable>(_ command: String) async throws -> T {
        let data = try await exec(command)
        return try Self.decoder.decode(T.self, from: data)
    }

    // MARK: - Helpers

    private func addAuth(_ request: inout URLRequest) {
        if let token {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
    }

    private static func checkStatus(_ response: URLResponse) throws {
        guard let http = response as? HTTPURLResponse else { return }
        guard (200..<300).contains(http.statusCode) else {
            throw APIError.badStatus(http.statusCode)
        }
    }
}

enum APIError: Error, LocalizedError {
    case badStatus(Int)
    case noData
    case serverMessage(String)

    var errorDescription: String? {
        switch self {
        case .badStatus(let code): "Server returned status \(code)"
        case .noData: "No data received"
        case .serverMessage(let msg): msg
        }
    }
}

/// URLSession delegate that prevents automatic redirect following.
private final class NoRedirectDelegate: NSObject, URLSessionTaskDelegate, Sendable {
    func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        willPerformHTTPRedirection response: HTTPURLResponse,
        newRequest request: URLRequest,
        completionHandler: @escaping (URLRequest?) -> Void
    ) {
        completionHandler(nil) // Don't follow redirects
    }
}
