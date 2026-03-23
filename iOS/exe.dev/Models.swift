import Foundation
import SwiftData

// MARK: - VM List (from POST /exec with body "ls --json")

struct VMListResponse: Decodable {
    let vms: [VM]
    let teamVMs: [VM]?

    enum CodingKeys: String, CodingKey {
        case vms
        case teamVMs = "team_vms"
    }
}

struct VM: Identifiable, Decodable {
    let vmName: String
    let sshDest: String
    let status: String
    let region: String
    let httpsURL: String
    let shelleyURL: String?
    let image: String?
    let regionDisplay: String?
    let creatorEmail: String?

    var id: String { vmName }
    var isRunning: Bool { status == "running" }

    enum CodingKeys: String, CodingKey {
        case vmName = "vm_name"
        case sshDest = "ssh_dest"
        case status, region
        case httpsURL = "https_url"
        case shelleyURL = "shelley_url"
        case image
        case regionDisplay = "region_display"
        case creatorEmail = "creator_email"
    }
}

// MARK: - Conversations

struct Conversation: Identifiable, Decodable {
    let conversationID: String
    let slug: String?
    let userInitiated: Bool
    let createdAt: Date
    let updatedAt: Date
    let cwd: String?
    let archived: Bool
    let parentConversationID: String?
    let model: String?

    var id: String { conversationID }

    enum CodingKeys: String, CodingKey {
        case conversationID = "conversation_id"
        case slug
        case userInitiated = "user_initiated"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case cwd, archived
        case parentConversationID = "parent_conversation_id"
        case model
    }
}

struct ConversationWithState: Identifiable, Decodable {
    let conversationID: String
    let slug: String?
    let userInitiated: Bool
    let createdAt: Date
    let updatedAt: Date
    let cwd: String?
    let archived: Bool
    let parentConversationID: String?
    let model: String?
    let working: Bool?

    var id: String { conversationID }

    enum CodingKeys: String, CodingKey {
        case conversationID = "conversation_id"
        case slug
        case userInitiated = "user_initiated"
        case createdAt = "created_at"
        case updatedAt = "updated_at"
        case cwd, archived
        case parentConversationID = "parent_conversation_id"
        case model, working
    }
}

// MARK: - Messages

struct ShelleyMessage: Identifiable, Decodable {
    let messageID: String
    let conversationID: String
    let sequenceID: Int64
    let type: String // "user", "agent", "error", "system"
    let llmData: String?
    let userData: String?
    let usageData: String?
    let createdAt: Date
    let displayData: String?
    let endOfTurn: Bool?

    var id: String { messageID }

    enum CodingKeys: String, CodingKey {
        case messageID = "message_id"
        case conversationID = "conversation_id"
        case sequenceID = "sequence_id"
        case type
        case llmData = "llm_data"
        case userData = "user_data"
        case usageData = "usage_data"
        case createdAt = "created_at"
        case displayData = "display_data"
        case endOfTurn = "end_of_turn"
    }

    private var parsed: LLMMessage? {
        guard let llmData, let data = llmData.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(LLMMessage.self, from: data)
    }

    var displayText: String {
        // Try llm_data first (works for both user and agent messages)
        if let msg = parsed {
            let text = msg.content
                .filter { $0.type == 2 && $0.text != nil }
                .map { $0.text! }
                .joined(separator: "\n")
            // If llm_data parsed, only return text blocks.
            // Don't fall through — tool-only messages should return "".
            return text
        }

        // No llm_data — fall back to user_data (may be JSON-encoded string)
        if let userData {
            if userData.hasPrefix("\""), let unquoted = try? JSONDecoder().decode(
                String.self, from: Data(userData.utf8)
            ) {
                return unquoted
            }
            return userData
        }

        return ""
    }

    var isToolUse: Bool {
        parsed?.content.contains { $0.type == 5 } ?? false
    }

    var toolName: String? {
        parsed?.content.first { $0.type == 5 }?.toolName
    }

    /// Extracts the tool input as a readable string (e.g. the bash command).
    var toolInputSummary: String? {
        guard let block = parsed?.content.first(where: { $0.type == 5 }),
              let input = block.toolInput else { return nil }
        // toolInput is an AnyCodable wrapping a dictionary.
        guard let dict = input.value as? [String: AnyCodable] else { return nil }
        // For Bash/shell tools, show "command"; for Edit, show "file_path"; etc.
        // Fall back to showing all string values.
        for key in ["command", "pattern", "file_path", "path", "query", "url"] {
            if let val = dict[key], let str = val.value as? String, !str.isEmpty {
                return str
            }
        }
        // Generic: show all string key=value pairs
        let parts = dict.compactMap { k, v -> String? in
            guard let s = v.value as? String, !s.isEmpty else { return nil }
            return "\(k): \(s)"
        }
        return parts.isEmpty ? nil : parts.joined(separator: "\n")
    }

    var toolUseID: String? {
        parsed?.content.first { $0.type == 5 }?.toolUseID
    }
}

