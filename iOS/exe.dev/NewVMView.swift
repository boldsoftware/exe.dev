import SwiftUI

struct NewVMView: View {
    let api: APIClient
    let onCreated: (String) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var hostname = ""
    @State private var prompt = ""
    @State private var isCreating = false
    @State private var error: String?
    @State private var hostnameValidation: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("VM Name", text: $hostname)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .font(.system(.body, design: .monospaced))
                        .onChange(of: hostname) { _, _ in
                            hostnameValidation = nil
                        }
                    if let hostnameValidation {
                        Text(hostnameValidation)
                            .font(.caption)
                            .foregroundStyle(.red)
                    }
                } header: {
                    Text("Name")
                } footer: {
                    Text("Lowercase letters, numbers, and hyphens.")
                }

                Section {
                    TextField("What should this VM do?", text: $prompt, axis: .vertical)
                        .lineLimit(3...8)
                } header: {
                    Text("Initial Prompt")
                } footer: {
                    Text("Shelley will start working on this when the VM is ready.")
                }

                if let error {
                    Section {
                        Label(error, systemImage: "exclamationmark.triangle.fill")
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle("New VM")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if isCreating {
                        ProgressView()
                    } else {
                        Button("Create") { Task { await create() } }
                            .disabled(hostname.isEmpty)
                    }
                }
            }
            .interactiveDismissDisabled(isCreating)
        }
        .onAppear {
            hostname = Self.randomHostname()
        }
    }

    private func create() async {
        let name = hostname.lowercased().trimmingCharacters(in: .whitespacesAndNewlines)
        error = nil
        hostnameValidation = nil

        // Validate hostname first
        do {
            let check = try await api.checkHostname(name)
            if !check.valid || !check.available {
                hostnameValidation = check.message ?? "Name is not available."
                return
            }
        } catch {
            self.error = error.localizedDescription
            return
        }

        isCreating = true
        do {
            try await api.createVM(
                hostname: name,
                prompt: prompt.trimmingCharacters(in: .whitespacesAndNewlines)
            )
            dismiss()
            onCreated(name)
        } catch {
            self.error = error.localizedDescription
            isCreating = false
        }
    }

    // MARK: - Client-side hostname generation

    private static let words = [
        "alpine", "amber", "arctic", "autumn", "azure",
        "blazing", "bold", "bright", "bronze", "calm",
        "cedar", "citrus", "clear", "clever", "cloud",
        "cobalt", "coral", "cosmic", "crisp", "crystal",
        "dapper", "dawn", "deep", "delta", "dew",
        "drift", "dune", "dusk", "eager", "ember",
        "epic", "fern", "flint", "flora", "flux",
        "forge", "frost", "gale", "gem", "gentle",
        "glacier", "gleam", "glow", "golden", "grand",
        "grove", "haze", "hollow", "horizon", "hue",
        "iron", "ivy", "jade", "jewel", "keen",
        "lake", "lark", "lemon", "light", "lime",
        "lunar", "maple", "meadow", "mist", "moon",
        "noble", "nova", "oak", "ocean", "olive",
        "opal", "orbit", "palm", "peak", "pine",
        "plum", "pond", "prism", "pulse", "quartz",
        "rain", "raven", "reef", "ridge", "river",
        "ruby", "rust", "sage", "sand", "shadow",
        "shore", "silk", "silver", "slate", "snow",
        "solar", "spark", "spire", "star", "steel",
        "stone", "storm", "stream", "sun", "swift",
        "thorn", "thunder", "tide", "timber", "topaz",
        "trail", "vale", "vapor", "velvet", "vine",
        "violet", "vivid", "wave", "wild", "willow",
        "wind", "winter", "zen",
    ]

    static func randomHostname() -> String {
        let w1 = words.randomElement()!
        var w2 = words.randomElement()!
        while w1 == w2 { w2 = words.randomElement()! }
        return "\(w1)-\(w2)"
    }
}
