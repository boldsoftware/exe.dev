import Foundation

nonisolated enum AppEnvironment {
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

nonisolated enum SharedAppConfiguration {
    static let appGroupIdentifier = "group.dev.exe.exe-dev"
    static let sharedKeychainAccessGroup = "4735MG39D5.dev.exe.shared"
    static let loggingSubsystem = "dev.exe.exe-dev"
    static let legacyBackgroundUploadSessionIdentifier = "dev.exe.exe-dev.share-upload"
    static let appBackgroundUploadSessionIdentifier = "dev.exe.exe-dev.share-upload.app"
    static let shareExtensionBackgroundUploadSessionIdentifier = "dev.exe.exe-dev.share-upload.extension"

    static let queuedShareItemsDefaultsKey = "queued_share_items"
    static let cachedVMsDefaultsKey = "cached_vm_summaries"
    static let lastSharedVMNameDefaultsKey = "last_shared_vm_name"
    static let conversationTargetsDefaultsKey = "conversation_targets"

    static let pendingSharesDirectoryName = "PendingShares"

    static let appBaseURL = "https://\(AppEnvironment.current.webHost)"

    static var isRunningInExtension: Bool {
        Bundle.main.infoDictionary?["NSExtension"] != nil
    }

    static var currentBackgroundUploadSessionIdentifier: String {
        if isRunningInExtension {
            return shareExtensionBackgroundUploadSessionIdentifier
        }
        return appBackgroundUploadSessionIdentifier
    }

    static func isKnownBackgroundUploadSessionIdentifier(_ identifier: String) -> Bool {
        identifier == legacyBackgroundUploadSessionIdentifier
            || identifier == appBackgroundUploadSessionIdentifier
            || identifier == shareExtensionBackgroundUploadSessionIdentifier
    }

    static var sharedDefaults: UserDefaults {
        guard let defaults = UserDefaults(suiteName: appGroupIdentifier) else {
            fatalError("Missing app group user defaults for \(appGroupIdentifier)")
        }
        return defaults
    }

    static var appGroupContainerURL: URL {
        guard let url = FileManager.default.containerURL(
            forSecurityApplicationGroupIdentifier: appGroupIdentifier
        ) else {
            fatalError("Missing app group container for \(appGroupIdentifier)")
        }
        return url
    }

    static var pendingSharesDirectoryURL: URL {
        appGroupContainerURL.appendingPathComponent(pendingSharesDirectoryName, isDirectory: true)
    }

    @discardableResult
    static func ensurePendingSharesDirectory() throws -> URL {
        let url = pendingSharesDirectoryURL
        try FileManager.default.createDirectory(
            at: url,
            withIntermediateDirectories: true
        )
        return url
    }
}
