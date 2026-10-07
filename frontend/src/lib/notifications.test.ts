import { afterEach, describe, expect, it, vi } from "vite-plus/test";
import { deliverNotification } from "./notifications.js";
import type { DesktopNotification } from "./api/client.js";

const notification: DesktopNotification = {
  kind: "turn_end",
  session_id: "session",
  project: "demo",
  agent: "claude",
  display_name: "Fix login",
  excerpt: "Ready for review",
};

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("native notifications", () => {
  it.each([
    { visibility: "visible", focused: true, viewing: "session", sent: false },
    { visibility: "hidden", focused: false, viewing: "session", sent: true },
    { visibility: "visible", focused: false, viewing: "session", sent: true },
    { visibility: "visible", focused: true, viewing: "other", sent: true },
  ])(
    "delivers with $visibility visibility and focus $focused while viewing $viewing",
    async ({ visibility, focused, viewing, sent }) => {
      const plugin = {
        isPermissionGranted: vi.fn().mockResolvedValue(true),
        requestPermission: vi.fn(),
        sendNotification: vi.fn(),
      };
      vi.stubGlobal("__TAURI__", { notification: plugin });
      vi.spyOn(document, "visibilityState", "get").mockReturnValue(
        visibility as DocumentVisibilityState,
      );
      vi.spyOn(document, "hasFocus").mockReturnValue(focused);
      await deliverNotification(notification, viewing);
      expect(plugin.sendNotification).toHaveBeenCalledTimes(sent ? 1 : 0);
      if (sent)
        expect(plugin.sendNotification).toHaveBeenCalledWith({
          title: "Fix login: reply finished",
          body: "Ready for review",
        });
    },
  );

  it.each(["granted", "denied"])("requests permission and handles %s", async (permission) => {
    const plugin = {
      isPermissionGranted: vi.fn().mockResolvedValue(false),
      requestPermission: vi.fn().mockResolvedValue(permission),
      sendNotification: vi.fn(),
    };
    vi.stubGlobal("__TAURI__", { notification: plugin });
    await deliverNotification({ ...notification, kind: "new_reply" }, null);
    expect(plugin.requestPermission).toHaveBeenCalledOnce();
    expect(plugin.sendNotification).toHaveBeenCalledTimes(permission === "granted" ? 1 : 0);
    if (permission === "granted")
      expect(plugin.sendNotification).toHaveBeenCalledWith({
        title: "Fix login: new reply",
        body: "Ready for review",
      });
  });

  it("does nothing without a desktop bridge", async () => {
    vi.stubGlobal("__TAURI__", undefined);
    await expect(deliverNotification(notification, null)).resolves.toBeUndefined();
  });
});
