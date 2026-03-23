import SwiftUI
import SwiftData

// Environment keys for passing shelley context to nested views.
private struct ShelleyURLKey: EnvironmentKey {
    static let defaultValue: String? = nil
}
private struct AuthTokenKey: EnvironmentKey {
    static let defaultValue: String? = nil
}
extension EnvironmentValues {
    var shelleyURL: String? {
        get { self[ShelleyURLKey.self] }
        set { self[ShelleyURLKey.self] = newValue }
    }
    var authToken: String? {
        get { self[AuthTokenKey.self] }
        set { self[AuthTokenKey.self] = newValue }
    }
}

struct ChannelView: View {
    @State var viewModel: ChannelViewModel

    var body: some View {
        VStack(spacing: 0) {
            if viewModel.isLoading {
                VStack(spacing: 12) {
                    ProgressView()
                    Text("Loading conversation...")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if let error = viewModel.error, viewModel.conversationID == nil {
                ContentUnavailableView {
                    Label("Unable to Load", systemImage: "exclamationmark.triangle")
                } description: {
                    Text(error)
                } actions: {
                    Button("Retry") {
                        Task { await viewModel.loadLatestConversation() }
                    }
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if viewModel.conversationID != nil {
                MessageListView(viewModel: viewModel)
                    .environment(\.shelleyURL, viewModel.shelleyURL)
            } else {
                Spacer()
            }

            InputBarView(viewModel: viewModel)
        }
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button(action: { viewModel.newConversation() }) {
                    Image(systemName: "square.and.pencil")
                }
            }
        }
        .task { await viewModel.loadLatestConversation() }
        .onDisappear { viewModel.onDisappear() }
    }
}

// MARK: - Input Bar

private struct InputBarView: View {
    let viewModel: ChannelViewModel
    @State private var inputText = ""
    @FocusState private var isFieldFocused: Bool

    private var canSend: Bool {
        !inputText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    var body: some View {
        HStack(spacing: 8) {
            TextField("Message #\(viewModel.vmName)", text: $inputText, axis: .vertical)
                .textFieldStyle(.plain)
                .lineLimit(1...5)
                .focused($isFieldFocused)
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
                .background(Color(.systemGray6))
                .clipShape(RoundedRectangle(cornerRadius: 18))

            Button(action: sendMessage) {
                Image(systemName: "arrow.up.circle.fill")
                    .font(.system(size: 28))
                    .foregroundStyle(canSend ? Color.accentColor : .gray)
            }
            .disabled(!canSend)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(.bar)
        .background {
            // Hidden button that's always enabled so shift+return always fires
            Button("", action: sendMessage)
                .keyboardShortcut(.return, modifiers: .shift)
                .opacity(0)
                .allowsHitTesting(false)
        }
    }

    private func sendMessage() {
        let text = inputText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }
        inputText = ""
        isFieldFocused = true
        viewModel.send(text: text)
    }
}

// MARK: - Message List

private struct MessageListView: View {
    let viewModel: ChannelViewModel
    @State private var isAtBottom = true

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 2) {
                    ForEach(groupedMessages) { group in
                        MessageGroupView(group: group, allMessages: viewModel.messages)
                    }

                    ForEach(viewModel.pendingMessages) { pending in
                        PendingMessageView(message: pending, onRetry: {
                            viewModel.retry(id: pending.id)
                        })
                    }

                    if viewModel.isWorking || viewModel.hasPendingSends {
                        typingIndicator
                    }

                    Color.clear.frame(height: 1).id("bottom")
                        .onAppear { isAtBottom = true }
                        .onDisappear { isAtBottom = false }
                }
                .padding(.horizontal, 12)
                .padding(.top, 8)
            }
            .defaultScrollAnchor(.bottom)
            .onAppear {
                proxy.scrollTo("bottom")
            }
            .onChange(of: viewModel.messages.count) { old, new in
                if new > old {
                    proxy.scrollTo("bottom")
                }
            }
            .onChange(of: viewModel.isWorking) {
                proxy.scrollTo("bottom")
            }
            .onReceive(NotificationCenter.default.publisher(for: UIResponder.keyboardWillShowNotification)) { _ in
                if isAtBottom {
                    // Small delay lets SwiftUI adjust the ScrollView's safe area first
                    DispatchQueue.main.asyncAfter(deadline: .now() + 0.05) {
                        withAnimation {
                            proxy.scrollTo("bottom")
                        }
                    }
                }
            }
        }
    }

    private var typingIndicator: some View {
        HStack(spacing: 4) {
            ProgressView()
                .controlSize(.small)
            Text("Shelley is working...")
                .font(.caption)
                .foregroundStyle(.secondary)
                .italic()
        }
        .padding(.leading, 36)
        .padding(.vertical, 6)
    }

    // MARK: - Message Grouping

    struct MessageGroup: Identifiable {
        let id: String
        let senderType: String
        let messages: [StoredMessage]
        let timestamp: Date
    }

    private var groupedMessages: [MessageGroup] {
        var groups: [MessageGroup] = []
        var currentMessages: [StoredMessage] = []
        var currentType: String?

        let visible = viewModel.messages.filter { msg in
            if msg.type == "system" || msg.type == "tool" || msg.type == "gitinfo" {
                return false
            }
            if msg.displayText.isEmpty && !msg.isToolUse { return false }
            return true
        }

        for msg in visible {
            if msg.type != currentType {
                if !currentMessages.isEmpty, let type = currentType {
                    groups.append(MessageGroup(
                        id: currentMessages.first!.messageID,
                        senderType: type,
                        messages: currentMessages,
                        timestamp: currentMessages.first!.createdAt
                    ))
                }
                currentMessages = [msg]
                currentType = msg.type
            } else {
                currentMessages.append(msg)
            }
        }

        if !currentMessages.isEmpty, let type = currentType {
            groups.append(MessageGroup(
                id: currentMessages.first!.messageID,
                senderType: type,
                messages: currentMessages,
                timestamp: currentMessages.first!.createdAt
            ))
        }

        return groups
    }
}

