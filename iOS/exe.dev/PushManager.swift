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
        guard let deviceToken, deviceToken != uploadedToken else { return }

        do {
            try await apiClient.registerPushToken(deviceToken)
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
