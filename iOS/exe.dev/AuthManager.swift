import AuthenticationServices
import Foundation
import Security

enum AppEnvironment {
    case production
    case staging

    // Flip this when pointing the app at staging.
    static let current: Self = .production

    var webHost: String {
        switch self {
        case .production:
            return "exe.dev"
        case .staging:
            return "exe-staging.dev"
        }
    }

    var boxHost: String {
        switch self {
        case .production:
            return "exe.xyz"
        case .staging:
            return "exe-staging.xyz"
        }
    }
}

@Observable
final class AuthManager: NSObject {
    static let webHost = AppEnvironment.current.webHost
    static let boxHost = AppEnvironment.current.boxHost
    static let appBaseURL = "https://\(webHost)"

    private(set) var isAuthenticated: Bool = false
    private(set) var token: String?

    private static let service = "dev.\(webHost).app-token"
    private static let account = "app-token"

    var baseURL: String = AuthManager.appBaseURL

    override init() {
        super.init()
        token = Self.loadToken()
        isAuthenticated = token != nil
    }

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
              let newToken = components.queryItems?.first(where: { $0.name == "token" })?.value
        else { return }

        Self.saveToken(newToken)
        token = newToken
        isAuthenticated = true
    }

    func signOut() {
        Self.deleteToken()
        token = nil
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
