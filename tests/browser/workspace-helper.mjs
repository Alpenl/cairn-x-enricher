// The 2026-10 redesign replaced the filter panel with the tag-chip popover
// under the list search field.
export async function openFilters(page) {
  if (await page.locator("#tf-pop").isHidden()) await page.locator("#tf-toggle").click();
  await page.locator("#tf-pop").waitFor();
  await page.waitForTimeout(120);
}
export async function closeFilters(page) {
  if (await page.locator("#tf-pop").isVisible()) await page.locator("#tf-toggle").click();
  await page.locator("#tf-pop").waitFor({ state: "hidden" });
}
export async function chooseTag(page, ref) {
  await openFilters(page);
  const chip = page.locator(`#tf-pop .tf-chip[data-ref="${ref}"]`);
  await chip.waitFor();
  await page.waitForFunction((value) => !document.querySelector(`#tf-pop .tf-chip[data-ref="${value}"]`)?.disabled, ref);
  await chip.click();
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
