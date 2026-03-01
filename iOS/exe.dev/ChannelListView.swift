import SwiftUI
import SwiftData

struct ChannelListView: View {
    var auth: AuthManager
    var api: APIClient
    var syncEngine: SyncEngine

    @Query(sort: \StoredVM.vmName) private var allVMs: [StoredVM]
    @State private var isLoading = true
    @State private var error: String?
    @State private var selectedVMName: String?

    private var runningVMs: [StoredVM] { allVMs.filter(\.isRunning) }
    private var stoppedVMs: [StoredVM] { allVMs.filter { !$0.isRunning } }

    var body: some View {
        NavigationSplitView {
            sidebar
        } detail: {
            if let name = selectedVMName,
               let vm = allVMs.first(where: { $0.vmName == name }) {
                VMDetailView(vm: vm, api: api, syncEngine: syncEngine, token: auth.token)
                    .id(name)
            } else {
                ContentUnavailableView(
                    "Select a VM",
                    systemImage: "bubble.left.and.text.bubble.right",
                    description: Text("Choose a VM from the sidebar to chat.")
                )
            }
        }
    }

    private var sidebar: some View {
        Group {
            if isLoading && allVMs.isEmpty {
                ProgressView()
                    .frame(maxHeight: .infinity)
            } else if let error, allVMs.isEmpty {
                ContentUnavailableView {
                    Label("Unable to Load", systemImage: "exclamationmark.triangle")
                } description: {
                    Text(error)
                } actions: {
                    Button("Retry") { Task { await loadVMs() } }
                }
            } else if allVMs.isEmpty {
                ContentUnavailableView(
                    "No VMs",
                    systemImage: "server.rack",
                    description: Text("Create a VM at exe.dev to get started.")
                )
            } else {
                vmList
            }
        }
        .navigationTitle("exe.dev")
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Button("Sign Out", role: .destructive) { auth.signOut() }
                    .font(.footnote)
            }
        }
        .refreshable { await loadVMs() }
        .task { await loadVMs() }
    }

    private var vmList: some View {
        List(selection: $selectedVMName) {
            if !runningVMs.isEmpty {
                Section("Running") {
                    ForEach(runningVMs) { vm in
                        vmRow(vm)
                    }
                }
            }
            if !stoppedVMs.isEmpty {
                Section("Stopped") {
                    ForEach(stoppedVMs) { vm in
                        vmRow(vm)
                    }
                }
            }
        }
        .listStyle(.sidebar)
    }

    private func vmRow(_ vm: StoredVM) -> some View {
        HStack(spacing: 8) {
            Text("#")
                .font(.system(size: 18, weight: .bold, design: .monospaced))
                .foregroundStyle(.secondary)
            Text(vm.vmName)
                .font(.system(.body, design: .monospaced))
            Spacer()
            Circle()
                .fill(vm.isRunning ? .green : .gray.opacity(0.4))
                .frame(width: 8, height: 8)
        }
        .tag(vm.vmName)
        .disabled(vm.shelleyURL == nil)
    }

    private func loadVMs() async {
        isLoading = true
        error = nil
        do {
            try await syncEngine.refreshVMs(api: api)
            isLoading = false
        } catch {
            self.error = error.localizedDescription
            isLoading = false
        }
    }
}
