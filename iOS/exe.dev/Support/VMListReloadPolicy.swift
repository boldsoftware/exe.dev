enum VMListReloadPolicy {
    static func shouldReload(for notificationKind: String?) -> Bool {
        notificationKind == "vms"
    }

    static func detailIdentity(vmName: String, isCreating: Bool) -> String {
        "\(vmName)-\(isCreating ? "creating" : "ready")"
    }
}