// MARK: - Pending Message (optimistic render before SSE delivers)

private struct PendingMessageView: View {
    let message: PendingMessage
    let onRetry: () -> Void

    private var isFailed: Bool {
        if case .failed = message.status { return true }
        return false
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
                Image(systemName: "person.circle.fill")
                    .font(.system(size: 16))
                    .foregroundStyle(isFailed ? .red : .blue)
                    .frame(width: 24, height: 24)
                Text("You")
                    .font(.system(size: 14, weight: .bold))
                Text(message.createdAt, style: .time)
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
            }
            Text(message.text)
                .font(.system(size: 15))
                .padding(.leading, 30)
                .foregroundStyle(isFailed ? .primary : .secondary)

            if case .failed(let reason) = message.status {
                HStack(spacing: 6) {
                    Image(systemName: "exclamationmark.circle.fill")
                        .font(.caption2)
                        .foregroundStyle(.red)
                    Text("Failed to send")
                        .font(.caption)
                        .foregroundStyle(.red)
                    Button("Retry", action: onRetry)
                        .font(.caption)
                }
                .padding(.leading, 30)
                .help(reason)
            }
        }
        .padding(.vertical, 4)
    }
}

// MARK: - Message Group View

private struct MessageGroupView: View {
    let group: MessageListView.MessageGroup
    let allMessages: [StoredMessage]

    private var senderName: String {
        group.senderType == "user" ? "You" : "Shelley"
    }

    private var senderIcon: String {
        group.senderType == "user" ? "person.circle.fill" : "sparkle"
    }

    private var senderColor: Color {
        group.senderType == "user" ? .blue : .purple
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
                Image(systemName: senderIcon)
                    .font(.system(size: 16))
                    .foregroundStyle(senderColor)
                    .frame(width: 24, height: 24)

                Text(senderName)
                    .font(.system(size: 14, weight: .bold))

                Text(group.timestamp, style: .time)
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
            }

