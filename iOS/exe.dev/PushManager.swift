import Foundation
import UIKit
import UserNotifications

@Observable
final class PushManager {
    private(set) var deviceToken: String?
    private var uploadedToken: String? {
        get { UserDefaults.standard.string(forKey: "push_uploaded_token") }
        set { UserDefaults.standard.set(newValue, forKey: "push_uploaded_token") }
    }

    /// The APNs environment for this build. Xcode debug builds use the sandbox
    /// environment; TestFlight and App Store builds use production. The entitlements
    /// file controls which environment iOS assigns, but Apple overrides it to
    /// "production" at signing time for distribution builds.
    static var apnsEnvironment: String {
        #if DEBUG
        return "sandbox"
        #else
        return "production"
        #endif
    }

    func requestPermissionAndRegister() async {
        do {
            let granted = try await UNUserNotificationCenter.current()
                .requestAuthorization(options: [.alert, .sound, .badge])
            guard granted else { return }
            await MainActor.run {
                UIApplication.shared.registerForRemoteNotifications()
            }
        } catch {
            print("Push permission error: \(error)")
        }
    }

    func didReceiveDeviceToken(_ token: Data) {
        deviceToken = token.map { String(format: "%02x", $0) }.joined()
    }

    func uploadTokenIfNeeded(apiClient: APIClient) async {
        guard let deviceToken else { return }

        // Always re-upload: the server may have removed the token
        // (e.g. after an APNs error) and we have no way to know.
        do {
            try await apiClient.registerPushToken(deviceToken, environment: Self.apnsEnvironment)
            uploadedToken = deviceToken
        } catch {
            print("Push token upload error: \(error)")
        }
    }

    func clearToken() {
        deviceToken = nil
        uploadedToken = nil
    }
}
