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
    @State private var showingNewVM = false
    @State private var pollingTask: Task<Void, Never>?
    @State private var creationWatchTask: Task<Void, Never>?

    private var creatingVMs: [StoredVM] { allVMs.filter(\.isCreating) }
    private var runningVMs: [StoredVM] { allVMs.filter { $0.isRunning && !$0.isCreating } }
    private var stoppedVMs: [StoredVM] { allVMs.filter { !$0.isRunning && !$0.isCreating } }

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
        .onChange(of: selectedVMName) { _, newName in
            if let newName {
                Task { await syncEngine.markVMAsRead(vmName: newName) }
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
                ContentUnavailableView {
                    Label("No VMs", systemImage: "server.rack")
                } description: {
                    Text("Create your first VM to get started.")
                } actions: {
                    Button("New VM") { showingNewVM = true }
                        .buttonStyle(.borderedProminent)
                }
            } else {
                vmList
            }
        }
        .navigationTitle("exe.dev")
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                Menu {
                    Button("Sign Out", role: .destructive) { auth.signOut() }
                } label: {
                    Image(systemName: "person.circle")
                }
            }
        }
        .refreshable { await loadVMs() }
        .task {
            await loadVMs()
            startPolling()
        }
        .onDisappear {
            pollingTask?.cancel()
        }
        .overlay(alignment: .bottomTrailing) {
            Button {
                showingNewVM = true
            } label: {
                Image(systemName: "plus")
                    .font(.title2.weight(.semibold))
                    .foregroundStyle(.white)
                    .frame(width: 52, height: 52)
                    .background(Color.accentColor, in: Circle())
                    .shadow(radius: 4, y: 2)
            }
            .padding(20)
        }
        .sheet(isPresented: $showingNewVM) {
            NewVMView(api: api) { hostname in
                Task {
                    // Insert placeholder immediately so it appears in the list.
                    await syncEngine.insertCreatingVM(hostname: hostname)
                    selectedVMName = hostname
                    watchCreation(hostname: hostname)
                }
            }
        }
    }

    private var vmList: some View {
        List(selection: $selectedVMName) {
            if !creatingVMs.isEmpty {
                Section("Creating") {
                    ForEach(creatingVMs) { vm in
                        vmRow(vm)
                    }
                }
            }
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
            if vm.isCreating {
                ProgressView()
                    .controlSize(.small)
            } else if vm.unreadCount > 0 {
                Text("\(vm.unreadCount)")
                    .font(.caption2.weight(.bold))
                    .foregroundStyle(.white)
                    .padding(.horizontal, 6)
                    .padding(.vertical, 2)
                    .background(.red, in: Capsule())
            }
            Circle()
                .fill(vm.isCreating ? .orange : vm.isRunning ? .green : .gray.opacity(0.4))
                .frame(width: 8, height: 8)
        }
        .tag(vm.vmName)
        .disabled(!vm.isRunning && !vm.isCreating)
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

    private func startPolling() {
        pollingTask?.cancel()
        pollingTask = Task.detached(priority: .utility) { [api, syncEngine] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(30))
                if Task.isCancelled { break }
                await refreshUnreadCounts(api: api, syncEngine: syncEngine)
            }
        }
    }

    /// Polls VM list every 2s until the given hostname is running (or 5 minutes elapse).
    private func watchCreation(hostname: String) {
        creationWatchTask?.cancel()
        creationWatchTask = Task {
            for _ in 0..<150 {
                try? await Task.sleep(for: .seconds(2))
                if Task.isCancelled { break }
                try? await syncEngine.refreshVMs(api: api)
                if let vm = allVMs.first(where: { $0.vmName == hostname }), vm.isRunning {
                    break
                }
            }
        }
    }
}

/// Fetches unread counts entirely off the actor — networking happens here,
/// then we hop onto the SyncEngine actor only to write the results.
private func refreshUnreadCounts(api: APIClient, syncEngine: SyncEngine) async {
    // 1. Snapshot the VM list from the actor (fast, no networking).
    let vmInfos = await syncEngine.runningVMsWithShelley()

    // 2. Fetch conversations concurrently, completely off the actor.
    await withTaskGroup(of: (String, Int).self) { group in
        for info in vmInfos {
            group.addTask {
                let count = await unreadCount(
                    api: api, shelleyURL: info.shelleyURL,
                    lastViewed: info.lastViewedAt
                )
                return (info.vmName, count)
            }
        }
        var results: [(String, Int)] = []
        for await result in group {
            results.append(result)
        }
        // 3. Write all results in one actor hop.
        await syncEngine.applyUnreadCounts(results)
    }
}

private func unreadCount(api: APIClient, shelleyURL: String, lastViewed: Date?) async -> Int {
    do {
        let conversations = try await api.listConversations(shelleyURL: shelleyURL)
        guard let latest = conversations.first else { return 0 }

        let cutoff = lastViewed ?? Date.distantPast
        if latest.updatedAt <= cutoff { return 0 }

        let convData = try await api.getConversation(shelleyURL: shelleyURL, id: latest.conversationID)
        return convData.messages?.filter { msg in
            msg.type == "agent" && msg.createdAt > cutoff && !msg.displayText.isEmpty
        }.count ?? 0
    } catch {
        return -1 // Signal: keep existing count
    }
}
