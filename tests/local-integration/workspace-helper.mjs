import assert from "node:assert/strict";

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
