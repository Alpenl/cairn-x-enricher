// Two visible images at a time leave HTTP/1.1 connections for reading and filters.
// Blobs stay in this document only; private images never enter persistent caches.
import { on } from "./store.js";
import { cachedBlob, blobCacheStats } from "./blob-cache.js";

const jobs = new Map();
let active = 0,
  reading = false,
  searching = false,
  sequence = 0;
const observer =
  typeof IntersectionObserver === "function"
    ? new IntersectionObserver(
        (entries) => {
          for (const entry of entries) {
            const job = jobs.get(entry.target);
            if (job) job.visible = entry.isIntersecting;
          }
          pump();
        },
        { rootMargin: "120px 0px" },
      )
    : null;

function dispose(job) {
  jobs.delete(job.image);
  observer?.unobserve(job.image);
  job.controller?.abort();
  if (job.objectURL) URL.revokeObjectURL(job.objectURL);
}
function prune() {
  for (const job of jobs.values()) if (!job.image.isConnected) dispose(job);
}
function pump() {
  prune();
  if (reading || searching || document.hidden) return;
  const ready = [...jobs.values()]
    .filter((job) => job.visible && !job.done && !job.controller)
    .sort((a, b) => a.priority - b.priority || a.order - b.order);
  for (const job of ready) {
    if (active >= 2) break;
    const controller = new AbortController();
    job.controller = controller;
    active++;
    cachedBlob(job.source, controller.signal)
      .then((blob) => {
        if (controller.signal.aborted || !jobs.has(job.image)) return;
        job.objectURL = URL.createObjectURL(blob);
        job.done = true;
        job.image.src = job.objectURL;
      })
      .catch((error) => {
        if (error.name !== "AbortError" && jobs.has(job.image)) {
          job.done = true;
          job.image.dispatchEvent(new Event("error"));
        }
      })
      .finally(() => {
        job.controller = null;
        active--;
        pump();
      });
  }
}
export function queueImage(image, source, { priority = 1 } = {}) {
  const old = jobs.get(image);
  if (old) dispose(old);
  image.dataset.imageSource = source;
  const job = {
    image,
    source,
    priority,
    visible: !observer,
    order: sequence++,
    done: false,
    controller: null,
    objectURL: null,
  };
  jobs.set(image, job);
  observer?.observe(image);
  // Callers commonly build a detached fragment before attaching it this turn.
  queueMicrotask(pump);
}
// Cached rows may reconnect after prune revoked their object URL. Rejoin the
// scheduler using the same bounded byte cache, without reviving old jobs.
export function resumeImage(image) {
  if (!jobs.has(image) && image.dataset.imageSource)
    queueImage(image, image.dataset.imageSource);
}
export function prioritizeReading(value) {
  reading = value;
  if (reading) for (const job of jobs.values()) job.controller?.abort();
  else pump();
}
export function prioritizeSearch(value) {
  searching = value;
  if (searching) for (const job of jobs.values()) job.controller?.abort();
  else pump();
}
export function clearImages(root) {
  for (const job of jobs.values()) if (root.contains(job.image)) dispose(job);
}
export function imageQueueStats() {
  return {
    active,
    queued: [...jobs.values()].filter((job) => !job.done).length,
    retained: jobs.size,
    reading,
    cache: blobCacheStats(),
  };
}
if (typeof document !== "undefined") {
  new MutationObserver(() => {
    prune();
    pump();
  }).observe(document.documentElement, { childList: true, subtree: true });
  document.addEventListener("visibilitychange", () => {
    if (document.hidden)
      for (const job of jobs.values()) job.controller?.abort();
    else pump();
  });
}
on("account:changed", () => {
  for (const job of jobs.values()) dispose(job);
});
