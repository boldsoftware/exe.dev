import Testing
@testable import VMListSupport

private struct TestVM: VMListGroupable {
    let vmListName: String
    let vmListTags: [String]
}

@Test func taggedSectionsSortAlphabetically() {
    let sections = VMListGrouping.sections(for: [
        TestVM(vmListName: "zeta-runner", vmListTags: ["zeta"]),
        TestVM(vmListName: "bravo-build", vmListTags: ["beta"]),
        TestVM(vmListName: "alpha-build", vmListTags: ["beta"]),
        TestVM(vmListName: "alpha-runner", vmListTags: ["alpha"]),
    ])

    #expect(sections.map(\.title) == ["alpha", "beta", "zeta"] as [String?])
    #expect(sections[1].items.map(\.vmListName) == ["alpha-build", "bravo-build"])
}

@Test func untaggedSectionComesLastWhenTaggedVMsExist() {
    let sections = VMListGrouping.sections(for: [
        TestVM(vmListName: "plain-b", vmListTags: []),
        TestVM(vmListName: "tagged-a", vmListTags: ["dev"]),
        TestVM(vmListName: "plain-a", vmListTags: []),
    ])

    #expect(sections.map(\.title) == ["dev", "Untagged"] as [String?])
    #expect(sections[1].items.map(\.vmListName) == ["plain-a", "plain-b"])
}

@Test func allUntaggedVMsUseHeaderlessSection() {
    let sections = VMListGrouping.sections(for: [
        TestVM(vmListName: "charlie", vmListTags: []),
        TestVM(vmListName: "alpha", vmListTags: []),
    ])

    #expect(sections.count == 1)
    #expect(sections[0].title == nil)
    #expect(sections[0].items.map(\.vmListName) == ["alpha", "charlie"])
}

@Test func firstTagIsUsedAsPrimaryGroup() {
    let sections = VMListGrouping.sections(for: [
        TestVM(vmListName: "double-tagged", vmListTags: ["beta", "alpha"]),
    ])

    #expect(sections.count == 1)
    #expect(sections[0].title == "beta")
}