            VStack(alignment: .leading, spacing: 4) {
                ForEach(group.messages) { msg in
                    MessageContentView(message: msg, allMessages: allMessages)
                }
            }
            .padding(.leading, 30)
            .textSelection(.enabled)
        }
        .padding(.vertical, 4)
    }
}

// MARK: - Message Content View

private struct MessageContentView: View {
    let message: StoredMessage
    let allMessages: [StoredMessage]
    @State private var isToolExpanded = false

    var body: some View {
        if message.isToolUse {
            ToolUseView(
                message: message,
                allMessages: allMessages,
                isExpanded: $isToolExpanded
            )
        }

        let text = message.displayText
        if !text.isEmpty {
            FormattedText(text: text)
        }
    }
}

// MARK: - Tool Use View

private struct ToolUseView: View {
    let message: StoredMessage
    let allMessages: [StoredMessage]
    @Binding var isExpanded: Bool
    @Environment(\.shelleyURL) private var shelleyURL

    private var name: String { message.toolName ?? "tool" }

    private var toolResultMessage: StoredMessage? {
        let startSeq = message.sequenceID
        // Tool results are stored as "user" type messages following the agent message.
        // Find the next non-agent message that has tool result content or display data.
        return allMessages
            .first { $0.sequenceID > startSeq && $0.type != "agent" &&
                     ($0.toolResultText != nil || $0.screenshotPath != nil || $0.displayData != nil) }
    }

    private var isScreenshot: Bool {
        toolResultMessage?.screenshotPath != nil
    }

    private var screenshotURL: URL? {
        guard let path = toolResultMessage?.screenshotPath,
              let base = shelleyURL else { return nil }
        return URL(string: base + path)
    }

    var body: some View {
        Button {
            withAnimation(.easeInOut(duration: 0.2)) { isExpanded.toggle() }
        } label: {
            HStack(spacing: 4) {
                Image(systemName: isScreenshot ? "camera" : "wrench")
                    .font(.caption2)
                Image(systemName: "chevron.right")
                    .font(.system(size: 8, weight: .semibold))
                    .rotationEffect(.degrees(isExpanded ? 90 : 0))
                Text(isScreenshot ? "Screenshot" : "Used \(name)")
                    .font(.caption)
            }
            .foregroundStyle(.secondary)
        }
        .buttonStyle(.plain)
        .onAppear {
            // Screenshots default to expanded.
            if isScreenshot && !isExpanded {
                isExpanded = true
            }
        }

        if isExpanded {
            VStack(alignment: .leading, spacing: 6) {
                if let url = screenshotURL {
                    ScreenshotView(url: url)
                } else {
                    if let input = message.toolInputSummary {
                        Text(input)
                            .font(.system(size: 12, design: .monospaced))
                            .padding(8)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(Color(.systemGray6))
                            .clipShape(RoundedRectangle(cornerRadius: 6))
                    }
                    if let result = toolResultMessage?.toolResultText {
                        Text(result.prefix(2000))
                            .font(.system(size: 12, design: .monospaced))
                            .foregroundStyle(.secondary)
                            .padding(8)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(Color(.systemGray6))
                            .clipShape(RoundedRectangle(cornerRadius: 6))
                            .lineLimit(20)
                    }
                }
            }
            .padding(.top, 2)
        }
    }
}

// MARK: - Screenshot View

private struct ScreenshotView: View {
    let url: URL
    @Environment(\.authToken) private var authToken
    @State private var image: UIImage?
    @State private var failed = false