// MARK: - LLM Message Format
//
// llm_data JSON: {"Role":int,"Content":[...],"EndOfTurn":bool}
// Content types: 0=Text, 1=Thinking, 2=RedactedThinking, 3=ToolUse, 4=ToolResult
// Note: JSON integer values from Go's iota+offset are shifted:
//   Text=2, Thinking=3, RedactedThinking=4, ToolUse=5, ToolResult=6
// We handle both numbering schemes for robustness.

struct LLMMessage: Decodable {
    let role: Int // 0=user, 1=assistant
    let content: [LLMContentBlock]
    let endOfTurn: Bool?

    enum CodingKeys: String, CodingKey {
        case role = "Role"
        case content = "Content"
        case endOfTurn = "EndOfTurn"
    }
}

struct LLMContentBlock: Decodable {
    let id: String?
    let type: Int       // 2=text, 3=thinking, 5=tool_use, 6=tool_result
    let text: String?
    let thinking: String?
    let toolName: String?
    let toolInput: AnyCodable?
    let toolUseID: String?
    let toolError: Bool?
    let display: AnyCodable?

    enum CodingKeys: String, CodingKey {
        case id = "ID"
        case type = "Type"
        case text = "Text"
        case thinking = "Thinking"
        case toolName = "ToolName"
        case toolInput = "ToolInput"
        case toolUseID = "ToolUseID"
        case toolError = "ToolError"
        case display = "Display"
    }
}

// Wrapper to decode arbitrary JSON values we don't need to inspect deeply
struct AnyCodable: Decodable {
    let value: Any

    init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if let str = try? container.decode(String.self) { value = str }
        else if let int = try? container.decode(Int.self) { value = int }
        else if let bool = try? container.decode(Bool.self) { value = bool }
        else if let dict = try? container.decode([String: AnyCodable].self) { value = dict }
        else if let arr = try? container.decode([AnyCodable].self) { value = arr }
        else if container.decodeNil() { value = NSNull() }
        else { value = NSNull() }
    }
}

// MARK: - Stream Response (SSE)

struct StreamResponse: Decodable {
    let messages: [ShelleyMessage]?
    let conversation: Conversation?
    let conversationState: ConversationState?
    let heartbeat: Bool?

    enum CodingKeys: String, CodingKey {
        case messages, conversation
        case conversationState = "conversation_state"
        case heartbeat
    }
}

struct ConversationState: Decodable {
    let conversationID: String?
    let working: Bool
    let model: String?

    enum CodingKeys: String, CodingKey {
        case conversationID = "conversation_id"
        case working, model
    }
}

// MARK: - Chat Request

struct ChatRequest: Encodable {
    let message: String
    let model: String?
    let cwd: String?
}

// MARK: - Chat Response

struct ChatResponse: Decodable {
    let status: String
    let conversationID: String?

    enum CodingKeys: String, CodingKey {
        case status
        case conversationID = "conversation_id"
    }
}

// MARK: - VM Creation

struct HostnameCheckResponse: Decodable {
    let valid: Bool
    let available: Bool
    let message: String?
}

// MARK: - SwiftData Models

@Model final class StoredVM {
    @Attribute(.unique) var vmName: String
    var sshDest: String
    var status: String
    var region: String
    var httpsURL: String
    var shelleyURL: String?
    var image: String?
    var regionDisplay: String?
    var creatorEmail: String?
    var lastFetchedAt: Date
    var lastViewedAt: Date?
    var unreadCount: Int = 0

    var isRunning: Bool { status == "running" }
    var isCreating: Bool { status == "creating" || status == "pending" || status == "building" }

    init(from vm: VM) {
        self.vmName = vm.vmName
        self.sshDest = vm.sshDest
        self.status = vm.status
        self.region = vm.region
        self.httpsURL = vm.httpsURL
        self.shelleyURL = vm.shelleyURL
        self.image = vm.image
        self.regionDisplay = vm.regionDisplay
        self.creatorEmail = vm.creatorEmail
        self.lastFetchedAt = Date()
    }

    /// Placeholder for a VM that is being created (not yet returned by the API).
    init(creating hostname: String) {
        self.vmName = hostname
        self.sshDest = ""
        self.status = "creating"
        self.region = ""
        self.httpsURL = ""
        self.lastFetchedAt = Date()
    }

    func update(from vm: VM) {
        sshDest = vm.sshDest
        status = vm.status
        region = vm.region
        httpsURL = vm.httpsURL
        shelleyURL = vm.shelleyURL
        image = vm.image
        regionDisplay = vm.regionDisplay
        creatorEmail = vm.creatorEmail
        lastFetchedAt = Date()
    }
}

@Model final class StoredConversation {
    @Attribute(.unique) var conversationID: String
    var vmName: String
    var slug: String?
    var userInitiated: Bool
    var createdAt: Date
    var updatedAt: Date
    var cwd: String?
    var archived: Bool
    var parentConversationID: String?
    var model: String?
    var working: Bool

