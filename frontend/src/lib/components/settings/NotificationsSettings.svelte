<script lang="ts">
  import { Toggle } from "@kenn-io/kit-ui";
  import { m } from "../../i18n/index.js";
  import { settings } from "../../stores/settings.svelte.js";
  import { notificationsAvailable, requestNotificationPermission } from "../../notifications.js";

  const available = notificationsAvailable();
  let denied = $state(false);
  let requesting = $state(false);
  let checked = $state(false);
  $effect(() => {
    checked = settings.notifications.enabled;
  });

  async function toggle(enabled: boolean) {
    requesting = true;
    try {
      denied = enabled && !(await requestNotificationPermission());
      if (!denied) await settings.save({ notifications: { enabled } });
    } finally {
      checked = settings.notifications.enabled;
      requesting = false;
    }
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
<p class="muted">{m.settings_notifications_turn_end_hint()}</p>
{#if !available}
  <p class="msg" role="status">{m.settings_notifications_unavailable()}</p>
{:else if denied}
  <p class="msg error" role="status">{m.settings_notifications_permission_denied()}</p>
{/if}

<style>
  .muted {
    font-size: 12px;
    color: var(--text-muted);
    margin: 0;
  }

  .msg {
    font-size: 11px;
    margin: 0;
  }

  .msg.error {
    color: var(--accent-red, #ef4444);
  }
</style>
