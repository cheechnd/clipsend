import SwiftUI
import UIKit
import UserNotifications

@MainActor
final class AppDelegate: NSObject, UIApplicationDelegate, UNUserNotificationCenterDelegate {
    func application(
        _ application: UIApplication,
        didFinishLaunchingWithOptions launchOptions: [UIApplication.LaunchOptionsKey: Any]? = nil
    ) -> Bool {
        UNUserNotificationCenter.current().delegate = self
        CompanionModel.shared.launched()
        return true
    }

    func application(_ application: UIApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
        CompanionModel.shared.registered(token: deviceToken)
    }

    func application(_ application: UIApplication, didFailToRegisterForRemoteNotificationsWithError error: any Error) {
        // Deliberately do not log the error or token payload.
        CompanionModel.shared.registrationFailed()
    }

    // UIKit's notification completion can update snapshots/state restoration.
    // Keep the completion on MainActor explicitly rather than using the async
    // delegate bridge, which may invoke it on a cooperative worker thread.
    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping @Sendable (UNNotificationPresentationOptions) -> Void
    ) {
        let metadata = PushMetadata.parse(notification.request.content.userInfo)
        Task { @MainActor in
            if let metadata { CompanionModel.shared.notificationReceived(metadata) }
            completionHandler([])
        }
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping @Sendable () -> Void
    ) {
        let shouldReceive = response.actionIdentifier == UNNotificationDefaultActionIdentifier &&
            response.notification.request.trigger is UNPushNotificationTrigger
        let metadata = shouldReceive ? PushMetadata.parse(response.notification.request.content.userInfo) : nil
        Task { @MainActor in
            if let metadata { CompanionModel.shared.notificationReceived(metadata) }
            completionHandler()
        }
    }

}

@main
struct ClipSendApp: App {
    @UIApplicationDelegateAdaptor(AppDelegate.self) private var delegate
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var model = CompanionModel.shared

    var body: some Scene {
        WindowGroup {
            ContentView(model: model)
                .onChange(of: scenePhase, initial: true) { _, phase in
                    model.sceneChanged(active: phase == .active)
                }
        }
    }
}
