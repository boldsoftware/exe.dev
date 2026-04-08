import SwiftUI
import SwiftData

struct ChannelListView: View {
    @Environment(\.openURL) private var openURL
    @Environment(\.scenePhase) private var scenePhase
    var auth: AuthManager
    var api: APIClient
    var syncEngine: SyncEngine

    @State private var allVMs: [VMListItem] = []
    @State private var isLoading = true
    @State private var error: String?
    @State private var selectedVMName: String?
    @State private var showingNewVM = false
    @State private var pollingTask: Task<Void, Never>?
    @State private var creationPollingTask: Task<Void, Never>?
    @State private var cpSource: VMListItem?
    @State private var vmListScrollOffset: CGFloat = 0

    private var creatingVMs: [VMListItem] { allVMs.filter(\.isCreating) }
    private var creatingVMNames: [String] { creatingVMs.map(\.vmName).sorted() }
    private var vmSections: [VMListSection<VMListItem>] { VMListGrouping.sections(for: allVMs) }
    private var brandingOpacity: Double {
        guard !allVMs.isEmpty else { return 1 }
        let fadeDistance: CGFloat = 36
        let progress = min(max(vmListScrollOffset, 0), fadeDistance) / fadeDistance
        return Double(1 - progress)
    }

    var body: some View {
        NavigationSplitView {
            sidebar
        } detail: {
            if let name = selectedVMName,
               let vm = allVMs.first(where: { $0.vmName == name }) {
                VMDetailView(vm: vm, api: api, syncEngine: syncEngine, token: auth.token)
                    .id(VMListReloadPolicy.detailIdentity(vmName: name, isCreating: vm.isCreating))
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
        .task {
            await reloadVMsFromSyncEngine()
            await loadVMs()
            startPolling()
            updateCreationPolling(for: creatingVMNames, reason: .observedListChange)
        }
        .onReceive(NotificationCenter.default.publisher(for: .syncEngineDidSave)) { notification in
            let kind = notification.userInfo?[SyncEngineSaveNotificationUserInfoKey.kind] as? String
            guard VMListReloadPolicy.shouldReload(for: kind) else { return }
            Task { await reloadVMsFromSyncEngine() }
        }
        .onDisappear {
            pollingTask?.cancel()
            creationPollingTask?.cancel()
            creationPollingTask = nil
        }
        .onChange(of: creatingVMNames) { _, newNames in
            updateCreationPolling(for: newNames, reason: .observedListChange)
        }
        .onChange(of: scenePhase) { _, newPhase in
            guard newPhase == .active else { return }
            Task { await loadVMs() }
        }
    }

    private var sidebar: some View {
        Group {
            if isLoading && allVMs.isEmpty {
                sidebarState {
                    ProgressView()
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            } else if let error, allVMs.isEmpty {
                sidebarState {
                    ContentUnavailableView {
                        Label("Unable to Load", systemImage: "exclamationmark.triangle")
                    } description: {
                        Text(error)
                    } actions: {
                        Button("Retry") { Task { await loadVMs() } }
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            } else if allVMs.isEmpty {
                sidebarState {
                    ContentUnavailableView {
                        Label("No VMs", systemImage: "server.rack")
                    } description: {
                        Text("Create your first VM to get started.")
                    } actions: {
                        Button("New VM") { showingNewVM = true }
                            .buttonStyle(.borderedProminent)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            } else {
                vmList
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background {
            VMListBackground()
        }
        .toolbar(.hidden, for: .navigationBar)
        .refreshable { await loadVMs() }
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
                    await handleCreatedVM(hostname: hostname)
                }
            }
        }
        .sheet(item: $cpSource) { sourceVM in
            CopyVMView(api: api, sourceVMName: sourceVM.vmName) { newName in
                Task {
                    await handleCreatedVM(hostname: newName)
                }
            }
        }
    }

    private var vmList: some View {
        List(selection: $selectedVMName) {
            sidebarHeader(opacity: brandingOpacity)
                .listRowInsets(EdgeInsets())
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
            ForEach(Array(vmSections.enumerated()), id: \.offset) { _, section in
                Section {
                    ForEach(section.items) { vm in
                        vmRow(vm)
                            .listRowBackground(Color(uiColor: .secondarySystemBackground))
                    }
                } header: {
                    if let title = section.title {
                        HStack(spacing: 6) {
                            Text(title)
                                .font(.system(.footnote, design: .monospaced).weight(.semibold))
                                .foregroundStyle(.secondary)
                            Text("\(section.items.count)")
                                .font(.system(.footnote, design: .monospaced))
                                .foregroundStyle(.tertiary)
                        }
                        .textCase(nil)
                    }
                }
            }
        }
        .listStyle(.plain)
        .listSectionSpacing(.custom(16))
        .scrollContentBackground(.hidden)
        .background(Color.clear)
        .onScrollGeometryChange(for: CGFloat.self) { geometry in
            geometry.contentOffset.y + geometry.contentInsets.top
        } action: { _, newValue in
            vmListScrollOffset = newValue
        }
    }

    private func sidebarState<Content: View>(@ViewBuilder content: () -> Content) -> some View {
        VStack(spacing: 0) {
            sidebarHeader(opacity: 1)
            content()
        }
    }

    private func sidebarHeader(opacity: Double) -> some View {
        HStack(spacing: 12) {
            HStack(spacing: 10) {
                Image("Exy")
                    .resizable()
                    .aspectRatio(contentMode: .fit)
                    .frame(width: 30, height: 30)
                Text(AuthManager.webHost)
                    .font(.system(size: 24, weight: .semibold, design: .rounded))
                    .foregroundStyle(.primary)
            }

            Spacer(minLength: 12)

            Menu {
                Button("Sign Out", role: .destructive) { auth.signOut() }
            } label: {
                Image(systemName: "person.circle")
                    .font(.system(size: 19, weight: .medium))
                    .foregroundStyle(.primary)
                    .frame(width: 40, height: 40)
                    .background(Color(uiColor: .secondarySystemBackground), in: Circle())
            }
            .buttonStyle(.plain)
        }
        .padding(.horizontal, 16)
        .padding(.top, 8)
        .padding(.bottom, 12)
        .opacity(opacity)
        .animation(.easeOut(duration: 0.18), value: opacity)
        .accessibilityElement(children: .contain)
    }

    private func vmRow(_ vm: VMListItem) -> some View {
        HStack(spacing: 8) {
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
        }
        .tag(vm.vmName)
        .contextMenu {
            if let safariURL = safariURL(for: vm) {
                Button {
                    openURL(safariURL)
                } label: {
                    Label("Open in Safari", systemImage: "safari")
                }
            }
            if vm.isRunning {
                Button {
                    cpSource = vm
                } label: {
                    Label("Copy VM...", systemImage: "doc.on.doc")
                }
            }
        }
        .selectionDisabled(!vm.isRunning && !vm.isCreating)
    }

    private func safariURL(for vm: VMListItem) -> URL? {
        guard !vm.httpsURL.isEmpty,
              let url = URL(string: vm.httpsURL),
              url.scheme != nil
        else {
            return nil
        }
        return url
    }

    private func loadVMs() async {
        isLoading = true
        error = nil
        do {
            try await syncEngine.refreshVMs(api: api)
            await reloadVMsFromSyncEngine()
            isLoading = false
        } catch {
            self.error = error.localizedDescription
            await reloadVMsFromSyncEngine()
            isLoading = false
        }
    }

    private func startPolling() {
        pollingTask?.cancel()
        pollingTask = Task.detached(priority: .utility) { [api, syncEngine] in
            // Refresh immediately on first run, then every 30 seconds.
            while !Task.isCancelled {
                await refreshUnreadCounts(api: api, syncEngine: syncEngine)
                try? await Task.sleep(for: .seconds(30))
            }
        }
    }

    @MainActor
    private func handleCreatedVM(hostname: String) async {
        await syncEngine.insertCreatingVM(hostname: hostname)
        await reloadVMsFromSyncEngine()
        selectedVMName = hostname
        updateCreationPolling(for: [hostname], reason: .createdVM)
        await loadVMs()
    }

    @MainActor
    private func reloadVMsFromSyncEngine() async {
        allVMs = await syncEngine.vmListItems()
        if let selectedVMName,
           !allVMs.contains(where: { $0.vmName == selectedVMName }) {
            self.selectedVMName = nil
        }
    }

    private func updateCreationPolling(
        for names: [String],
        reason: VMCreationPollingReason
    ) {
        switch VMCreationPolling.action(
            hasRunningTask: creationPollingTask != nil,
            observedCreatingVMNames: names,
            reason: reason
        ) {
        case .keepCurrent:
            return
        case .cancel:
            creationPollingTask?.cancel()
            creationPollingTask = nil
            return
        case .start:
            creationPollingTask?.cancel()
        }

        creationPollingTask = Task.detached(priority: .utility) { [api, syncEngine] in
            while !Task.isCancelled {
                try? await syncEngine.refreshVMs(api: api)
                if Task.isCancelled { break }
                let pendingNames = await syncEngine.creatingVMNames()
                if pendingNames.isEmpty { break }
                try? await Task.sleep(for: .seconds(2))
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
        guard let messages = convData.messages else { return 0 }

        // Count completed turns, not individual messages.
        // A turn ends when an endOfTurn message appears after non-endOfTurn
        // messages. Consecutive endOfTurn messages (e.g. max-tokens
        // truncation producing two) count as a single turn.
        var turnCount = 0
        var prevWasEndOfTurn = false
        for msg in messages where msg.createdAt > cutoff {
            if msg.endOfTurn == true {
                if !prevWasEndOfTurn {
                    turnCount += 1
                }
                prevWasEndOfTurn = true
            } else {
                prevWasEndOfTurn = false
            }
        }
        return turnCount
    } catch {
        return -1 // Signal: keep existing count
    }
}

private struct VMListBackground: View {
    var body: some View {
        ZStack {
            Color(uiColor: .systemGroupedBackground)
            Canvas { context, size in
                let stripeHeight: CGFloat = 2
                let stripeSpacing: CGFloat = 4
                let stripeColor = Color.primary.opacity(0.03)

                for y in stride(from: stripeHeight, through: size.height, by: stripeSpacing) {
                    let rect = CGRect(x: 0, y: y, width: size.width, height: stripeHeight)
                    context.fill(Path(rect), with: .color(stripeColor))
                }
            }
        }
        .ignoresSafeArea()
    }
}
