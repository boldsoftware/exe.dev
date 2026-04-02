import Foundation
import Security

nonisolated enum AuthTokenStore {
    private static let service = "dev.\(AppEnvironment.current.webHost).app-token"
    private static let account = "app-token"

    static func loadToken() -> String? {
        if let shared = loadToken(from: sharedQuery()) {
            return shared
        }
        if let legacy = loadToken(from: legacyQuery()) {
            saveToken(legacy)
            deleteToken(from: legacyQuery())
            return legacy
        }
        return nil
    }

    static func saveToken(_ token: String) {
        deleteToken(from: sharedQuery())

        var query = sharedQuery()
        query[kSecValueData as String] = Data(token.utf8)
        query[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        SecItemAdd(query as CFDictionary, nil)
    }

    static func deleteToken() {
        deleteToken(from: sharedQuery())
        deleteToken(from: legacyQuery())
    }

    private static func sharedQuery() -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecAttrAccessGroup as String: SharedAppConfiguration.sharedKeychainAccessGroup,
        ]
    }

    private static func legacyQuery() -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
        ]
    }

    private static func loadToken(from baseQuery: [String: Any]) -> String? {
        var query = baseQuery
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne

        var result: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        guard status == errSecSuccess, let data = result as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    private static func deleteToken(from query: [String: Any]) {
        SecItemDelete(query as CFDictionary)
    }
}
