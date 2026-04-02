protocol VMNamedSnapshot {
    var vmName: String { get }
}

enum VMDetailSnapshotResolver {
    static func resolveCurrent<Snapshot: VMNamedSnapshot>(
        current: Snapshot,
        refreshed: Snapshot?
    ) -> Snapshot {
        guard let refreshed, refreshed.vmName == current.vmName else {
            return current
        }
        return refreshed
    }
}
