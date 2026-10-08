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
    getApiV1SessionsById: vi.fn(),
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
const session = vi.mocked(SessionsService.getApiV1SessionsById);
async function flush() {
  for (let i = 0; i < 15; i++) await Promise.resolve();
}
async function start(viewing: string | null = null) {
  stop = startNotificationWatcher(() => viewing);
  await flush();
  expect(list).toHaveBeenCalled();
  messages.mockClear();
}
async function change(patch: Partial<DbSession> = {}) {
  row = { ...row, ...patch };
  mocks.callback();
  await flush();
}
function assistantMessage(patch: Partial<DbMessage> = {}) {
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
  session.mockImplementation(
    async () => row as Awaited<ReturnType<typeof SessionsService.getApiV1SessionsById>>,
  );
  messages.mockImplementation(async () => ({
    messages: [assistantMessage({ ordinal: row.message_count - 1 })],
    count: 1,
  }));
});
afterEach(() => {
  stop?.();
  stop = undefined;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
describe("desktop notification watcher", () => {
  it("ignores system-only appends in a usage-only archive after baseline and delivery", async () => {
    row.termination_status = "awaiting_user";
    messages.mockResolvedValue({ messages: [assistantMessage({ ordinal: 1 })], count: 1 });
    await start();
    await change({ message_count: 3 });
    expect(plugin.sendNotification).not.toHaveBeenCalled();

    messages.mockResolvedValue({ messages: [assistantMessage({ ordinal: 3 })], count: 1 });
    await change({ message_count: 4 });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
    await change({ message_count: 5 });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();

    messages.mockResolvedValue({ messages: [assistantMessage({ ordinal: 5 })], count: 1 });
    await change({ message_count: 6 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(2);
  });
  it.each(["2026-10-07T11:59:00Z", "2026-10-07T12:05:00Z"])(
    "silently baselines waiting sessions at %s and ignores repeats and progress",
    async (ended_at) => {
      row.ended_at = ended_at;
      row.termination_status = "awaiting_user";
      await start();
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
    },
  );
  it("toasts a waiting transition once and a later user turn", async () => {
    await start();
    await change({ termination_status: "awaiting_user" });
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledTimes(1);
    expect(plugin.sendNotification.mock.calls[0]![0].title).toContain("Fix login");
    await change({ message_count: 4, user_message_count: 2 });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(2);
    expect(messages).toHaveBeenCalledTimes(2);
    expect(session).toHaveBeenCalledTimes(2);
  });
  it("reads appended rows by position when stored ordinals skip numbers", async () => {
    row.termination_status = "awaiting_user";
    row.message_count = 3;
    const stored = [
      assistantMessage({ ordinal: 0, role: "user" }),
      assistantMessage({ ordinal: 1 }),
      assistantMessage({ ordinal: 3, is_system: true }),
    ];
    messages.mockImplementation(async (_, params) => {
      const page = stored
        .filter((message) => params?.from === undefined || message.ordinal <= params.from)
        .slice()
        .reverse()
        .slice(0, params!.limit!);
      return { messages: page, count: page.length };
    });
    await start();
    stored.push(assistantMessage({ ordinal: 4, is_system: true }));
    await change({ message_count: 4 });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    expect(messages).toHaveBeenLastCalledWith({ id: "session" }, { direction: "desc", limit: 1 });
    stored.push(assistantMessage({ ordinal: 6 }));
    await change({ message_count: 5 });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it.each(["2026-10-07T11:59:00Z", "2026-10-07T12:00:01Z", "2026-10-07T12:05:00Z"])(
    "silently baselines a newly discovered waiting session at %s",
    async (ended_at) => {
      await start();
      await change({
        id: "new",
        termination_status: "awaiting_user",
        ended_at,
      });
      expect(plugin.sendNotification).not.toHaveBeenCalled();
      expect(messages).toHaveBeenCalledOnce();
    },
  );
  it.each([{ relationship_type: "subagent" }, { is_automated: true }])(
    "keeps excluded sessions silent: %j",
    async (patch) => {
      await start();
      await change({ ...patch, termination_status: "awaiting_user", message_count: 4 });
      expect(plugin.sendNotification).not.toHaveBeenCalled();
    },
  );
  it.each([
    { visible: "visible", focused: true, sent: false },
    { visible: "hidden", focused: true, sent: true },
    { visible: "visible", focused: false, sent: true },
  ])("respects window visibility and focus: %j", async ({ visible, focused, sent }) => {
    vi.spyOn(document, "visibilityState", "get").mockReturnValue(
      visible as DocumentVisibilityState,
    );
    vi.spyOn(document, "hasFocus").mockReturnValue(focused);
    await start("session");
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
  it("reads all pages before completing the silent baseline", async () => {
    list.mockResolvedValueOnce({ sessions: [], total: 1, next_cursor: "page2" });
    row.termination_status = "awaiting_user";
    await start();
    expect(list.mock.calls[1]![0]?.cursor).toBe("page2");
    await change();
    expect(plugin.sendNotification).not.toHaveBeenCalled();
  });
  it("rechecks a status flip when the user resumes while later list pages load", async () => {
    await start();
    const waiting = {
      ...row,
      termination_status: "awaiting_user",
      ended_at: "2026-10-07T12:00:01Z",
    };
    let resolve!: (value: Awaited<ReturnType<typeof SessionsService.getApiV1Sessions>>) => void;
    list.mockResolvedValueOnce({ sessions: [waiting], total: 1, next_cursor: "page2" });
    list.mockReturnValueOnce(
      new Promise((done) => {
        resolve = done;
      }),
    );
    mocks.callback();
    await flush();
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: "page2" }));
    row = { ...waiting, termination_status: "", user_message_count: 2, message_count: 3 };
    resolve({ sessions: [], total: 1 });
    await flush();
    expect(session).toHaveBeenCalledExactlyOnceWith({ id: waiting.id });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    await change(waiting);
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
    await change();
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
  it("silently baselines a waiting session returning after retention expires", async () => {
    row.termination_status = "awaiting_user";
    await start();
    vi.setSystemTime(new Date("2026-10-08T12:01:00Z"));
    list.mockResolvedValueOnce({ sessions: [], total: 0 });
    await change();
    await change({ ended_at: "2026-10-08T12:01:01Z" });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    expect(messages).toHaveBeenCalledOnce();
    await change({ message_count: 4, user_message_count: 2 });
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
  it("retries a failed waiting read and toasts once", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    row.termination_status = "awaiting_user";
    await start();
    messages.mockRejectedValueOnce(new Error("unavailable"));
    await change({ message_count: 3, termination_status: "awaiting_user" });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    await change();
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it("ignores growing and newly seen sessions while running", async () => {
    await start();
    await change({ message_count: 3 });
    await change({ id: "new", message_count: 4, ended_at: "2026-10-07T12:00:01Z" });
    expect(messages).not.toHaveBeenCalled();
    expect(session).not.toHaveBeenCalled();
    expect(plugin.sendNotification).not.toHaveBeenCalled();
  });
  it.each([
    { delta: 2, limit: 2 },
    { delta: 101, limit: 101 },
  ])("reads the waiting append: %j", async ({ delta, limit }) => {
    row.termination_status = "awaiting_user";
    await start();
    await change({ message_count: 2 + delta });
    expect(messages).toHaveBeenCalledExactlyOnceWith(
      { id: "session" },
      { direction: "desc", limit },
    );
    expect(session).toHaveBeenCalledExactlyOnceWith({ id: "session" });
    expect(plugin.sendNotification).toHaveBeenCalledExactlyOnceWith({
      title: "Fix login: turn finished",
      body: "The agent finished this turn and is waiting for you.",
    });
  });
  it("finds a final answer before more than a page of system rows", async () => {
    row.termination_status = "awaiting_user";
    await start();
    const appended = [
      assistantMessage({ ordinal: 3 }),
      ...Array.from({ length: 1100 }, (_, i) =>
        assistantMessage({
          id: i + 2,
          ordinal: i * 2 + 5,
          role: "system",
          content: "Session metadata updated",
          is_system: true,
        }),
      ),
    ];
    messages.mockImplementation(async (_, params) => {
      const page = appended
        .filter((message) => params?.from === undefined || message.ordinal <= params.from)
        .reverse()
        .slice(0, Math.min(params!.limit!, 1000));
      return { messages: page, count: page.length };
    });
    await change({ message_count: 1103 });
    expect(messages).toHaveBeenNthCalledWith(
      1,
      { id: "session" },
      { direction: "desc", limit: 1101 },
    );
    expect(messages).toHaveBeenNthCalledWith(
      2,
      { id: "session" },
      { direction: "desc", from: 204, limit: 101 },
    );
    expect(messages).toHaveBeenCalledTimes(2);
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it("stops paging at ordinal zero when the listed message count is stale", async () => {
    row.termination_status = "awaiting_user";
    await start();
    messages.mockResolvedValue({
      messages: [assistantMessage({ ordinal: 0, role: "system", is_system: true })],
      count: 1,
    });
    await change({ message_count: 4 });
    expect(messages).toHaveBeenCalledExactlyOnceWith(
      { id: "session" },
      { direction: "desc", limit: 2 },
    );
    expect(plugin.sendNotification).not.toHaveBeenCalled();
  });
  it.each([{ role: "user" }, { is_system: true }])(
    "ignores a waiting append without a new assistant message: %j",
    async (patch) => {
      row.termination_status = "awaiting_user";
      await start();
      messages.mockResolvedValue({ messages: [assistantMessage(patch)], count: 1 });
      await change({ message_count: 3 });
      expect(plugin.sendNotification).not.toHaveBeenCalled();
      expect(session).not.toHaveBeenCalled();
    },
  );
  it.each([{ termination_status: "" }, { user_message_count: 2 }, { message_count: 4 }])(
    "retries a changed session after the message read: %j",
    async (patch) => {
      row.termination_status = "awaiting_user";
      await start();
      session.mockImplementationOnce(
        async () =>
          ({ ...row, ...patch }) as Awaited<
            ReturnType<typeof SessionsService.getApiV1SessionsById>
          >,
      );
      await change({ message_count: 3 });
      expect(plugin.sendNotification).not.toHaveBeenCalled();
      await change();
      expect(messages).toHaveBeenCalledTimes(2);
      expect(plugin.sendNotification).toHaveBeenCalledOnce();
      await change();
      expect(plugin.sendNotification).toHaveBeenCalledOnce();
    },
  );
  it("retries a failed session recheck without consuming the append", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    row.termination_status = "awaiting_user";
    await start();
    session.mockRejectedValueOnce(new Error("unavailable"));
    await change({ message_count: 3 });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    await change();
    expect(messages).toHaveBeenCalledTimes(2);
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it.each([{ relationship_type: "subagent" }, { is_automated: true }])(
    "suppresses a session excluded between the list and re-read: %j",
    async (patch) => {
      await start();
      session.mockImplementationOnce(async () => ({ ...row, ...patch }));
      await change({ termination_status: "awaiting_user" });
      expect(session).toHaveBeenCalledExactlyOnceWith({ id: "session" });
      expect(plugin.sendNotification).not.toHaveBeenCalled();
    },
  );
  it("delivers the name from the current session snapshot", async () => {
    await start();
    session.mockImplementationOnce(async () => ({ ...row, display_name: "Review login" }));
    await change({ termination_status: "awaiting_user" });
    expect(plugin.sendNotification).toHaveBeenCalledExactlyOnceWith({
      title: "Review login: turn finished",
      body: "The agent finished this turn and is waiting for you.",
    });
  });
  it("suppresses delivery when the current session gains focus during the re-read", async () => {
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    const focus = vi.spyOn(document, "hasFocus").mockReturnValue(false);
    await start("session");
    session.mockImplementationOnce(async () => {
      focus.mockReturnValue(true);
      return row;
    });
    await change({ termination_status: "awaiting_user" });
    expect(session).toHaveBeenCalledExactlyOnceWith({ id: "session" });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
  });
  it("delivers after restart when the plugin reports permission denied", async () => {
    plugin.isPermissionGranted.mockResolvedValue(false);
    await start();
    await change({ termination_status: "awaiting_user", message_count: 4 });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
    expect(plugin.isPermissionGranted).not.toHaveBeenCalled();
    expect(plugin.requestPermission).not.toHaveBeenCalled();
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
