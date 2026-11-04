// Theme preference (system → light → dark), applied before the stylesheet paints.
(function () {
  'use strict';
  var root = document.documentElement;
  var media = window.matchMedia('(prefers-color-scheme: dark)');
  var order = ['system', 'light', 'dark'];
  var key = 'update-checker:theme';
  var pref = 'system';

  try {
    if (order.indexOf(localStorage.getItem(key)) >= 0) pref = localStorage.getItem(key);
  } catch (e) {
    // Storage unavailable: follow the OS.
  }

  function apply() {
    root.setAttribute('data-theme-pref', pref);
    root.setAttribute('data-theme', pref === 'system' ? (media.matches ? 'dark' : 'light') : pref);
    var btn = document.getElementById('theme-toggle');
    if (btn) btn.setAttribute('aria-label', 'Theme: ' + pref + '. Switch theme');
  }

  apply();
  media.addEventListener('change', apply);

  document.addEventListener('DOMContentLoaded', function () {
    var btn = document.getElementById('theme-toggle');
    if (!btn) return;
    apply();
    btn.addEventListener('click', function () {
      pref = order[(order.indexOf(pref) + 1) % order.length];
      try {
        localStorage.setItem(key, pref);
      } catch (e) {
        // Storage refused: the choice lasts this page view.
      }
      apply();
    });
  });
})();
