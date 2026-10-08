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

export function startNotificationWatcher(viewingId: () => string | null): () => void {
  const seen = new Map<
    string,
    {
      status?: string;
      user_message_count: number;
      message_count: number;
      assistant_ordinal: number;
      activity: number;
    }
  >();
  let stopped = false;
  let running = false;
  let pending = false;

  function silent(row: DbSession): boolean {
    return (
      row.relationship_type === "subagent" ||
      row.is_automated ||
      (viewingId() === row.id && document.visibilityState === "visible" && document.hasFocus())
    );
  }

  function send(row: DbSession) {
    if (stopped || silent(row)) return;
    const name = row.display_name || row.project || row.agent;
    bridge()?.sendNotification({
      title: m.notification_turn_end_title_suffix({ name }),
      body: m.notification_turn_end_body(),
    });
  }

  async function refresh() {
    if (running) {
      pending = true;
      return;
    }
    running = true;
    try {
      if (stopped) return;
      const fetchedAt = Date.now();
      const rows: DbSession[] = [];
      let cursor: string | undefined;
      do {
        const result = await SessionsService.getApiV1Sessions({
          active_since: new Date(fetchedAt - FRESHNESS_MS).toISOString(),
          each_row: true,
          skip_total: true,
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
        const entry = {
          status: row.termination_status,
          user_message_count: row.user_message_count,
          message_count: row.message_count,
          assistant_ordinal: previous?.assistant_ordinal ?? -1,
          activity,
        };
        let turnEnd =
          !!previous &&
          row.termination_status === "awaiting_user" &&
          (previous.status !== "awaiting_user" ||
            previous.user_message_count !== row.user_message_count);
        try {
          if (
            row.termination_status === "awaiting_user" &&
            (!previous || turnEnd || row.message_count > previous.message_count)
          ) {
            let remaining =
              !previous || turnEnd ? row.message_count : row.message_count - previous.message_count;
            let from: number | undefined;
            while (remaining > 0 && !stopped) {
              const result = await SessionsService.getApiV1SessionsByIdMessages(
                { id: row.id },
                { direction: "desc", limit: remaining, ...(from === undefined ? {} : { from }) },
              );
              if (!result.messages.length) break;
              const assistant = result.messages.find(
                (message) =>
                  !message.is_system &&
                  !message.is_compact_boundary &&
                  message.role === "assistant",
              );
              if (assistant) {
                turnEnd ||= !!previous && assistant.ordinal > previous.assistant_ordinal;
                entry.assistant_ordinal = Math.max(entry.assistant_ordinal, assistant.ordinal);
                break;
              }
              remaining -= result.messages.length;
              from = result.messages[result.messages.length - 1]!.ordinal - 1;
              if (from < 0) break;
            }
          }
          if (turnEnd) {
            const current = await SessionsService.getApiV1SessionsById({ id: row.id });
            if (
              current.termination_status !== "awaiting_user" ||
              current.user_message_count !== row.user_message_count ||
              current.message_count !== row.message_count
            )
              continue;
            send(current);
          }
        } catch (err) {
          console.warn("notification turn-end read failed", err);
          continue;
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
