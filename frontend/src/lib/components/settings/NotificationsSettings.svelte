<script lang="ts">
  import { Toggle } from "@kenn-io/kit-ui";
  import { m } from "../../i18n/index.js";
  import { settings } from "../../stores/settings.svelte.js";
  import { notificationPermissionGranted, requestNotificationPermission } from "../../notifications.js";

  let denied = $state(false);
  let requesting = $state(false);
  let checked = $state(false);
  $effect(() => { checked = settings.notifications.enabled; });
  $effect(() => {
    if (settings.notifications.enabled) {
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
</script>

<Toggle
  bind:checked
  disabled={requesting || settings.saving || settings.readOnly}
  ariaLabel={m.settings_notifications_enable()}
  onchange={toggle}
>
  {m.settings_notifications_enable()}
</Toggle>
<p>{m.settings_notifications_turn_end_hint()}</p>
{#if denied}
  <p role="status">{m.settings_notifications_permission_denied()}</p>
{/if}
<Toggle
  checked={settings.notifications.notify_new_reply}
  disabled={!settings.notifications.enabled || settings.saving || settings.readOnly}
  ariaLabel={m.settings_notifications_new_reply()}
  onchange={(notify_new_reply) => settings.save({ notifications: { ...settings.notifications, notify_new_reply } })}
>
  {m.settings_notifications_new_reply()}
</Toggle>
<p>{m.settings_notifications_new_reply_hint()}</p>
