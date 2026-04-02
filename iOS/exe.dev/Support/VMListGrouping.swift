import Foundation

protocol VMListGroupable {
    var vmListName: String { get }
    var vmListTags: [String] { get }
}

struct VMListSection<Item: VMListGroupable> {
    let title: String?
    let items: [Item]
}

enum VMListGrouping {
    static func sections<Item: VMListGroupable>(for items: [Item]) -> [VMListSection<Item>] {
        let sortStrings = { (lhs: String, rhs: String) in
            lhs.localizedCaseInsensitiveCompare(rhs) == .orderedAscending
        }

        let sortedItems = items.sorted { lhs, rhs in
            sortStrings(lhs.vmListName, rhs.vmListName)
        }

        var taggedGroups: [String: [Item]] = [:]
        var untagged: [Item] = []

        for item in sortedItems {
            if let primaryTag = item.vmListTags.first, !primaryTag.isEmpty {
                taggedGroups[primaryTag, default: []].append(item)
            } else {
                untagged.append(item)
            }
        }

        var sections = taggedGroups.keys
            .sorted(by: sortStrings)
            .map { title in
                VMListSection(title: title, items: taggedGroups[title] ?? [])
            }

        guard !untagged.isEmpty else { return sections }
        guard !sections.isEmpty else { return [VMListSection(title: nil, items: untagged)] }

        sections.append(VMListSection(title: "Untagged", items: untagged))
        return sections
    }
}
