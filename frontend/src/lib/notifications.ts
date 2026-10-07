import { SessionsService, type DbSession } from "./api/generated/index.js";
import { m } from "./i18n/index.js";
import { events } from "./stores/events.svelte.js";

type NotificationBridge = {
  isPermissionGranted: () => Promise<boolean>;
  requestPermission: () => Promise<string>;
  sendNotification: (options: { title: string; body: string }) => void;
};

function bridge() {
  return (window as Window & { __TAURI__?: { notification?: NotificationBridge } }).__TAURI__
    ?.notification;
}

export async function notificationPermissionGranted(): Promise<boolean> {
  try {
    return (await bridge()?.isPermissionGranted()) ?? false;
  } catch {
    return false;
  }
}

export async function requestNotificationPermission(): Promise<boolean> {
  try {
    return (
      (await notificationPermissionGranted()) || (await bridge()?.requestPermission()) === "granted"
    );
  } catch {
    return false;
  }
}

const FRESHNESS_MS = 10 * 60_000;
const DEBOUNCE_MS = 300;

export function startNotificationWatcher(
  repliesEnabled: () => boolean,
  viewingId: () => string | null,
): () => void {
  const seen = new Map<
    string,
    {
      status?: string;
      user_message_count: number;
      message_count: number;
      lastSeenTs: number;
      activity: number;
    }
  >();
  let baseline = true;
  let stopped = false;
  let running = false;
  let pending = false;

  function send(row: DbSession, kind: "turn_end" | "new_reply", body: string) {
    if (stopped) return;
    if (viewingId() === row.id && document.visibilityState === "visible" && document.hasFocus())
      return;
    if (kind === "new_reply" && !repliesEnabled()) return;
    const name = row.display_name || row.project || row.agent;
    bridge()?.sendNotification({
      title:
        kind === "turn_end"
          ? m.notification_turn_end_title_suffix({ name })
          : m.notification_new_reply_title_suffix({ name }),
      body,
    });
  }

  async function refresh() {
    if (running) {
      pending = true;
      return;
    }
    running = true;
    try {
      if (!(await notificationPermissionGranted()) || stopped) return;
      const fetchedAt = Date.now();
      const rows: DbSession[] = [];
      let cursor: string | undefined;
      do {
        const result = await SessionsService.getApiV1Sessions({
          active_since: new Date(fetchedAt - FRESHNESS_MS).toISOString(),
          include_children: true,
          include_one_shot: true,
          cursor,
        });
        rows.push(...result.sessions);
        cursor = result.next_cursor;
      } while (cursor && !stopped);
      if (stopped) return;
      for (const row of rows) {
        const activity = Date.parse(row.ended_at || row.started_at || "");
        if (!Number.isFinite(activity) || activity < fetchedAt - FRESHNESS_MS) continue;
        const previous = seen.get(row.id);
        const silent =
          baseline ||
          row.relationship_type === "subagent" ||
          (viewingId() === row.id && document.visibilityState === "visible" && document.hasFocus());
        const turnEnd =
          !silent &&
          row.termination_status === "awaiting_user" &&
          (!previous ||
            previous.status !== "awaiting_user" ||
            previous.user_message_count !== row.user_message_count);
        if (turnEnd) {
          send(row, "turn_end", m.notification_turn_end_body());
        }
        const entry = {
          status: row.termination_status,
          user_message_count: row.user_message_count,
          message_count: previous?.message_count ?? 0,
          lastSeenTs: previous?.lastSeenTs ?? (baseline ? activity : fetchedAt - FRESHNESS_MS),
          activity,
        };
        seen.set(row.id, entry);
        if (!baseline && (turnEnd || row.message_count > (previous?.message_count ?? 0)) && repliesEnabled()) {
          try {
            const result = await SessionsService.getApiV1SessionsByIdMessages(
              { id: row.id },
              { direction: "desc", limit: 20 },
            );
            const newer = result.messages.filter(
              (message) => Date.parse(message.timestamp ?? "") > entry.lastSeenTs,
            );
            const latest = newer.find(
              (message) =>
                !message.is_system &&
                message.role === "assistant" &&
                !message.has_tool_use &&
                message.content.trim(),
            );
            if (!silent && !turnEnd && latest) {
              send(row, "new_reply", latest.content.slice(0, 200));
            }
            for (const message of newer) {
              entry.lastSeenTs = Math.max(entry.lastSeenTs, Date.parse(message.timestamp ?? ""));
            }
            entry.message_count = row.message_count;
          } catch (err) {
            console.warn("notification reply read failed", err);
          }
        } else {
          entry.message_count = row.message_count;
        }
      }
      for (const [id, entry] of seen) {
        if (entry.activity < fetchedAt - FRESHNESS_MS) seen.delete(id);
      }
      baseline = false;
    } catch (err) {
      console.warn("notification session read failed", err);
    } finally {
      running = false;
      if (pending && !stopped) {
        pending = false;
        void refresh();
      }
    }
  }

  const unsubscribe = events.subscribeDebounced(() => void refresh(), DEBOUNCE_MS);
  void refresh();
  return () => {
    stopped = true;
    unsubscribe();
  };
}
