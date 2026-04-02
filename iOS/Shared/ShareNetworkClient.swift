import Foundation

nonisolated struct SharedVMListResponse: Decodable {
    let vms: [SharedVMEnvelope]
    let teamVMs: [SharedVMEnvelope]?

    enum CodingKeys: String, CodingKey {
        case vms
        case teamVMs = "team_vms"
    }
}

nonisolated struct SharedVMEnvelope: Decodable {
    let vmName: String
    let status: String
    let shelleyURL: String?

    enum CodingKeys: String, CodingKey {
        case vmName = "vm_name"
        case status
        case shelleyURL = "shelley_url"
    }
}

nonisolated private struct ShareConversationRequest: Encodable {
    let message: String
}

nonisolated struct SharedConversationSummary: Decodable, Sendable {
    let conversationID: String
    let archived: Bool

    enum CodingKeys: String, CodingKey {
        case conversationID = "conversation_id"
        case archived
    }
}

nonisolated enum ShareNetworkClient {
    static func listVMs(token: String) async throws -> [SharedVMSummary] {
        var request = URLRequest(url: URL(string: "\(SharedAppConfiguration.appBaseURL)/exec")!)
        request.httpMethod = "POST"
        request.httpBody = Data("ls --json".utf8)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")

        let (data, response) = try await URLSession.shared.data(for: request)
        try checkStatus(response)

        let decoded = try JSONDecoder().decode(SharedVMListResponse.self, from: data)
        var vms = decoded.vms
        if let teamVMs = decoded.teamVMs {
            vms.append(contentsOf: teamVMs)
        }

        let summaries = vms.map {
            SharedVMSummary(vmName: $0.vmName, status: $0.status, shelleyURL: $0.shelleyURL)
        }
        SharedVMStore.saveVMs(summaries)
        return summaries
    }

    static func sendNewConversation(
        shelleyURL: String,
        token: String,
        message: String
    ) async throws -> String? {
        var request = URLRequest(url: URL(string: "\(shelleyURL)/api/conversations/new")!)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.httpBody = try JSONEncoder().encode(ShareConversationRequest(message: message))

        let (data, response) = try await URLSession.shared.data(for: request)
        try checkStatus(response)
        return try? JSONDecoder().decode(ShareConversationResponse.self, from: data).conversationID
    }

    static func sendMessage(
        shelleyURL: String,
        token: String,
        conversationID: String,
        message: String
    ) async throws {
        var request = URLRequest(
            url: URL(string: "\(shelleyURL)/api/conversation/\(conversationID)/chat")!
        )
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.httpBody = try JSONEncoder().encode(ShareConversationRequest(message: message))

        let (_, response) = try await URLSession.shared.data(for: request)
        try checkStatus(response)
    }

    static func listConversations(
        shelleyURL: String,
        token: String
    ) async throws -> [SharedConversationSummary] {
        var request = URLRequest(url: URL(string: "\(shelleyURL)/api/conversations")!)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")

        let (data, response) = try await URLSession.shared.data(for: request)
        try checkStatus(response)
        return try JSONDecoder().decode([SharedConversationSummary].self, from: data)
    }

    private static func checkStatus(_ response: URLResponse) throws {
        guard let http = response as? HTTPURLResponse else { return }
        guard (200..<300).contains(http.statusCode) else {
            throw ShareUploadError.badStatus(http.statusCode)
        }
    }
}

nonisolated private struct ShareConversationResponse: Decodable {
    let conversationID: String?

    enum CodingKeys: String, CodingKey {
        case conversationID = "conversation_id"
    }
}
