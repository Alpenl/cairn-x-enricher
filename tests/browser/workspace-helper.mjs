export async function openFilters(page) {
  const panel = page.locator("#filter-panel");
  if (!(await panel.evaluate((node) => node.open)))
    await page.locator("#filter-button").click();
  await page.waitForFunction(
    () => document.querySelector("#filter-panel").open,
  );
  await page.waitForTimeout(120);
}
export async function closeFilters(page) {
  if (await page.locator("#filter-panel").evaluate((node) => node.open))
    await page.locator("#filter-done").click();
}
export async function openCuration(page) {
  await closeFilters(page);
  await page.waitForFunction(async () =>
    Boolean((await import("/assets/js/store.js")).state.selectedId),
  );
  await page.evaluate(async () =>
    (await import("/assets/js/workspace.js")).showInspector("curation"),
  );
  await page.locator("#curation-why").waitFor();
}
export async function closeInspector(page) {
  await page.evaluate(async () =>
    (await import("/assets/js/workspace.js")).closeInspector(),
  );
  await page
    .locator("wa-dialog.inspector-drawer")
    .waitFor({ state: "detached" });
}
