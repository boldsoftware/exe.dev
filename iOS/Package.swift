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
    ],
    targets: [
        .target(
            name: "ConversationDeltaReducerSupport",
            path: "exe.dev/ViewModels",
            exclude: ["ChannelViewModel.swift"],
            sources: ["ConversationDeltaReducer.swift"]
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
                "TerminalRedrawSupport",
            ],
            path: "exe.devTests"
        ),
    ]
)
