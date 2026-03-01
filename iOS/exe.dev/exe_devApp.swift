import SwiftUI
import SwiftData

@main
struct exe_devApp: App {
    @State private var auth = AuthManager()
    let container: ModelContainer
    let syncEngine: SyncEngine

    init() {
        let container = try! ModelContainer(
            for: StoredVM.self, StoredConversation.self, StoredMessage.self
        )
        self.container = container
        self.syncEngine = SyncEngine(modelContainer: container)
    }

    var body: some Scene {
        WindowGroup {
            ContentView(auth: auth, syncEngine: syncEngine)
                .onOpenURL { url in
                    auth.handleCallback(url: url)
                }
        }
        .modelContainer(container)
    }
}
