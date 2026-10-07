import { cleanup, fireEvent, render } from "@testing-library/svelte";
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
afterEach(cleanup);

it("saves each notification toggle and renders the saved value", async () => {
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
  const enabled = getByRole("switch", { name: "Enable desktop notifications" });
  const replies = getByRole("switch", { name: "Also notify on new replies" });
  expect(replies.hasAttribute("disabled")).toBe(true);
  await fireEvent.click(enabled);
  expect(save).toHaveBeenCalledWith({ notifications: { enabled: true, notify_new_reply: false } });
  expect((enabled as HTMLInputElement).checked).toBe(true);
  save.mockResolvedValue({ ...response, notifications: { enabled: true, notify_new_reply: true } });
  await fireEvent.click(replies);
  expect(save).toHaveBeenLastCalledWith({
    notifications: { enabled: true, notify_new_reply: true },
  });
  expect((replies as HTMLInputElement).checked).toBe(true);
});
