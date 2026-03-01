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

            ZStack {
                ChannelView(viewModel: channelViewModel)
                    .opacity(selectedTab == 0 ? 1 : 0)
                    .allowsHitTesting(selectedTab == 0)

                if let url = URL(string: vm.httpsURL) {
                    VMWebView(url: url, token: token)
                        .opacity(selectedTab == 1 ? 1 : 0)
                        .allowsHitTesting(selectedTab == 1)
                }
            }
        }
        .navigationTitle("# \(vm.vmName)")
        .navigationBarTitleDisplayMode(.inline)
    }
}
