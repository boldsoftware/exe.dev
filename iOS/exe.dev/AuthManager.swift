import AuthenticationServices
import Foundation
import Security

@Observable
final class AuthManager: NSObject {
    private(set) var isAuthenticated: Bool = false

    private static let service = "dev.exe.app-token"
    private static let account = "app-token"

    var baseURL: String = "https://exe.dev"

    override init() {
        super.init()
        isAuthenticated = Self.loadToken() != nil
    }

    var token: String? { Self.loadToken() }

    func signIn() {
        let urlString = "\(baseURL)/auth?response_mode=app_token&callback_uri=exedev-app://auth"
        guard let url = URL(string: urlString) else { return }

        let session = ASWebAuthenticationSession(
            url: url,
            callback: .customScheme("exedev-app")
        ) { [weak self] callbackURL, error in
            if let callbackURL {
                self?.handleCallback(url: callbackURL)
            }
        }
        session.prefersEphemeralWebBrowserSession = false
        session.presentationContextProvider = self
        session.start()
    }

    func handleCallback(url: URL) {
        guard let components = URLComponents(url: url, resolvingAgainstBaseURL: false),
              let token = components.queryItems?.first(where: { $0.name == "token" })?.value
        else { return }

        Self.saveToken(token)
        isAuthenticated = true
    }

    func signOut() {
        Self.deleteToken()
        isAuthenticated = false
    }

    // MARK: - Keychain

    private static func saveToken(_ token: String) {
        deleteToken()
        let data = Data(token.utf8)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecValueData as String: data,
            kSecAttrAccessible as String: kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly,
        ]
        SecItemAdd(query as CFDictionary, nil)
    }

    private static func loadToken() -> String? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var result: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        guard status == errSecSuccess, let data = result as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    private static func deleteToken() {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
        SecItemDelete(query as CFDictionary)
    }
}

extension AuthManager: ASWebAuthenticationPresentationContextProviding {
    nonisolated func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        MainActor.assumeIsolated {
            guard let scene = UIApplication.shared.connectedScenes
                .compactMap({ $0 as? UIWindowScene })
                .first
            else {
                return ASPresentationAnchor(windowScene: UIApplication.shared.connectedScenes.first as! UIWindowScene)
            }
            return scene.windows.first ?? ASPresentationAnchor(windowScene: scene)
        }
    }
}
