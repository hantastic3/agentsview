import type { DesktopNotification } from "./api/client.js";
import { m } from "./i18n/index.js";

type NotificationBridge = {
  isPermissionGranted: () => Promise<boolean>;
  requestPermission: () => Promise<string>;
  sendNotification: (options: { title: string; body: string }) => void;
};

export async function requestNotificationPermission(): Promise<boolean> {
  const plugin = (window as Window & { __TAURI__?: { notification?: NotificationBridge } })
    .__TAURI__?.notification;
  if (!plugin) return false;
  try {
    return (await plugin.isPermissionGranted()) || (await plugin.requestPermission()) === "granted";
  } catch (err) {
    console.warn("native notification permission failed", err);
    return false;
  }
}

export async function deliverNotification(
  n: DesktopNotification,
  viewingId: string | null,
): Promise<void> {
  if (viewingId === n.session_id && document.visibilityState === "visible" && document.hasFocus())
    return;
  const plugin = (window as Window & { __TAURI__?: { notification?: NotificationBridge } })
    .__TAURI__?.notification;
  if (!plugin) return;
  try {
    if (!(await plugin.isPermissionGranted())) return;
    const name = n.display_name || n.project || n.agent;
    plugin.sendNotification({
      title:
        n.kind === "turn_end"
          ? m.notification_turn_end_title_suffix({ name })
          : m.notification_new_reply_title_suffix({ name }),
      body: n.excerpt || (n.kind === "turn_end" ? m.notification_turn_end_body() : ""),
    });
  } catch (err) {
    console.warn("native notification failed", err);
  }
}
