// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "ClipSend",
    platforms: [.iOS(.v17)],
    products: [.library(name: "ClipSend", targets: ["ClipSend"])],
    targets: [.target(name: "ClipSend")]
)
