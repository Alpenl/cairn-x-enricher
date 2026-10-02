// A topic has one identity. Specificity affects presentation, never membership.
export function topicSpecific(term) { return term?.granularity === "specific"; }

export function orderedTopics(ids, catalog) {
  const specific = new Set((catalog || []).filter(topicSpecific).map((term) => term.id));
  return [...new Set(ids || [])].sort((a, b) => Number(specific.has(b)) - Number(specific.has(a)));
}

export function matchesTopic(term, query) {
  const needle = query.normalize("NFKC").trim().toLocaleLowerCase();
  return !needle || [term.id, term.label, ...(term.aliases || [])]
    .some((value) => String(value || "").normalize("NFKC").toLocaleLowerCase().includes(needle));
}

export function topicSections(catalog, selected, pinned, counts, query = "", all = false) {
  const choices = (catalog || []).filter((term) => (term.active !== false && !term.deprecated) || selected.has(term.id));
  const searching = Boolean(query.trim()) || all;
  const matches = choices.filter((term) => matchesTopic(term, query));
  if (searching) return [{ id: "all", label: "全部主题", terms: matches }];
  const fixed = choices.filter((term) => pinned.has(term.id));
  const navigation = choices.filter((term) => !pinned.has(term.id) &&
    (term.navigation === true || (term.navigation !== false && !topicSpecific(term))));
  const used = new Set([...fixed, ...navigation].map((term) => term.id));
  const specific = choices.filter((term) => !used.has(term.id) && topicSpecific(term) &&
    (selected.has(term.id) || (selected.size > 0 && (counts.get(term.id) || 0) > 0)));
  const chosen = choices.filter((term) => selected.has(term.id) && !used.has(term.id) && !specific.includes(term));
  return [{ id: "pinned", label: "常用主题", terms: fixed },
    { id: "navigation", label: "浏览主题", terms: navigation },
    { id: "specific", label: "当前结果中的具体主题", terms: specific },
    { id: "selected", label: "已选主题", terms: chosen }].filter((section) => section.terms.length);
}

export function primaryTags(selection, custom, catalog) {
  return [
    ...orderedTopics(selection?.topics, catalog).map((id) => ({ field: "topics", id })),
    ...(selection?.resource_kinds || []).map((id) => ({ field: "resource_kinds", id })),
    ...(selection?.content_functions || []).map((id) => ({ field: "content_functions", id })),
    ...(custom || []).map((tag) => ({ field: "custom_tags", id: tag.id, label: tag.label }))
  ];
}
