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

        let (data, _) = try await URLSession.shared.data(for: request)
        let response = try Self.decoder.decode(VMListResponse.self, from: data)

        var vms = response.vms
        if let teamVMs = response.teamVMs {
            vms.append(contentsOf: teamVMs)
        }
        return vms
    }

    // MARK: - Conversations

    func listConversations(shelleyURL: String) async throws -> [ConversationWithState] {
        var request = URLRequest(url: URL(string: "\(shelleyURL)/api/conversations")!)
        addAuth(&request)

        let (data, _) = try await URLSession.shared.data(for: request)
        return try Self.decoder.decode([ConversationWithState].self, from: data)
    }

    func getConversation(shelleyURL: String, id: String) async throws -> StreamResponse {
        var request = URLRequest(url: URL(string: "\(shelleyURL)/api/conversation/\(id)")!)
        addAuth(&request)
        request.setValue("gzip", forHTTPHeaderField: "Accept-Encoding")

        let (data, _) = try await URLSession.shared.data(for: request)
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

        let (data, _) = try await URLSession.shared.data(for: request)
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

        let (data, _) = try await URLSession.shared.data(for: request)
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

    // MARK: - Helpers

    private func addAuth(_ request: inout URLRequest) {
        if let token {
            request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        }
    }
}

enum APIError: Error, LocalizedError {
    case badStatus(Int)
    case noData

    var errorDescription: String? {
        switch self {
        case .badStatus(let code): "Server returned status \(code)"
        case .noData: "No data received"
        }
    }
}
