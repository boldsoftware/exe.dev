import SwiftUI
import WebKit

struct VMWebView: UIViewRepresentable {
    let url: URL
    let token: String?

    func makeUIView(context: Context) -> WKWebView {
        let webView = WKWebView(frame: .zero)

        guard let token else {
            webView.load(URLRequest(url: url))
            return webView
        }

        // Set the app token as a proxy auth cookie.
        // The proxy accepts "login-with-exe-<port>" cookies containing
        // app tokens (exeapp_...) for iOS web views that can't set headers.
        let port = url.port ?? 443
        let cookieName = "login-with-exe-\(port)"

        let cookieProps: [HTTPCookiePropertyKey: Any] = [
            .name: cookieName,
            .value: token,
            .domain: url.host ?? "",
            .path: "/",
            .secure: "TRUE",
        ]

        if let cookie = HTTPCookie(properties: cookieProps) {
            webView.configuration.websiteDataStore.httpCookieStore.setCookie(cookie) {
                webView.load(URLRequest(url: self.url))
            }
        } else {
            webView.load(URLRequest(url: url))
        }

        return webView
    }

    func updateUIView(_ webView: WKWebView, context: Context) {}
}
