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
        .testTarget(
            name: "ConversationDeltaReducerSupportTests",
            dependencies: [
                "ConversationDeltaReducerSupport",
                "SyncNotificationSupport",
                "TerminalRedrawSupport",
            ],
            path: "exe.devTests"
        ),
    ]
)
