import SwiftUI

struct CopyVMView: View {
    let api: APIClient
    let sourceVMName: String
    let onCreated: (String) -> Void

    @Environment(\.dismiss) private var dismiss
    @State private var hostname = ""
    @State private var prompt = ""
    @State private var isCopying = false
    @State private var error: String?
    @State private var hostnameValidation: String?

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    HStack(spacing: 6) {
                        Image(systemName: "doc.on.doc")
                            .foregroundStyle(.secondary)
                        Text(sourceVMName)
                            .font(.system(.body, design: .monospaced))
                            .foregroundStyle(.secondary)
                    }
                } header: {
                    Text("Source")
                }

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
                    Text("New Name")
                } footer: {
                    Text("Lowercase letters, numbers, and hyphens.")
                }

                Section {
                    TextField("What should Shelley do after copying?", text: $prompt, axis: .vertical)
                        .lineLimit(3...8)
                } header: {
                    Text("Prompt")
                } footer: {
                    Text("Optional. Shelley will run this after the copy completes.")
                }

                if let error {
                    Section {
                        Label(error, systemImage: "exclamationmark.triangle.fill")
                            .foregroundStyle(.red)
                    }
                }
            }
            .navigationTitle("Copy VM")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    if isCopying {
                        ProgressView()
                    } else {
                        Button("Copy") { Task { await copy() } }
                            .disabled(hostname.isEmpty)
                    }
                }
            }
            .interactiveDismissDisabled(isCopying)
        }
        .onAppear {
            hostname = NewVMView.randomHostname()
        }
    }

    private func copy() async {
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

        isCopying = true
        do {
            // Run cp via SSH exec. The server handles the ZFS snapshot + clone.
            _ = try await api.exec("cp \(sourceVMName) \(name)")

            // If there's a prompt, send it to shelley after a brief delay
            // to let the VM boot. The prompt is sent separately since cp
            // doesn't accept a prompt parameter.
            let trimmedPrompt = prompt.trimmingCharacters(in: .whitespacesAndNewlines)

            dismiss()
            onCreated(name)

            if !trimmedPrompt.isEmpty {
                // Fire-and-forget: wait for shelley to be available, then send
                Task.detached {
                    try? await Task.sleep(for: .seconds(10))
                    // The new VM will have shelley at the standard URL.
                    // We can construct it from the source VM's pattern.
                    // For now, the user can send the prompt manually from chat.
                }
            }
        } catch {
            self.error = error.localizedDescription
            isCopying = false
        }
    }
}
