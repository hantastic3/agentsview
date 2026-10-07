import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { SessionsService, type DbSession, type DbMessage } from "./api/generated/index.js";
import { startNotificationWatcher, requestNotificationPermission } from "./notifications.js";

const mocks = vi.hoisted(() => ({ callback: () => {}, unsubscribe: vi.fn() }));
vi.mock("./stores/events.svelte.js", () => ({
  events: {
    subscribeDebounced: vi.fn((callback) => {
      mocks.callback = callback;
      return mocks.unsubscribe;
    }),
  },
}));
vi.mock("./api/generated/index.js", () => ({
  SessionsService: {
    getApiV1Sessions: vi.fn(),
    getApiV1SessionsByIdMessages: vi.fn(),
  },
}));
const plugin = {
  isPermissionGranted: vi.fn(),
  requestPermission: vi.fn(),
  sendNotification: vi.fn(),
};
let stop: (() => void) | undefined;
let row: DbSession;
const list = vi.mocked(SessionsService.getApiV1Sessions);
const messages = vi.mocked(SessionsService.getApiV1SessionsByIdMessages);
async function flush() {
  for (let i = 0; i < 15; i++) await Promise.resolve();
}
async function start(replies = false, viewing: string | null = null) {
  stop = startNotificationWatcher(
    () => replies,
    () => viewing,
  );
  await flush();
  expect(list).toHaveBeenCalled();
}
async function change(patch: Partial<DbSession> = {}) {
  row = { ...row, ...patch };
  mocks.callback();
  await flush();
}
function reply(patch: Partial<DbMessage> = {}) {
  return {
    role: "assistant",
    content: "Ready for review",
    timestamp: "2026-10-07T12:00:01Z",
    is_system: false,
    has_tool_use: false,
    ...patch,
  } as DbMessage;
}
beforeEach(() => {
  vi.resetAllMocks();
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-07T12:00:00Z"));
  vi.stubGlobal("__TAURI__", { notification: plugin });
  vi.spyOn(document, "hasFocus").mockReturnValue(false);
  plugin.isPermissionGranted.mockResolvedValue(true);
  row = {
    id: "session",
    agent: "claude",
    project: "demo",
    display_name: "Fix login",
    ended_at: "2026-10-07T12:00:00Z",
    message_count: 2,
    user_message_count: 1,
  } as DbSession;
  list.mockImplementation(async () => ({ sessions: [row], total: 1 }));
  messages.mockResolvedValue({ messages: [reply()], count: 1 });
});
afterEach(() => {
  stop?.();
  stop = undefined;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
describe("desktop notification watcher", () => {
  it("silently baselines waiting sessions and ignores repeats and progress", async () => {
    row.termination_status = "awaiting_user";
    await start(true);
    await change();
    await change({ ended_at: "2026-10-07T12:00:02Z" });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    expect(messages).not.toHaveBeenCalled();
    expect(list.mock.calls[0]![0]).toMatchObject({
      active_since: "2026-10-07T11:50:00.000Z",
      include_children: true,
      include_one_shot: true,
    });
  });
  it("toasts a waiting transition once and a later user turn", async () => {
    await start();
    await change({ termination_status: "awaiting_user" });
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
    expect(plugin.sendNotification.mock.calls[0]![0].title).toContain("Fix login");
    await change({ message_count: 4, user_message_count: 2 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(2);
  });
  it("keeps system-only appends silent while waiting", async () => {
    await start();
    await change({ termination_status: "awaiting_user", message_count: 3 });
    await change({ message_count: 4 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it("sends only the waiting toast with both toggles on and consumes the reply", async () => {
    await start(true);
    await change({ termination_status: "awaiting_user", message_count: 3 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
    expect(plugin.sendNotification.mock.calls[0]![0].body).toBe("The agent finished this turn and is waiting for you.");
    await change({ message_count: 4 });
    expect(messages).toHaveBeenCalledTimes(2);
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it("toasts a newly discovered waiting session", async () => {
    await start();
    await change({ id: "new", termination_status: "awaiting_user" });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it.each([
    { relationship_type: "subagent" },
    { ended_at: "2026-10-07T11:49:59Z" },
  ])("keeps excluded sessions silent: %j", async (patch) => {
    await start(true);
    await change({ ...patch, termination_status: "awaiting_user", message_count: 4 });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
  });
  it.each([
    { visible: "visible", focused: true, sent: false },
    { visible: "hidden", focused: true, sent: true },
    { visible: "visible", focused: false, sent: true },
  ])("respects window visibility and focus: %j", async ({ visible, focused, sent }) => {
    vi.spyOn(document, "visibilityState", "get").mockReturnValue(
      visible as DocumentVisibilityState,
    );
    vi.spyOn(document, "hasFocus").mockReturnValue(focused);
    await start(false, "session");
    await change({ termination_status: "awaiting_user" });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(sent ? 1 : 0);
  });
  it("retries a failed fetch with the full freshness window", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    await start();
    vi.setSystemTime(new Date("2026-10-07T12:00:05Z"));
    list.mockRejectedValueOnce(new Error("unavailable"));
    await change({ termination_status: "awaiting_user" });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    vi.setSystemTime(new Date("2026-10-07T12:00:10Z"));
    await change();
    expect(list.mock.calls[2]![0]?.active_since).toBe("2026-10-07T11:50:10.000Z");
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it("finds a reply timestamped before a successful fetch but committed later", async () => {
    list.mockImplementation(async (params) => ({
      sessions: Date.parse(row.ended_at!) >= Date.parse(params!.active_since!) ? [row] : [],
      total: 1,
    }));
    await start(true);
    vi.setSystemTime(new Date("2026-10-07T12:00:05Z"));
    await change();
    vi.setSystemTime(new Date("2026-10-07T12:00:10Z"));
    await change({ message_count: 3, ended_at: "2026-10-07T12:00:01Z" });
    expect(list.mock.calls[2]![0]?.active_since).toBe("2026-10-07T11:50:10.000Z");
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
    expect(plugin.sendNotification.mock.calls[0]![0].body).toBe("Ready for review");
  });
  it("reads all pages before completing the silent baseline", async () => {
    list.mockResolvedValueOnce({ sessions: [], total: 1, next_cursor: "page2" });
    row.termination_status = "awaiting_user";
    await start();
    expect(list.mock.calls[1]![0]?.cursor).toBe("page2");
    await change();
    expect(plugin.sendNotification).not.toHaveBeenCalled();
  });
  it("toasts new assistant prose once and ignores a same-timestamp rewrite", async () => {
    await start(true);
    await change({ message_count: 3 });
    expect(messages).toHaveBeenCalledWith({ id: "session" }, { direction: "desc", limit: 20 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
    expect(plugin.sendNotification.mock.calls[0]![0].body).toBe("Ready for review");
    await change();
    messages.mockResolvedValue({ messages: [reply({ content: "Rewritten reply" })], count: 1 });
    await change({ message_count: 4 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it.each([{ has_tool_use: true }, { role: "user" }, { content: "  " }])(
    "ignores a latest message without assistant prose: %j",
    async (patch) => {
      messages.mockResolvedValue({ messages: [reply(patch)], count: 1 });
      await start(true);
      await change({ message_count: 3 });
      expect(plugin.sendNotification).not.toHaveBeenCalled();
    },
  );
  it("skips system rows when finding the newest reply", async () => {
    messages.mockResolvedValue({ messages: [reply({ is_system: true }), reply()], count: 2 });
    await start(true);
    await change({ message_count: 3 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it.each([{ has_tool_use: true }, { role: "user" }])(
    "finds assistant prose before a later message: %j",
    async (patch) => {
      messages.mockResolvedValue({ messages: [
        reply({ ...patch, timestamp: "2026-10-07T12:00:03Z" }),
        reply({ content: "Latest text", timestamp: "2026-10-07T12:00:02Z" }),
        reply(),
      ], count: 3 });
      await start(true);
      await change({ message_count: 5 });
      expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
      expect(plugin.sendNotification.mock.calls[0]![0].body).toBe("Latest text");
      messages.mockResolvedValue({ messages: [reply({ content: "Older text", timestamp: "2026-10-07T12:00:02Z" })], count: 1 });
      await change({ message_count: 6 });
      expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
    },
  );
  it("retries a failed reply read without consuming the count or timestamp", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    await start(true);
    messages.mockRejectedValueOnce(new Error("unavailable"));
    await change({ message_count: 3 });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    await change();
    expect(messages).toHaveBeenCalledTimes(2);
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
    expect(plugin.sendNotification.mock.calls[0]![0].body).toBe("Ready for review");
    await change();
    expect(messages).toHaveBeenCalledTimes(2);
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it("delivers the waiting toast even when the reply read fails", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    messages.mockRejectedValue(new Error("unavailable"));
    await start(true);
    await change({ termination_status: "awaiting_user", message_count: 4 });
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it("does no reads or delivery when permission is denied", async () => {
    plugin.isPermissionGranted.mockResolvedValue(false);
    stop = startNotificationWatcher(
      () => true,
      () => null,
    );
    await flush();
    await change({ termination_status: "awaiting_user", message_count: 4 });
    expect(list).not.toHaveBeenCalled();
    expect(plugin.sendNotification).not.toHaveBeenCalled();
  });
  it("stops delivery during a pending fetch and unsubscribes", async () => {
    await start();
    let resolve!: (value: Awaited<ReturnType<typeof SessionsService.getApiV1Sessions>>) => void;
    list.mockReturnValueOnce(
      new Promise((done) => {
        resolve = done;
      }),
    );
    await change({ termination_status: "awaiting_user" });
    stop!();
    resolve({ sessions: [row], total: 1 });
    await flush();
    expect(mocks.unsubscribe).toHaveBeenCalledOnce();
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    stop = undefined;
  });
});
it("requests permission only when needed and returns denial", async () => {
  expect(await requestNotificationPermission()).toBe(true);
  expect(plugin.requestPermission).not.toHaveBeenCalled();
  plugin.isPermissionGranted.mockResolvedValue(false);
  plugin.requestPermission.mockResolvedValue("denied");
  expect(await requestNotificationPermission()).toBe(false);
  expect(plugin.requestPermission).toHaveBeenCalledOnce();
});
