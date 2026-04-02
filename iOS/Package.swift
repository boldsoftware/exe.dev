// swift-tools-version: 6.0

import PackageDescription

let package = Package(
    name: "exeIOSSupport",
    platforms: [
        .macOS(.v14),
    ],
    products: [
        .library(
            name: "ConversationDeltaReducerSupport",
            targets: ["ConversationDeltaReducerSupport"]
        ),
        .library(
            name: "TerminalRedrawSupport",
            targets: ["TerminalRedrawSupport"]
        ),
        .library(
            name: "SyncNotificationSupport",
            targets: ["SyncNotificationSupport"]
        ),
        .library(
            name: "VMListSupport",
            targets: ["VMListSupport"]
        ),
    ],
    targets: [
        .target(
            name: "ConversationDeltaReducerSupport",
            path: "exe.dev/ViewModels",
            exclude: ["ChannelViewModel.swift"],
            sources: [
                "ConversationDeltaReducer.swift",
                "ConversationRefreshPolicy.swift",
            ]
        ),
        .target(
            name: "SyncNotificationSupport",
            path: "exe.dev/Database",
            exclude: ["SyncEngine.swift"],
            sources: ["SyncEngineSaveNotificationDispatcher.swift"]
        ),
        .target(
            name: "TerminalRedrawSupport",
            path: "exe.dev/TerminalSupport",
            sources: ["TerminalRedrawPlanner.swift"]
        ),
        .target(
            name: "VMListSupport",
            path: "exe.dev/Support",
            sources: ["VMListGrouping.swift"]
        ),
        .testTarget(
            name: "ConversationDeltaReducerSupportTests",
            dependencies: [
                "ConversationDeltaReducerSupport",
                "SyncNotificationSupport",
                "TerminalRedrawSupport",
                "VMListSupport",
            ],
            path: "exe.devTests"
        ),
    ]
)
