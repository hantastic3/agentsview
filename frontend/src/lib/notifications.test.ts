import { afterEach, beforeEach, describe, expect, it, vi } from "vite-plus/test";
import { SessionsService, type DbSession } from "./api/generated/index.js";
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
const session = vi.mocked(SessionsService.getApiV1SessionsById);
async function flush() {
  for (let i = 0; i < 15; i++) await Promise.resolve();
}
async function start(viewing: string | null = null) {
  stop = startNotificationWatcher(() => viewing);
  await flush();
  expect(list).toHaveBeenCalled();
}
async function change(patch: Partial<DbSession> = {}) {
  row = { ...row, ...patch };
  mocks.callback();
  await flush();
}
beforeEach(() => {
  vi.resetAllMocks();
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-10-07T12:00:00Z"));
  vi.stubGlobal("__TAURI__", { notification: plugin });
  plugin.isPermissionGranted.mockResolvedValue(true);
  vi.spyOn(document, "hasFocus").mockReturnValue(true);
  vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
  row = {
    id: "session",
    agent: "claude",
    project: "demo",
    display_name: "Fix login",
    ended_at: "2026-10-07T12:00:00Z",
    message_count: 2,
    user_message_count: 1,
    total_output_tokens: 100,
  } as DbSession;
  list.mockImplementation(async () => ({ sessions: [row], total: 1 }));
  session.mockImplementation(
    async () => row as Awaited<ReturnType<typeof SessionsService.getApiV1SessionsById>>,
  );
});
afterEach(() => {
  stop?.();
  stop = undefined;
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
describe("desktop notification watcher", () => {
  it("records a waiting baseline silently", async () => {
    row.termination_status = "awaiting_user";
    await start();
    await change();
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    expect(session).not.toHaveBeenCalled();
  });
  it("notifies once on completion", async () => {
    await start();
    await change({ termination_status: "awaiting_user" });
    expect(session).toHaveBeenCalledExactlyOnceWith({ id: "session" });
    expect(plugin.sendNotification).toHaveBeenCalledExactlyOnceWith({
      title: "Fix login: turn finished",
      body: "The agent finished this turn and is waiting for you.",
    });
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it.each([
    { user_message_count: 2 },
    { total_output_tokens: 200 },
    { ended_at: "2026-10-07T12:01:00Z" },
  ])("notifies on a new waiting turn: %j", async (patch) => {
    row.termination_status = "awaiting_user";
    await start();
    await change(patch);
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it("notifies after a rewind and resend repeats the completed turn's counts", async () => {
    await start();
    await change({ termination_status: "awaiting_user" });
    await change({ termination_status: "clean", user_message_count: 0, total_output_tokens: 0 });
    await change({
      termination_status: "awaiting_user",
      user_message_count: 1,
      total_output_tokens: 100,
      ended_at: "2026-10-07T12:01:00Z",
    });
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledTimes(2);
  });
  it.each([
    { started_at: "2026-10-07T11:59:55Z", sent: true },
    { started_at: "2026-10-07T12:01:00Z", sent: true },
    { started_at: "2026-10-07T11:59:54Z", sent: false },
    { started_at: null, created_at: "2026-10-07T12:01:00Z", sent: true },
    { started_at: "2026-10-07T11:00:00Z", created_at: "2026-10-07T12:01:00Z", sent: false },
  ])("handles a first-seen waiting session: %j", async ({ sent, ...patch }) => {
    list.mockResolvedValueOnce({ sessions: [], total: 0 });
    await start();
    vi.setSystemTime(new Date("2026-10-07T12:02:00Z"));
    await change({ ...patch, termination_status: "awaiting_user" });
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledTimes(sent ? 1 : 0);
  });
  it("ignores metadata and status flicker with the same key", async () => {
    row.termination_status = "awaiting_user";
    await start();
    await change({ message_count: 3, display_name: "Renamed" });
    await change({ termination_status: undefined });
    await change({ termination_status: "clean" });
    await change({ termination_status: "awaiting_user", message_count: 4 });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    expect(session).not.toHaveBeenCalled();
  });
  it.each([{ relationship_type: "subagent" }, { is_automated: true }, {}])(
    "remembers silent completions: %j",
    async (patch) => {
      await start("session");
      session.mockImplementation(
        async () =>
          ({ ...row, ...patch }) as Awaited<
            ReturnType<typeof SessionsService.getApiV1SessionsById>
          >,
      );
      await change({ termination_status: "awaiting_user" });
      vi.spyOn(document, "hasFocus").mockReturnValue(false);
      await change();
      expect(plugin.sendNotification).not.toHaveBeenCalled();
      expect(session).toHaveBeenCalledOnce();
    },
  );
  it("notifies for the viewed session while the window is hidden", async () => {
    await start("session");
    vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    await change({ termination_status: "awaiting_user" });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it.each([{ total_output_tokens: 200 }, { termination_status: "clean" }])(
    "retries a changed re-read: %j",
    async (patch) => {
      await start();
      session.mockResolvedValueOnce({ ...row, ...patch } as Awaited<
        ReturnType<typeof SessionsService.getApiV1SessionsById>
      >);
      await change({ termination_status: "awaiting_user" });
      expect(plugin.sendNotification).not.toHaveBeenCalled();
      await change();
      expect(plugin.sendNotification).toHaveBeenCalledOnce();
      expect(session).toHaveBeenCalledTimes(2);
    },
  );
  it("retries a failed re-read", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    await start();
    session.mockRejectedValueOnce(new Error("unavailable"));
    await change({ termination_status: "awaiting_user" });
    await change();
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it("discovers a completion after refresh failures longer than ten minutes", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    await start();
    list.mockRejectedValue(new Error("unavailable"));
    vi.setSystemTime(new Date("2026-10-07T12:01:00Z"));
    await change({ termination_status: "awaiting_user", ended_at: "2026-10-07T12:01:00Z" });
    vi.setSystemTime(new Date("2026-10-07T12:15:00Z"));
    await change();
    list.mockImplementation(async (params) => ({
      sessions: Date.parse(row.ended_at!) >= Date.parse(params!.active_since!) ? [row] : [],
      total: 1,
    }));
    vi.setSystemTime(new Date("2026-10-07T12:20:00Z"));
    await change();
    expect(list).toHaveBeenLastCalledWith({
      active_since: "2026-10-07T12:00:00.000Z",
      each_row: true,
      include_one_shot: true,
      cursor: undefined,
    });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
    await change();
    expect(list).toHaveBeenLastCalledWith({
      active_since: "2026-10-07T12:10:00.000Z",
      each_row: true,
      include_one_shot: true,
      cursor: undefined,
    });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it("reads every page", async () => {
    list.mockImplementation(async (params) =>
      params?.cursor
        ? { sessions: [row], total: 2 }
        : { sessions: [{ ...row, id: "other" }], total: 2, next_cursor: "page-2" },
    );
    await start();
    expect(list).toHaveBeenNthCalledWith(1, {
      active_since: "2026-10-07T11:50:00.000Z",
      each_row: true,
      include_one_shot: true,
      cursor: undefined,
    });
    expect(list).toHaveBeenNthCalledWith(2, {
      active_since: "2026-10-07T11:50:00.000Z",
      each_row: true,
      include_one_shot: true,
      cursor: "page-2",
    });
    await change({ termination_status: "awaiting_user" });
    expect(plugin.sendNotification).toHaveBeenCalledTimes(2);
  });
  it("remembers re-entry after ten idle minutes", async () => {
    row.termination_status = "awaiting_user";
    await start();
    list.mockResolvedValue({ sessions: [], total: 0 });
    vi.setSystemTime(new Date("2026-10-07T12:11:00Z"));
    await change();
    list.mockImplementation(async () => ({ sessions: [row], total: 1 }));
    await change({ message_count: 3 });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    await change({ total_output_tokens: 200 });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it("prunes entries unseen for 24 hours", async () => {
    row.termination_status = "awaiting_user";
    await start();
    list.mockResolvedValue({ sessions: [], total: 0 });
    vi.setSystemTime(new Date("2026-10-08T12:01:00Z"));
    await change();
    list.mockImplementation(async () => ({ sessions: [row], total: 1 }));
    await change({ total_output_tokens: 200 });
    expect(plugin.sendNotification).not.toHaveBeenCalled();
    await change({ total_output_tokens: 300 });
    expect(plugin.sendNotification).toHaveBeenCalledOnce();
  });
  it("unsubscribes and cancels refreshes on stop", async () => {
    await start();
    stop?.();
    expect(mocks.unsubscribe).toHaveBeenCalledOnce();
    const calls = list.mock.calls.length;
    await vi.advanceTimersByTimeAsync(5 * 60_000);
    await change({ termination_status: "awaiting_user" });
    expect(list).toHaveBeenCalledTimes(calls);
    expect(plugin.sendNotification).not.toHaveBeenCalled();
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