    init(from conv: Conversation, vmName: String, working: Bool = false) {
        self.conversationID = conv.conversationID
        self.vmName = vmName
        self.slug = conv.slug
        self.userInitiated = conv.userInitiated
        self.createdAt = conv.createdAt
        self.updatedAt = conv.updatedAt
        self.cwd = conv.cwd
        self.archived = conv.archived
        self.parentConversationID = conv.parentConversationID
        self.model = conv.model
        self.working = working
    }

    init(from conv: ConversationWithState, vmName: String) {
        self.conversationID = conv.conversationID
        self.vmName = vmName
        self.slug = conv.slug
        self.userInitiated = conv.userInitiated
        self.createdAt = conv.createdAt
        self.updatedAt = conv.updatedAt
        self.cwd = conv.cwd
        self.archived = conv.archived
        self.parentConversationID = conv.parentConversationID
        self.model = conv.model
        self.working = conv.working ?? false
    }

    func update(from conv: Conversation, working: Bool) {
        self.slug = conv.slug
        self.userInitiated = conv.userInitiated
        self.updatedAt = conv.updatedAt
        self.cwd = conv.cwd
        self.archived = conv.archived
        self.parentConversationID = conv.parentConversationID
        self.model = conv.model
        self.working = working
    }

    func update(from conv: ConversationWithState) {
        self.slug = conv.slug
        self.userInitiated = conv.userInitiated
        self.updatedAt = conv.updatedAt
        self.cwd = conv.cwd
        self.archived = conv.archived
        self.parentConversationID = conv.parentConversationID
        self.model = conv.model
        if let w = conv.working { self.working = w }
    }
}

@Model final class StoredMessage {
    @Attribute(.unique) var messageID: String
    var conversationID: String
    var sequenceID: Int64
    var type: String
    var llmData: String?
    var userData: String?
    var usageData: String?
    var createdAt: Date
    var displayData: String?
    var endOfTurn: Bool?
    // Pre-computed at write time (eliminates JSON parsing from render path)
    var displayText: String
    var isToolUse: Bool
    var toolName: String?

    /// Parses llmData on demand to extract tool input details.
    var toolInputSummary: String? {
        guard let llmData, let data = llmData.data(using: .utf8),
              let msg = try? JSONDecoder().decode(LLMMessage.self, from: data),
              let block = msg.content.first(where: { $0.type == 5 }),
              let input = block.toolInput,
              let dict = input.value as? [String: AnyCodable] else { return nil }
        for key in ["command", "pattern", "file_path", "path", "query", "url"] {
            if let val = dict[key], let str = val.value as? String, !str.isEmpty {
                return str
            }
        }
        let parts = dict.compactMap { k, v -> String? in
            guard let s = v.value as? String, !s.isEmpty else { return nil }
            return "\(k): \(s)"
        }
        return parts.isEmpty ? nil : parts.joined(separator: "\n")
    }

    /// The tool_use ID, used to match against tool_result messages.
    var toolUseID: String? {
        guard let llmData, let data = llmData.data(using: .utf8),
              let msg = try? JSONDecoder().decode(LLMMessage.self, from: data) else { return nil }
        return msg.content.first { $0.type == 5 }?.toolUseID
    }

    /// Extracts tool result text from a message containing tool_result content blocks.
    var toolResultText: String? {
        guard let llmData, let data = llmData.data(using: .utf8),
              let msg = try? JSONDecoder().decode(LLMMessage.self, from: data) else { return nil }
        // tool_result content blocks (type 6) have text in the "text" field.
        for block in msg.content where block.type == 6 {
            if let text = block.text, !text.isEmpty { return text }
        }
        return nil
    }

    /// Extracts a screenshot relative URL from displayData (e.g. "/api/read?path=...").
    /// displayData is JSON like: [{"tool_name":"browser","display":{"type":"screenshot","url":"..."}}]
    var screenshotPath: String? {
        guard let displayData, let data = displayData.data(using: .utf8),
              let entries = try? JSONDecoder().decode([[String: AnyCodable]].self, from: data)
        else { return nil }
        for entry in entries {
            guard let display = entry["display"]?.value as? [String: AnyCodable],
                  let type = display["type"]?.value as? String, type == "screenshot",
                  let url = display["url"]?.value as? String
            else { continue }
            return url
        }
        return nil
    }

    init(from msg: ShelleyMessage) {
        self.messageID = msg.messageID
        self.conversationID = msg.conversationID
        self.sequenceID = msg.sequenceID
        self.type = msg.type
        self.llmData = msg.llmData
        self.userData = msg.userData
        self.usageData = msg.usageData
        self.createdAt = msg.createdAt
        self.displayData = msg.displayData
        self.endOfTurn = msg.endOfTurn
        self.displayText = msg.displayText
        self.isToolUse = msg.isToolUse
        self.toolName = msg.toolName
    }

    func update(from msg: ShelleyMessage) {
        self.sequenceID = msg.sequenceID
        self.type = msg.type
        self.llmData = msg.llmData
        self.userData = msg.userData
        self.usageData = msg.usageData
        self.displayData = msg.displayData
        self.endOfTurn = msg.endOfTurn
        self.displayText = msg.displayText
        self.isToolUse = msg.isToolUse
        self.toolName = msg.toolName
    }
}
