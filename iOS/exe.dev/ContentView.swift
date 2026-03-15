import SwiftUI

struct ContentView: View {
    var auth: AuthManager
    let syncEngine: SyncEngine
    var pushManager: PushManager

    var body: some View {
        if auth.isAuthenticated {
            ChannelListView(
                auth: auth,
                api: APIClient(baseURL: auth.baseURL, auth: auth),
                syncEngine: syncEngine
            )
            .task {
                await pushManager.requestPermissionAndRegister()
            }
            .task(id: pushManager.deviceToken) {
                guard pushManager.deviceToken != nil else { return }
                let api = APIClient(baseURL: auth.baseURL, auth: auth)
                await pushManager.uploadTokenIfNeeded(apiClient: api)
            }
        } else {
            AuthView(auth: auth)
                .onAppear {
                    pushManager.clearToken()
                }
        }
    }
}
