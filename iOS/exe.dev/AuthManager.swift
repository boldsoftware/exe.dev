import AuthenticationServices
import Foundation

@Observable
final class AuthManager: NSObject {
    static let webHost = AppEnvironment.current.webHost
    static let boxHost = AppEnvironment.current.boxHost
    static let appBaseURL = "https://\(webHost)"

    private(set) var isAuthenticated: Bool = false
    private(set) var token: String?

    var baseURL: String = AuthManager.appBaseURL

    override init() {
        super.init()
        token = AuthTokenStore.loadToken()
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

        AuthTokenStore.saveToken(newToken)
        token = newToken
        isAuthenticated = true
    }

    func signOut() {
        AuthTokenStore.deleteToken()
        token = nil
        isAuthenticated = false
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
