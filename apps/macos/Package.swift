// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "ScribaBar",
    platforms: [
        .macOS(.v14),
    ],
    targets: [
        .executableTarget(
            name: "ScribaBar",
            path: "Sources/ScribaBar",
            swiftSettings: [
                .enableUpcomingFeature("StrictConcurrency"),
            ]),
        .testTarget(
            name: "ScribaBarTests",
            dependencies: ["ScribaBar"],
            path: "Tests/ScribaBarTests",
            swiftSettings: [
                .enableUpcomingFeature("StrictConcurrency"),
            ]),
    ])
