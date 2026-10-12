// Settings page entry: registers the sections and starts the router.
import { initSettingsPage, openSettings, leaveSettings, settingsOpen, isSettingsHash } from "./page.js";
import { initTagsSection } from "./tags.js";
import { initRulesSection } from "./rules.js";
import { initSimpleSections } from "./simple.js";

/** hooks: openItem(id), showTag(ref), openCollection(id), theme(v), service(), offline(), export(), forget() */
export function initSettings(hooks) {
  // Workspace actions close the settings page first, then run on the library.
  const out = (fn) => async (...args) => { await leaveSettings(); fn?.(...args); };
  const wsHooks = { openItem: out(hooks.openItem), showTag: out(hooks.showTag), openCollection: out(hooks.openCollection), service: out(hooks.service) };
  initTagsSection(wsHooks);
  initRulesSection(wsHooks);
  initSimpleSections({ ...wsHooks, theme: hooks.theme, offline: hooks.offline, export: out(hooks.export), forget: hooks.forget });
  initSettingsPage();
}
export { openSettings, leaveSettings, settingsOpen, isSettingsHash };
