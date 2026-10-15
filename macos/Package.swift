// swift-tools-version:5.10
import PackageDescription

let package = Package(
    name: "Dial",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(
            name: "Dial",
            path: "Sources/Dial",
            swiftSettings: [.unsafeFlags(["-Osize"])],
            linkerSettings: [.linkedFramework("Carbon")]
        ),
        // One tiny app per account in the Dock: focuses (or opens) that
        // account's desktop window, then quits.
        .executableTarget(
            name: "DialLauncher",
            path: "Sources/DialLauncher",
            swiftSettings: [.unsafeFlags(["-Osize"])]
        ),
    ]
)
