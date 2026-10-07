<script lang="ts">
  import { Toggle } from "@kenn-io/kit-ui";
  import { m } from "../../i18n/index.js";
  import { settings } from "../../stores/settings.svelte.js";
  import { requestNotificationPermission } from "../../notifications.js";
</script>

<Toggle
  checked={settings.notifications.enabled}
  disabled={settings.saving || settings.readOnly}
  ariaLabel={m.settings_notifications_enable()}
  onchange={(enabled) => {
    if (enabled) void requestNotificationPermission();
    return settings.save({ notifications: { ...settings.notifications, enabled } });
  }}
>
  {m.settings_notifications_enable()}
</Toggle>
<p>{m.settings_notifications_turn_end_hint()}</p>
<Toggle
  checked={settings.notifications.notify_new_reply}
  disabled={!settings.notifications.enabled || settings.saving || settings.readOnly}
  ariaLabel={m.settings_notifications_new_reply()}
  onchange={(notify_new_reply) => settings.save({ notifications: { ...settings.notifications, notify_new_reply } })}
>
  {m.settings_notifications_new_reply()}
</Toggle>
<p>{m.settings_notifications_new_reply_hint()}</p>
