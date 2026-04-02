import Foundation

nonisolated struct SharedVMSummary: Codable, Identifiable, Equatable, Sendable {
    let vmName: String
    let status: String
    let shelleyURL: String?

    var id: String { vmName }

    var canReceiveSharedScreenshot: Bool {
        status == "running" && !(shelleyURL?.isEmpty ?? true)
    }
}

nonisolated enum SharedVMStore {
    static func loadVMs() -> [SharedVMSummary] {
        guard let data = SharedAppConfiguration.sharedDefaults.data(
            forKey: SharedAppConfiguration.cachedVMsDefaultsKey
        ) else {
            return []
        }
        return (try? JSONDecoder().decode([SharedVMSummary].self, from: data)) ?? []
    }

    static func saveVMs(_ vms: [SharedVMSummary]) {
        guard let data = try? JSONEncoder().encode(vms) else { return }
        SharedAppConfiguration.sharedDefaults.set(
            data,
            forKey: SharedAppConfiguration.cachedVMsDefaultsKey
        )
    }

    static func loadLastSharedVMName() -> String? {
        SharedAppConfiguration.sharedDefaults.string(
            forKey: SharedAppConfiguration.lastSharedVMNameDefaultsKey
        )
    }

    static func saveLastSharedVMName(_ vmName: String) {
        SharedAppConfiguration.sharedDefaults.set(
            vmName,
            forKey: SharedAppConfiguration.lastSharedVMNameDefaultsKey
        )
    }
}
