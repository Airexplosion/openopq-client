"use strict";

// Stores only a whitelisted visual theme identifier; never keys, tokens, or session state.
(() => {
  const storageKey = "openopq.appearance.v2";
  const themes = {
    obsidian: { label: "曜石流光", color: "#06080f", scheme: "dark" },
    aurora: { label: "极夜星芒", color: "#070c1a", scheme: "dark" },
    daylight: { label: "纯净极光", color: "#f8fafc", scheme: "light" },
  };

  let selected = "obsidian";
  try {
    const saved = localStorage.getItem(storageKey);
    if (saved && Object.hasOwn(themes, saved)) {
      selected = saved;
    }
  } catch {
    /* Appearance still works when browser storage is unavailable. */
  }

  function apply(name, persist = false) {
    const next = Object.hasOwn(themes, name) ? name : "obsidian";
    selected = next;
    const theme = themes[next];
    document.documentElement.dataset.theme = next;
    document.querySelector('meta[name="theme-color"]')?.setAttribute("content", theme.color);
    document.querySelector('meta[name="color-scheme"]')?.setAttribute("content", theme.scheme);
    document.querySelectorAll("[data-theme-select]").forEach((select) => {
      select.value = next;
    });
    if (persist) {
      try {
        localStorage.setItem(storageKey, next);
      } catch {
        /* Session-only choice. */
      }
      const status = document.getElementById("theme-status");
      if (status) status.textContent = "已切换为" + theme.label + "外观";
    }
    document.dispatchEvent(new CustomEvent("openopq:theme", { detail: { name: next } }));
  }

  // Runs before stylesheets paint to prevent light flashes on dark glass surfaces.
  apply(selected);

  document.addEventListener(
    "DOMContentLoaded",
    () => {
      const status = document.createElement("span");
      status.id = "theme-status";
      status.className = "sr-only";
      status.setAttribute("role", "status");
      document.body.append(status);

      document.querySelectorAll("[data-theme-select]").forEach((select) => {
        select.value = selected;
        select.addEventListener("change", () => apply(select.value, true));
      });
    },
    { once: true },
  );

  window.addEventListener("storage", (event) => {
    if (event.key === storageKey && event.newValue) {
      apply(event.newValue);
    }
  });
})();
