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

async function notificationPermissionGranted(): Promise<boolean> {
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

function finished(row: DbSession): boolean {
  return row.termination_status === "awaiting_user" && !row.turn_open && !!row.last_reply_id;
}

const FRESHNESS_MS = 10 * 60_000;
const SAFETY_NET_REFRESH_MS = 5 * 60_000;

export function startNotificationWatcher(viewingId: () => string | null): () => void {
  const seen = new Map<string, { replyId?: string }>();
  let coveredSince = Date.now();
  let stopped = false;
  let running = false;
  let pending = false;

  function silent(row: DbSession): boolean {
    return (
      row.is_automated ||
      row.relationship_type === "subagent" ||
      (viewingId() === row.id && document.visibilityState === "visible" && document.hasFocus())
    );
  }

  function send(row: DbSession) {
    if (silent(row)) return;
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
          active_since: new Date(Math.min(fetchedAt - FRESHNESS_MS, coveredSince)).toISOString(),
          each_row: true,
          include_one_shot: true,
          cursor,
        });
        rows.push(...result.sessions);
        cursor = result.next_cursor;
      } while (cursor && !stopped);
      if (stopped) return;
      let readsSucceeded = true;
      for (const row of rows) {
        const previous = seen.get(row.id);
        if (!previous) {
          seen.set(row.id, { replyId: finished(row) ? row.last_reply_id : undefined });
          continue;
        }
        if (!finished(row) || previous.replyId === row.last_reply_id) continue;
        try {
          const current = await SessionsService.getApiV1SessionsById({ id: row.id });
          if (stopped) return;
          if (finished(current) && current.last_reply_id === row.last_reply_id) {
            send(current);
            previous.replyId = row.last_reply_id;
          }
        } catch (err) {
          readsSucceeded = false;
          console.warn("notification turn-end read failed", err);
        }
      }
      if (readsSucceeded) coveredSince = fetchedAt;
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
