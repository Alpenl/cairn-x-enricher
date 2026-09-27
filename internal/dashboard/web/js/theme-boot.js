// Classic (non-module) script loaded synchronously in <head>: applies a saved
// theme before the first paint. The CSP forbids inline scripts, hence a file.
(function () {
  try {
    var theme = localStorage.getItem("cairn.theme");
    if (theme === "light" || theme === "dark") document.documentElement.setAttribute("data-theme", theme);
  } catch (error) {
    // Storage may be unavailable; the system preference still applies.
  }
})();
