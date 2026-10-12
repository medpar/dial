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
        )
    ]
)
