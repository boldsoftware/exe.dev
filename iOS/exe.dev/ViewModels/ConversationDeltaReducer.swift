import Foundation

enum ConversationDeltaReducer {
    static let incrementalFetchThreshold = 24

    static func shouldReload(hasSnapshot: Bool, changedMessageCount: Int) -> Bool {
        !hasSnapshot || changedMessageCount > incrementalFetchThreshold
    }

    static func merge<T, ID: Hashable>(
        current: [T],
        changed: [T],
        id: (T) -> ID,
        areInIncreasingOrder: (T, T) -> Bool
    ) -> [T] {
        guard !changed.isEmpty else { return current }

        var merged = current
        var indexByID: [ID: Int] = [:]
        indexByID.reserveCapacity(current.count + changed.count)

        for (index, message) in merged.enumerated() {
            indexByID[id(message)] = index
        }

        var needsSort = false

        for message in changed {
            let messageID = id(message)
            if let existingIndex = indexByID[messageID] {
                merged[existingIndex] = message
            } else {
                indexByID[messageID] = merged.count
                merged.append(message)
                needsSort = true
            }
        }

        if needsSort || !isSorted(merged, by: areInIncreasingOrder) {
            merged.sort(by: areInIncreasingOrder)
        }

        return merged
    }

    private static func isSorted<T>(_ messages: [T], by areInIncreasingOrder: (T, T) -> Bool) -> Bool {
        guard messages.count > 1 else { return true }

        for index in 1..<messages.count {
            if areInIncreasingOrder(messages[index], messages[index - 1]) {
                return false
            }
        }

        return true
    }
}
