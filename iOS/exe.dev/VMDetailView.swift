import SwiftUI

struct VMDetailView: View {
    let vm: StoredVM
    let api: APIClient
    let syncEngine: SyncEngine
    let token: String?

    @State private var selectedTab = 0
    @State private var channelViewModel: ChannelViewModel

    init(vm: StoredVM, api: APIClient, syncEngine: SyncEngine, token: String?) {
        self.vm = vm
        self.api = api
        self.syncEngine = syncEngine
        self.token = token
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
            }
            .pickerStyle(.segmented)
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
            .disabled(vm.isCreating)

            ZStack {
                if vm.isCreating {
                    VMCreatingView(vmName: vm.vmName)
                } else {
                    ChannelView(viewModel: channelViewModel)
                }

                if selectedTab == 1, !vm.isCreating, let url = URL(string: vm.httpsURL) {
                    VMWebView(url: url, token: token)
                }
            }
        }
        .navigationTitle("# \(vm.vmName)")
        .navigationBarTitleDisplayMode(.inline)
        .task {
            await syncEngine.markVMAsRead(vmName: vm.vmName)
        }
        .task(id: vm.isCreating) {
            // Poll aggressively while the VM is being created.
            guard vm.isCreating else { return }
            while !Task.isCancelled {
                try? await Task.sleep(for: .seconds(2))
                if Task.isCancelled { break }
                try? await syncEngine.refreshVMs(api: api)
            }
        }
        .onChange(of: vm.shelleyURL) { _, newURL in
            if let newURL, channelViewModel.shelleyURL == nil {
                channelViewModel.shelleyURL = newURL
                Task { await channelViewModel.loadLatestConversation() }
            }
        }
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