    var body: some View {
        // Always use a fixed-height placeholder so layout never depends on the network.
        ZStack {
            if let image {
                Image(uiImage: image)
                    .resizable()
                    .aspectRatio(contentMode: .fit)
                    .clipShape(RoundedRectangle(cornerRadius: 6))
            } else if failed {
                Label("Failed to load screenshot", systemImage: "photo")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                ProgressView()
            }
        }
        .frame(maxWidth: .infinity, minHeight: image == nil ? 150 : 0)
        .background(image == nil ? Color(.systemGray6) : .clear)
        .clipShape(RoundedRectangle(cornerRadius: 6))
        .task(id: url) { await loadImage() }
    }

    private func loadImage() async {
        var request = URLRequest(url: url)
        request.timeoutInterval = 10
        if let authToken {
            request.setValue("Bearer \(authToken)", forHTTPHeaderField: "Authorization")
        }
        do {
            let (data, response) = try await URLSession.shared.data(for: request)
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            if (200..<300).contains(status), let loaded = UIImage(data: data) {
                image = loaded
            } else {
                print("[ScreenshotView] HTTP \(status) for \(url)")
                failed = true
            }
        } catch {
            print("[ScreenshotView] Error loading \(url): \(error)")
            failed = true
        }
    }
}

// MARK: - Formatted Text (code blocks + markdown)

private struct FormattedText: View {
    let text: String

    var body: some View {
        let segments = parseSegments(text)
        VStack(alignment: .leading, spacing: 4) {
            ForEach(Array(segments.enumerated()), id: \.offset) { _, segment in
                if segment.isCode {
                    Text(segment.content)
                        .font(.system(size: 13, design: .monospaced))
                        .padding(8)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(Color(.systemGray6))
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                } else if !segment.content.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    markdownText(segment.content)
                }
            }
        }
    }

    /// Renders a text segment with markdown formatting (bold, italic, inline code, links).
    private func markdownText(_ raw: String) -> some View {
        let processed = Self.linkifyBareURLs(raw)
        let attributed = (try? AttributedString(markdown: processed, options: .init(
            interpretedSyntax: .inlineOnlyPreservingWhitespace
        ))) ?? AttributedString(raw)
        return Text(attributed)
            .font(.system(size: 15))
    }

    /// Finds bare URLs (not already inside markdown link syntax) and wraps them as markdown links.
    private static func linkifyBareURLs(_ text: String) -> String {
        // Match URLs that aren't preceded by ]( (already a markdown link target)
        // or by [ (start of markdown link text that happens to be a URL).
        let pattern = #"(?<!\]\()(?<!\[)(https?://[^\s\)\]>]+)"#
        guard let regex = try? NSRegularExpression(pattern: pattern) else { return text }
        let nsText = text as NSString
        var result = text
        // Process matches in reverse so indices stay valid.
        let matches = regex.matches(in: text, range: NSRange(location: 0, length: nsText.length))
        for match in matches.reversed() {
            guard let range = Range(match.range, in: result) else { continue }
            let url = String(result[range])
            // Strip trailing punctuation that's likely not part of the URL.
            let cleaned = url.replacingOccurrences(of: #"[.,;:!?\*]+$"#, with: "", options: .regularExpression)
            let suffix = String(url.dropFirst(cleaned.count))
            result.replaceSubrange(range, with: "[\(cleaned)](\(cleaned))\(suffix)")
        }
        return result
    }

    private struct Segment {
        let content: String
        let isCode: Bool
    }

    private func parseSegments(_ text: String) -> [Segment] {
        var segments: [Segment] = []
        let parts = text.components(separatedBy: "```")
        for (i, part) in parts.enumerated() {
            if i % 2 == 1 {
                var code = part
                if let newline = code.firstIndex(of: "\n") {
                    let firstLine = code[code.startIndex..<newline]
                    if firstLine.allSatisfy({ $0.isLetter || $0.isNumber }) {
                        code = String(code[code.index(after: newline)...])
                    }
                }
                segments.append(Segment(content: code, isCode: true))
            } else {
                segments.append(Segment(content: part, isCode: false))
            }
        }
        return segments
    }
}
