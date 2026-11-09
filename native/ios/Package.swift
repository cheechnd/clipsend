// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "ClipSend",
    platforms: [.iOS(.v17)],
    products: [
        .library(name: "ClipSend", targets: ["ClipSend"]),
        .library(name: "ClipSendShare", targets: ["ClipSendShare"]),
    ],
    targets: [
        .target(name: "ClipSendCore"),
        .target(name: "ClipSend", dependencies: ["ClipSendCore"]),
        .target(name: "ClipSendShare", dependencies: ["ClipSendCore"], linkerSettings: [
            // Retain the Objective-C principal class and a real __text section:
            // xtool's signer cannot sign an extension containing only stubs.
            .unsafeFlags(["-Xlinker", "-all_load", "-Xlinker", "-headerpad", "-Xlinker", "0x1000"]),
        ]),
    ]
)
