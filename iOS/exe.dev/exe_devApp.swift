import SwiftUI
import SwiftData

class AppDelegate: NSObject, UIApplicationDelegate {
    var pushManager: PushManager?

    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        return true
    }

    func application(
        _ application: UIApplication,
        didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data
    ) {
        pushManager?.didReceiveDeviceToken(deviceToken)
    }

    func application(
        _ application: UIApplication,
        didFailToRegisterForRemoteNotificationsWithError error: Error
    ) {
        print("Failed to register for remote notifications: \(error)")
    }
}

@main
struct exe_devApp: App {
    @UIApplicationDelegateAdaptor private var appDelegate: AppDelegate
    @State private var auth = AuthManager()
    @State private var pushManager = PushManager()
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
            ContentView(auth: auth, syncEngine: syncEngine, pushManager: pushManager)
                .onOpenURL { url in
                    auth.handleCallback(url: url)
                }
                .onAppear {
                    appDelegate.pushManager = pushManager
                }
        }
        .modelContainer(container)
    }
}
