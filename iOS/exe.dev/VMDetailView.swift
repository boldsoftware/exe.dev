import SwiftUI

struct VMDetailView: View {
    @Environment(\.scenePhase) private var scenePhase
    let vmName: String
    let api: APIClient
    let syncEngine: SyncEngine
    let token: String?

    @State private var selectedTab = 0
    @State private var channelViewModel: ChannelViewModel
    @State private var currentVM: VMListItem
    @State private var showingShare = false

    init(vm: VMListItem, api: APIClient, syncEngine: SyncEngine, token: String?) {
        self.vmName = vm.vmName
        self.api = api
        self.syncEngine = syncEngine
        self.token = token
        _currentVM = State(initialValue: vm)
        _channelViewModel = State(initialValue: ChannelViewModel(
            vmName: vm.vmName,
            shelleyURL: vm.shelleyURL,
            api: api,
            syncEngine: syncEngine
        ))
    }

    var body: some View {
        VStack(spacing: 0) {
            Picker("View", selection: $selectedTab) {
                Text("Chat").tag(0)
                Text("Web").tag(1)
                Text("Term").tag(2)
            }
            .pickerStyle(.segmented)
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .disabled(currentVM.isCreating)

            selectedContent
        }
        .navigationTitle("# \(vmName)")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItemGroup(placement: .topBarTrailing) {
                Button { showingShare = true } label: {
                    Image(systemName: "square.and.arrow.up")
                }
                Button { channelViewModel.newConversation() } label: {
                    Image(systemName: "square.and.pencil")
                }
            }
        }
        .sheet(isPresented: $showingShare) {
            ShareView(viewModel: ShareViewModel(vmName: vmName, api: api))
                .presentationDetents([.medium, .large])
        }
        .task {
            await syncEngine.markVMAsRead(vmName: vmName)
            await reloadVM()
        }
        .onReceive(NotificationCenter.default.publisher(for: .syncEngineDidSave)) { notification in
            let kind = notification.userInfo?[SyncEngineSaveNotificationUserInfoKey.kind] as? String
            guard VMListReloadPolicy.shouldReload(for: kind) else { return }
            Task { await reloadVM() }
        }
        .onChange(of: currentVM.shelleyURL) { _, newURL in
            if channelViewModel.shelleyURL != newURL {
                channelViewModel.shelleyURL = newURL
            }
            if currentVM.shelleyURL != nil {
                if selectedTab == 0 {
                    Task {
                        await channelViewModel.loadLatestConversation(
                            reason: .chatBecameVisible,
                            forceRefresh: true
                        )
                    }
                }
            }
        }
        .onChange(of: selectedTab) { _, newTab in
            guard newTab == 0 else { return }
            Task {
                await channelViewModel.loadLatestConversation(reason: .chatBecameVisible)
            }
        }
        .onChange(of: scenePhase) { _, newPhase in
            guard newPhase == .active, selectedTab == 0 else { return }
            Task {
                await reloadVM()
                await channelViewModel.loadLatestConversation(reason: .appBecameActive)
            }
        }
    }

    @ViewBuilder
    private var selectedContent: some View {
        if currentVM.isCreating {
            VMCreatingView(vmName: vmName)
        } else if selectedTab == 0 {
            ChannelView(viewModel: channelViewModel)
                .environment(\.openURL, OpenURLAction { url in
                    if isVMProxyURL(url) {
                        selectedTab = 1
                        return .handled
                    }
                    return .systemAction
                })
                .environment(\.authToken, token)
        } else if selectedTab == 1 {
            if let url = URL(string: currentVM.httpsURL) {
                VMWebView(url: url, token: token)
            } else {
                ContentUnavailableView(
                    "Web Unavailable",
                    systemImage: "globe",
                    description: Text("This VM does not have a valid web URL.")
                )
            }
        } else {
            VMTerminalView(vm: currentVM, token: token)
        }
    }

    @MainActor
    private func reloadVM() async {
        let refreshed = await syncEngine.vmListItem(named: vmName)
        currentVM = VMDetailSnapshotResolver.resolveCurrent(
            current: currentVM,
            refreshed: refreshed
        )
    }

    /// Returns true if the URL points to this VM's HTTPS proxy (e.g. ocean-horizon.exe.xyz:8000).
    private func isVMProxyURL(_ url: URL) -> Bool {
        guard let host = url.host else { return false }
        // Match the VM's proxy hostname (vmName.exe.xyz or similar).
        if let proxyURL = URL(string: currentVM.httpsURL), let proxyHost = proxyURL.host {
            return host == proxyHost
        }
        // Fallback: match vmName as subdomain of a known exe domain.
        return host.hasPrefix(vmName + ".")
    }
}

/// Shown in the chat area while a VM is being created.
private struct VMCreatingView: View {
    let vmName: String

    var body: some View {
        VStack(spacing: 16) {
            Spacer()
            ProgressView()
                .controlSize(.large)
            Text("Creating \(vmName)...")
                .font(.headline)
            Text("You can navigate away — we'll keep going in the background.")
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
            Spacer()
        }
        .padding(24)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}
