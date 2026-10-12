import assert from "node:assert/strict";

// The redesign dropped the sidebar shortcuts; management pages open through
// the workspace router (settings / ⌘K use the same entry).
export async function openPage(page, key) {
  await page.evaluate(async (key) => (await import("/assets/js/workspace.js")).navigate(key), key);
}

export async function closeWorkspace(page) {
  await page.evaluate(async () =>
    (await import("/assets/js/workspace.js")).closeWorkspace(),
  );
}

// Web Awesome renders its visible control above the native checkbox. Activate
// via its accessible keyboard interaction and wait for the reactive render.
export async function setCheckbox(scope, name, checked) {
  const field = scope.getByRole("checkbox", { name });
  if ((await field.isChecked()) !== checked) await field.press("Space");
  await field.evaluate(async (node) => {
    await node.getRootNode().host.updateComplete;
  });
  assert.equal(await field.isChecked(), checked);
}

// Full-page settings (#/settings/<section>[/<sub>]); the workspace stays mounted underneath.
export async function openSettingsPage(page, section, sub = "") {
  await page.evaluate(([section, sub]) => { location.hash = `#/settings/${section}${sub ? `/${sub}` : ""}`; }, [section, sub]);
  await page.locator("#settings-page:not([hidden])").waitFor();
  await page.locator(`#sp-nav-list [data-tab="${section}"][aria-current="page"]`).waitFor({ state: "attached" }); // the list is a dropdown on phones
}
export async function leaveSettingsPage(page) {
  await page.locator("#sp-back").click();
  await page.locator("#settings-page").waitFor({ state: "hidden" });
}
