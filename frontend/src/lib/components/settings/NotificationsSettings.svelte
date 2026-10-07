<script lang="ts">
  import { Toggle } from "@kenn-io/kit-ui";
  import { m } from "../../i18n/index.js";
  import { settings } from "../../stores/settings.svelte.js";
  import { notificationsAvailable, notificationPermissionGranted, requestNotificationPermission } from "../../notifications.js";

  const available = notificationsAvailable();
  let denied = $state(false);
  let requesting = $state(false);
  let checked = $state(false);
  let repliesChecked = $state(false);
  $effect(() => { checked = settings.notifications.enabled; });
  $effect(() => { repliesChecked = settings.notifications.notify_new_reply; });
  $effect(() => {
    if (available && settings.notifications.enabled) {
      let active = true;
      void notificationPermissionGranted().then((granted) => {
        if (active) denied = !granted;
      });
      return () => { active = false; };
    }
  });

  async function toggle(enabled: boolean) {
    requesting = true;
    try {
      denied = enabled && !(await requestNotificationPermission());
      if (!denied) await settings.save({ notifications: { ...settings.notifications, enabled } });
    } finally {
      checked = settings.notifications.enabled;
      requesting = false;
    }
  }

  async function toggleReplies(notify_new_reply: boolean) {
    await settings.save({ notifications: { ...settings.notifications, notify_new_reply } });
    repliesChecked = settings.notifications.notify_new_reply;
  }
</script>

<Toggle
  bind:checked
  disabled={!available || requesting || settings.saving || settings.readOnly}
  ariaLabel={m.settings_notifications_enable()}
  onchange={toggle}
>
  {m.settings_notifications_enable()}
</Toggle>
<p>{m.settings_notifications_turn_end_hint()}</p>
{#if !available}
  <p role="status">{m.settings_notifications_unavailable()}</p>
{:else if denied}
  <p role="status">{m.settings_notifications_permission_denied()}</p>
{/if}
<Toggle
  bind:checked={repliesChecked}
  disabled={!available || !settings.notifications.enabled || settings.saving || settings.readOnly}
  ariaLabel={m.settings_notifications_new_reply()}
  onchange={toggleReplies}
>
  {m.settings_notifications_new_reply()}
</Toggle>
<p>{m.settings_notifications_new_reply_hint()}</p>
