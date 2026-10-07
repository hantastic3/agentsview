import { cleanup, fireEvent, render, waitFor } from "@testing-library/svelte";
import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";
import NotificationsSettings from "./NotificationsSettings.svelte";
import { SettingsService, type SettingsResponse } from "../../api/generated/index";
import { settings } from "../../stores/settings.svelte.js";

vi.mock("../../api/generated/index", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../api/generated/index")>()),
  SettingsService: { putApiV1Settings: vi.fn() },
}));

beforeEach(() => {
  vi.clearAllMocks();
  settings.notifications = { enabled: false, notify_new_reply: false };
  settings.readOnly = false;
  settings.saving = false;
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

it("saves each notification toggle and renders the saved value", async () => {
  const plugin = {
    isPermissionGranted: vi.fn().mockResolvedValue(false),
    requestPermission: vi.fn().mockResolvedValue("granted"),
    sendNotification: vi.fn(),
  };
  vi.stubGlobal("__TAURI__", { notification: plugin });
  const save = vi.mocked(SettingsService.putApiV1Settings);
  const response: Omit<SettingsResponse, "notifications"> = {
    agent_dirs: {},
    chart_palette: "agentsview",
    github_configured: false,
    host: "127.0.0.1",
    port: 8080,
    read_only: false,
    require_auth: false,
    terminal: { mode: "auto" },
    disabled_agents: [],
    session_providers: [],
    tool_result_images: "keep",
    insight_default_agent: "claude",
  };
  save.mockResolvedValue({
    ...response,
    notifications: { enabled: true, notify_new_reply: false },
  });
  const { getByRole } = render(NotificationsSettings);
  const enabled = () => getByRole("switch", { name: "Enable desktop notifications" });
  const replies = getByRole("switch", { name: "Also notify on new replies" });
  expect(replies.hasAttribute("disabled")).toBe(true);
  expect(plugin.requestPermission).not.toHaveBeenCalled();
  await fireEvent.click(enabled());
  await waitFor(() =>
    expect(save).toHaveBeenCalledWith({
      notifications: { enabled: true, notify_new_reply: false },
    }),
  );
  expect(plugin.requestPermission).toHaveBeenCalledOnce();
  expect((enabled() as HTMLInputElement).checked).toBe(true);
  save.mockResolvedValue({ ...response, notifications: { enabled: true, notify_new_reply: true } });
  await fireEvent.click(replies);
  expect(save).toHaveBeenLastCalledWith({
    notifications: { enabled: true, notify_new_reply: true },
  });
  expect((replies as HTMLInputElement).checked).toBe(true);
  save.mockResolvedValue({
    ...response,
    notifications: { enabled: false, notify_new_reply: true },
  });
  await fireEvent.click(enabled());
  expect(save).toHaveBeenLastCalledWith({
    notifications: { enabled: false, notify_new_reply: true },
  });
  expect(plugin.requestPermission).toHaveBeenCalledOnce();
  expect(plugin.sendNotification).not.toHaveBeenCalled();
});

it("keeps notifications off and explains denied permission", async () => {
  vi.stubGlobal("__TAURI__", {
    notification: {
      isPermissionGranted: vi.fn().mockResolvedValue(false),
      requestPermission: vi.fn().mockResolvedValue("denied"),
    },
  });
  const { getByRole } = render(NotificationsSettings);
  const enabled = () => getByRole("switch", { name: "Enable desktop notifications" });
  await fireEvent.click(enabled());
  await waitFor(() =>
    expect(getByRole("status").textContent).toBe(
      "Notification permission was denied. Allow notifications for AgentsView in your system settings.",
    ),
  );
  expect(SettingsService.putApiV1Settings).not.toHaveBeenCalled();
  expect(settings.notifications.enabled).toBe(false);
  expect((enabled() as HTMLInputElement).checked).toBe(false);
});

it("explains denied permission when enabled settings load", async () => {
  settings.notifications.enabled = true;
  vi.stubGlobal("__TAURI__", {
    notification: {
      isPermissionGranted: vi.fn().mockResolvedValue(false),
      requestPermission: vi.fn(),
    },
  });
  const { getByRole } = render(NotificationsSettings);
  await waitFor(() =>
    expect(getByRole("status").textContent).toContain("Notification permission was denied."),
  );
  expect(SettingsService.putApiV1Settings).not.toHaveBeenCalled();
});

it.each(["Enable desktop notifications", "Also notify on new replies"])("reverts %s after a failed save", async (name) => {
  settings.notifications.enabled = name === "Also notify on new replies";
  vi.stubGlobal("__TAURI__", { notification: { isPermissionGranted: vi.fn().mockResolvedValue(true) } });
  vi.mocked(SettingsService.putApiV1Settings).mockRejectedValueOnce(new Error("save failed"));
  const { getByRole } = render(NotificationsSettings);
  const toggle = getByRole("switch", { name }) as HTMLInputElement;
  await fireEvent.click(toggle);
  await waitFor(() => expect(toggle.checked).toBe(false));
  expect(settings.saveError).toBe("save failed");
});

it("explains an unavailable notification bridge", () => {
  const { getByRole } = render(NotificationsSettings);
  expect(getByRole("status").textContent).toBe("Desktop notifications are unavailable in this app.");
  expect(getByRole("switch", { name: "Enable desktop notifications" }).hasAttribute("disabled")).toBe(true);
});

it("shows OS notification settings guidance when desktop permission reports granted", async () => {
  settings.notifications.enabled = true;
  vi.stubGlobal("__TAURI__", { notification: { isPermissionGranted: vi.fn().mockResolvedValue(true) } });
  const { getByText, queryByRole } = render(NotificationsSettings);
  expect(getByText("Toasts follow the OS notification settings for agentsview.")).toBeTruthy();
  expect(queryByRole("status")).toBeNull();
});
