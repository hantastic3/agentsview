import { SessionsService, type DbMessage, type DbSession } from "./api/generated/index.js";
import { m } from "./i18n/index.js";
import { isToolOnly } from "./utils/content-parser.js";
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

export function notificationsAvailable(): boolean {
  return !!bridge();
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
const RETENTION_MS = 24 * 60 * 60_000;
const SAFETY_NET_REFRESH_MS = 5 * 60_000;

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
      activity: number;
      reply_ordinal: number;
    }
  >();
  const startedAt = Date.now();
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

  async function newMessages(row: DbSession, count: number): Promise<DbMessage[]> {
    const delta = row.message_count - count;
    const messages: DbMessage[] = [];
    let from: number | undefined;
    while (messages.length < delta && !stopped) {
      const result = await SessionsService.getApiV1SessionsByIdMessages(
        { id: row.id },
        { direction: "desc", limit: delta - messages.length, ...(from === undefined ? {} : { from }) },
      );
      messages.push(...result.messages);
      const last = result.messages.at(-1);
      if (!last || last.ordinal <= count) break;
      from = last.ordinal - 1;
    }
    return messages.filter((message) => message.ordinal >= count);
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
          each_row: true,
          include_one_shot: true,
          cursor,
        });
        rows.push(...result.sessions);
        cursor = result.next_cursor;
      } while (cursor && !stopped);
      if (stopped) return;
      for (const row of rows) {
        const activity = Date.parse(row.ended_at || row.started_at || "");
        const previous = seen.get(row.id);
        const silent =
          row.relationship_type === "subagent" ||
          (viewingId() === row.id && document.visibilityState === "visible" && document.hasFocus());
        const fresh = !!previous || activity > startedAt;
        const count = previous?.message_count ?? (fresh ? 0 : row.message_count);
        const entry = {
          status: row.termination_status,
          user_message_count: row.user_message_count,
          message_count: row.message_count,
          activity,
          reply_ordinal: previous?.reply_ordinal ?? -1,
        };
        let messages: DbMessage[] = [];
        if (!silent && fresh && row.message_count > count) {
          try {
            messages = await newMessages(row, count);
          } catch (err) {
            entry.message_count = count;
            entry.status = previous?.status;
            entry.user_message_count = previous?.user_message_count ?? 0;
            seen.set(row.id, entry);
            console.warn("notification reply read failed", err);
            continue;
          }
        }
        const assistants = messages.filter((message) => !message.is_system && message.role === "assistant");
        const turnEnd =
          !silent &&
          row.termination_status === "awaiting_user" &&
          (previous
            ? previous.status !== "awaiting_user" || previous.user_message_count !== row.user_message_count || assistants.length > 0
            : activity > startedAt);
        if (turnEnd) {
          send(row, "turn_end", m.notification_turn_end_body());
        } else if (!silent && row.termination_status !== "awaiting_user" && repliesEnabled()) {
          const latest = assistants.find((message) => message.ordinal > entry.reply_ordinal && message.content.trim() && !isToolOnly(message));
          if (latest) {
            send(row, "new_reply", latest.content.slice(0, 200));
            entry.reply_ordinal = latest.ordinal;
          }
        }
        seen.set(row.id, entry);
      }
      for (const [id, entry] of seen) {
        if (entry.activity < fetchedAt - RETENTION_MS) seen.delete(id);
      }
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

  const unsubscribe = events.subscribeDebounced(() => void refresh());
  const interval = setInterval(() => void refresh(), SAFETY_NET_REFRESH_MS);
  void refresh();
  return () => {
    stopped = true;
    clearInterval(interval);
    unsubscribe();
  };
}
