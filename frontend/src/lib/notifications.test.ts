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
    id: 1,
    ordinal: 2,
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
  messages.mockImplementation(async () => ({ messages: [reply({ ordinal: row.message_count - 1 })], count: 1 }));
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
      each_row: true,
      include_one_shot: true,
    });
    expect(list.mock.calls[0]![0]?.include_children).toBeUndefined();
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
    messages.mockResolvedValue({ messages: [reply({ ordinal: 3, is_system: true })], count: 1 });
    await change({ message_count: 4 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it("sends only the waiting toast with both toggles on and consumes the reply", async () => {
    await start(true);
    await change({ termination_status: "awaiting_user", message_count: 3 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
    expect(plugin.sendNotification.mock.calls[0]![0].body).toBe("The agent finished this turn and is waiting for you.");
    messages.mockResolvedValue({ messages: [reply({ ordinal: 3, is_system: true })], count: 1 });
    await change({ message_count: 4 });
    expect(messages).toHaveBeenCalledTimes(2);
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it("toasts a newly discovered waiting session", async () => {
    await start();
    await change({ id: "new", termination_status: "awaiting_user", ended_at: "2026-10-07T12:00:01Z" });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it.each([
    { relationship_type: "subagent" },
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
  it("toasts the newest new assistant prose once per refresh", async () => {
    await start(true);
    await change({ message_count: 3 });
    expect(messages).toHaveBeenCalledWith({ id: "session" }, { direction: "desc", limit: 1 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
    expect(plugin.sendNotification.mock.calls[0]![0].body).toBe("Ready for review");
    await change();
    messages.mockResolvedValue({ messages: [reply({ ordinal: 3, content: "Another reply" })], count: 1 });
    await change({ message_count: 4 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(2);
  });
  it.each([{ has_tool_use: true, content: "[Bash] ls" }, { role: "user" }, { content: "  " }])(
    "ignores a latest message without assistant prose: %j",
    async (patch) => {
      messages.mockResolvedValue({ messages: [reply(patch)], count: 1 });
      await start(true);
      await change({ message_count: 3 });
      expect(plugin.sendNotification).not.toHaveBeenCalled();
    },
  );
  it("skips system rows when finding the newest reply", async () => {
    messages.mockResolvedValue({ messages: [reply({ ordinal: 3, is_system: true }), reply()], count: 2 });
    await start(true);
    await change({ message_count: 4 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it.each([{ has_tool_use: true, content: "[Bash] ls" }, { role: "user" }])(
    "finds assistant prose before a later message: %j",
    async (patch) => {
      messages.mockResolvedValue({ messages: [
        reply({ ordinal: 4, ...patch, timestamp: "2026-10-07T12:00:03Z" }),
        reply({ ordinal: 3, content: "Latest text", timestamp: "2026-10-07T12:00:02Z" }),
        reply(),
      ], count: 3 });
      await start(true);
      await change({ message_count: 5 });
      expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
      expect(plugin.sendNotification.mock.calls[0]![0].body).toBe("Latest text");
    },
  );
  it("retries a failed reply read without consuming the count", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    await start(true);
    messages.mockRejectedValueOnce(new Error("unavailable"));
    await change({ message_count: 3 });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    await change();
    expect(messages).toHaveBeenLastCalledWith({ id: "session" }, { direction: "desc", limit: 1 });
    expect(messages).toHaveBeenCalledTimes(2);
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
    expect(plugin.sendNotification.mock.calls[0]![0].body).toBe("Ready for review");
    await change();
    expect(messages).toHaveBeenCalledTimes(2);
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
  });
  it("keeps restored rows silent and reads the first reply of a newly seen session", async () => {
    await start(true);
    await change({ id: "restored", termination_status: "awaiting_user", ended_at: "2026-10-07T11:59:00Z" });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    expect(messages).not.toHaveBeenCalled();
    messages.mockResolvedValue({ messages: [reply({ ordinal: 1 }), reply({ ordinal: 0, role: "user" })], count: 2 });
    await change({ id: "new-running", termination_status: "", message_count: 2, ended_at: "2026-10-07T12:00:01Z" });
    expect(messages).toHaveBeenCalledWith({ id: "new-running" }, { direction: "desc", limit: 2 });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it("toasts a second turn end after a system-only append", async () => {
    row.termination_status = "awaiting_user";
    await start();
    messages.mockResolvedValueOnce({ messages: [reply({ is_system: true })], count: 1 });
    await change({ message_count: 3 });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    await change({ message_count: 4 });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it("toasts prose beside a tool call", async () => {
    await start(true);
    messages.mockResolvedValue({ messages: [reply({ has_tool_use: true, content: "Ready for review\n\n[Bash] ls" })], count: 1 });
    await change({ message_count: 3 });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it("toasts once when a reply appends between the list and message read", async () => {
    await start(true);
    messages.mockResolvedValue({ messages: [reply({ ordinal: 3, content: "Appended reply" })], count: 1 });
    await change({ message_count: 3 });
    expect(plugin.sendNotification.mock.calls[0]![0].body).toBe("Appended reply");
    await change({ message_count: 4 });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it("remembers a waiting session that re-enters after ten idle minutes", async () => {
    await start();
    await change({ termination_status: "awaiting_user" });
    list.mockResolvedValueOnce({ sessions: [], total: 0 });
    vi.setSystemTime(new Date("2026-10-07T12:11:00Z"));
    await change();
    await change({ ended_at: "2026-10-07T12:11:01Z" });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it("recovers missed events within five minutes and clears the poll on stop", async () => {
    await start();
    row = { ...row, termination_status: "awaiting_user" };
    await vi.advanceTimersByTimeAsync(5 * 60_000);
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
    expect(list).toHaveBeenCalledTimes(2);
    stop!();
    await vi.advanceTimersByTimeAsync(5 * 60_000);
    expect(list).toHaveBeenCalledTimes(2);
  });
  it("reads a burst beyond fifty rows", async () => {
    await start(true);
    const tools = Array.from({ length: 97 }, (_, index) => reply({ id: index + 10, ordinal: 99 - index, has_tool_use: true, content: "[Bash] ls" }));
    messages.mockResolvedValue({ messages: [...tools, reply()], count: 98 });
    await change({ message_count: 100 });
    expect(messages).toHaveBeenCalledWith({ id: "session" }, { direction: "desc", limit: 98 });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
    expect(plugin.sendNotification.mock.calls[0]![0].body).toBe("Ready for review");
  });
  it("pages through a capped read to find prose behind a tool burst", async () => {
    await start(true);
    const tools = Array.from({ length: 1000 }, (_, index) => reply({ id: index + 10, ordinal: 1002 - index, has_tool_use: true, content: "[Bash] ls" }));
    messages.mockResolvedValueOnce({ messages: tools, count: 1000 });
    messages.mockResolvedValueOnce({ messages: [reply()], count: 1 });
    await change({ message_count: 1003 });
    expect(messages).toHaveBeenNthCalledWith(1, { id: "session" }, { direction: "desc", limit: 1001 });
    expect(messages).toHaveBeenNthCalledWith(2, { id: "session" }, { direction: "desc", limit: 1, from: 2 });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
    expect(plugin.sendNotification.mock.calls[0]![0].body).toBe("Ready for review");
  });
  it("retries a failed waiting read and toasts once", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    await start();
    messages.mockRejectedValueOnce(new Error("unavailable"));
    await change({ message_count: 3, termination_status: "awaiting_user" });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    await change();
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
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
