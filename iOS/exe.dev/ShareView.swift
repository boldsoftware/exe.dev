import SwiftUI

// MARK: - Share Data Models

struct ShareInfo: Decodable {
    let vmName: String
    let status: String // "public" or "private"
    let port: Int
    let url: String
    let links: [ShareLink]

    enum CodingKeys: String, CodingKey {
        case vmName = "vm_name"
        case status, port, url, links
    }
}

struct ShareLink: Decodable, Identifiable {
    let token: String
    let createdAt: String
    let useCount: Int
    let lastUsedAt: String?

    var id: String { token }

    enum CodingKeys: String, CodingKey {
        case token
        case createdAt = "created_at"
        case useCount = "use_count"
        case lastUsedAt = "last_used_at"
    }
}

// MARK: - Share View Model

@MainActor @Observable
final class ShareViewModel {
    var info: ShareInfo?
    var isLoading = false
    var error: String?
    var isBusy = false // for toggle/link operations

    private let vmName: String
    private let api: APIClient

    init(vmName: String, api: APIClient) {
        self.vmName = vmName
        self.api = api
    }

    func load() async {
        isLoading = true
        error = nil
        do {
            info = try await api.execJSON("share show \(vmName) --json")
        } catch {
            self.error = error.localizedDescription
        }
        isLoading = false
    }

    var isPublic: Bool { info?.status == "public" }

    func togglePublic() async {
        isBusy = true
        let cmd = isPublic ? "share set-private \(vmName) --json" : "share set-public \(vmName) --json"
        _ = try? await api.exec(cmd)
        await load()
        isBusy = false
    }

    func setPort(_ port: Int) async {
        isBusy = true
        _ = try? await api.exec("share port \(vmName) \(port) --json")
        await load()
        isBusy = false
    }

    /// Returns the share URL, creating a share link if none exists.
    func getOrCreateShareURL() async -> URL? {
        // If public, just use the direct URL
        if isPublic, let url = info?.url {
            return URL(string: url)
        }

        // If there's an existing share link, use it
        if let link = info?.links.first, let base = info?.url {
            return URL(string: "\(base)?share=\(link.token)")
        }

        // Create a new share link
        isBusy = true
        _ = try? await api.exec("share add-link \(vmName) --json")
        await load()
        isBusy = false

        if let link = info?.links.first, let base = info?.url {
            return URL(string: "\(base)?share=\(link.token)")
        }
        if let urlStr = info?.url {
            return URL(string: urlStr)
        }
        return nil
    }
}

// MARK: - Share View

struct ShareView: View {
    let viewModel: ShareViewModel
    @Environment(\.dismiss) private var dismiss
    @State private var shareURL: URL?
    @State private var showingPortPicker = false
    @State private var showingShareSheet = false
    @State private var copied = false

    var body: some View {
        NavigationStack {
            Form {
                if viewModel.isLoading && viewModel.info == nil {
                    Section {
                        HStack {
                            Spacer()
                            ProgressView()
                            Spacer()
                        }
                    }
                } else if let error = viewModel.error, viewModel.info == nil {
                    Section {
                        Label(error, systemImage: "exclamationmark.triangle")
                            .foregroundStyle(.secondary)
                    }
                } else if let info = viewModel.info {
                    // Public toggle
                    Section {
                        Toggle("Public", isOn: Binding(
                            get: { viewModel.isPublic },
                            set: { _ in Task { await viewModel.togglePublic() } }
                        ))
                        .disabled(viewModel.isBusy)
                    } footer: {
                        Text(viewModel.isPublic
                             ? "Anyone with the link can access this VM."
                             : "Only you and shared users can access this VM.")
                    }

                    // Port
                    Section {
                        Button {
                            showingPortPicker = true
                        } label: {
                            HStack {
                                Text("Port")
                                    .foregroundStyle(.primary)
                                Spacer()
                                Text("\(info.port)")
                                    .foregroundStyle(.secondary)
                                Image(systemName: "chevron.right")
                                    .font(.caption)
                                    .foregroundStyle(.tertiary)
                            }
                        }
                        .disabled(viewModel.isBusy)
                    }

                    // Share actions
                    Section {
                        // Copy link
                        Button {
                            Task {
                                if let url = await viewModel.getOrCreateShareURL() {
                                    UIPasteboard.general.string = url.absoluteString
                                    copied = true
                                    try? await Task.sleep(for: .seconds(2))
                                    copied = false
                                }
                            }
                        } label: {
                            Label(copied ? "Copied!" : "Copy Link",
                                  systemImage: copied ? "checkmark" : "doc.on.doc")
                        }
                        .disabled(viewModel.isBusy)

                        // System share sheet
                        Button {
                            Task {
                                shareURL = await viewModel.getOrCreateShareURL()
                                if shareURL != nil {
                                    showingShareSheet = true
                                }
                            }
                        } label: {
                            Label("Share...", systemImage: "square.and.arrow.up")
                        }
                        .disabled(viewModel.isBusy)
                    }

                    // Existing share links
                    if !info.links.isEmpty {
                        Section("Share Links") {
                            ForEach(info.links) { link in
                                HStack {
                                    VStack(alignment: .leading, spacing: 2) {
                                        Text(link.token.prefix(12) + "...")
                                            .font(.system(.caption, design: .monospaced))
                                        Text("\(link.useCount) use\(link.useCount == 1 ? "" : "s")")
                                            .font(.caption2)
                                            .foregroundStyle(.secondary)
                                    }
                                    Spacer()
                                }
                            }
                        }
                    }
                }
            }
            .navigationTitle("Share")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Done") { dismiss() }
                }
            }
            .sheet(isPresented: $showingPortPicker) {
                PortPickerView(currentPort: viewModel.info?.port ?? 80) { port in
                    Task { await viewModel.setPort(port) }
                }
            }
            .sheet(isPresented: $showingShareSheet) {
                if let url = shareURL {
                    ActivityView(items: [url])
                }
            }
        }
        .task { await viewModel.load() }
    }
}

// MARK: - Port Picker

private struct PortPickerView: View {
    let currentPort: Int
    let onSelect: (Int) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var portText: String

    private let commonPorts = [3000, 4000, 5000, 5173, 8000, 8080, 8888]

    init(currentPort: Int, onSelect: @escaping (Int) -> Void) {
        self.currentPort = currentPort
        self.onSelect = onSelect
        _portText = State(initialValue: "\(currentPort)")
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Common Ports") {
                    ForEach(commonPorts, id: \.self) { (port: Int) in
                        Button {
                            onSelect(port)
                            dismiss()
                        } label: {
                            HStack {
                                Text("\(port)")
                                    .foregroundStyle(.primary)
                                Spacer()
                                if port == currentPort {
                                    Image(systemName: "checkmark")
                                        .foregroundStyle(Color.accentColor)
                                }
                            }
                        }
                    }
                }

                Section("Custom Port") {
                    HStack {
                        TextField("3000–9999", text: $portText)
                            .keyboardType(.numberPad)
                        Button("Set") {
                            if let port = Int(portText), port >= 3000, port <= 9999 {
                                onSelect(port)
                                dismiss()
                            }
                        }
                        .disabled({
                            guard let port = Int(portText) else { return true }
                            return port < 3000 || port > 9999
                        }())
                    }
                }
            }
            .navigationTitle("Port")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
            }
        }
        .presentationDetents([.medium])
    }
}

// MARK: - UIActivityViewController Bridge

struct ActivityView: UIViewControllerRepresentable {
    let items: [Any]

    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: items, applicationActivities: nil)
    }

    func updateUIViewController(_ controller: UIActivityViewController, context: Context) {}
}
