import SwiftUI

struct ContentView: View {
    var auth: AuthManager
    let syncEngine: SyncEngine

    var body: some View {
        if auth.isAuthenticated {
            ChannelListView(
                auth: auth,
                api: APIClient(baseURL: auth.baseURL, auth: auth),
                syncEngine: syncEngine
            )
        } else {
            AuthView(auth: auth)
        }
    }
}
