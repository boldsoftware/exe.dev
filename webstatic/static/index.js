// Homepage ideas drawer
(function () {
  'use strict';

  const $ = (sel) => document.querySelector(sel);
  let ideas = [];

  const categoryLabels = {
    'dev-tools': 'Dev Tools',
    'web-apps': 'Web Apps',
    'ai-ml': 'AI & ML',
    'databases': 'Databases',
    'games': 'Games & Fun',
    'self-hosted': 'Self-Hosted',
    'other': 'Other',
  };

  const categoryOrder = ['dev-tools', 'web-apps', 'ai-ml', 'databases', 'games', 'self-hosted', 'other'];

  async function init() {
    const btn = $('#ideas-btn');
    if (!btn) return;

    try {
      const res = await fetch('/api/ideas');
      if (res.ok) ideas = await res.json();
    } catch (e) {
      // Ideas unavailable
    }

    if (ideas.length === 0) {
      btn.style.display = 'none';
      return;
    }

    btn.addEventListener('click', openDrawer);

    const backdrop = $('#ideas-drawer .ideas-backdrop');
    if (backdrop) backdrop.addEventListener('click', closeDrawer);
    const closeBtn = $('#ideas-drawer .ideas-close');
    if (closeBtn) closeBtn.addEventListener('click', closeDrawer);

    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape') closeDrawer();
    });

    document.addEventListener('click', function (e) {
      const card = e.target.closest('.ideas-card');
      if (!card) return;
      selectIdea(card.dataset.slug);
    });
  }

  function openDrawer() {
    const drawer = $('#ideas-drawer');
    if (!drawer) return;
    renderSections();
    drawer.classList.add('open');
    document.body.style.overflow = 'hidden';
  }

  function closeDrawer() {
    const drawer = $('#ideas-drawer');
    if (!drawer) return;
    drawer.classList.remove('open');
    document.body.style.overflow = '';
  }

  function selectIdea(slug) {
    const t = ideas.find(function (x) { return x.slug === slug; });
    if (!t) return;
    const prompt = $('#prompt-input');
    if (prompt && t.prompt) {
      prompt.value = t.prompt;
    }
    closeDrawer();
  }

  function renderSections() {
    const container = $('#ideas-sections');
    if (!container) return;

    let html = '';
    for (const catSlug of categoryOrder) {
      const items = ideas.filter(function (t) { return t.category === catSlug; });
      if (items.length === 0) continue;
      const label = categoryLabels[catSlug] || catSlug;
      html += '<section class="ideas-section">';
      html += '<h3 class="ideas-section-title">' + esc(label) + '</h3>';
      html += '<div class="ideas-grid">';
      for (const t of items) {
        html += ideaCard(t);
      }
      html += '</div></section>';
    }

    if (!html) {
      html = '<div class="ideas-empty">No ideas available.</div>';
    }
    container.innerHTML = html;
  }

  function ideaCard(t) {
    const icon = t.icon_url || '\uD83D\uDCE6';
    const isEmoji = !icon.startsWith('http') && !icon.startsWith('/');
    const iconHtml = isEmoji
      ? '<span class="ideas-card-icon">' + icon + '</span>'
      : '<span class="ideas-card-icon"><img src="' + esc(icon) + '" alt=""></span>';

    return '<button type="button" class="ideas-card" data-slug="' + esc(t.slug) + '">'
      + '<div class="ideas-card-header">' + iconHtml + '</div>'
      + '<div class="ideas-card-title">' + esc(t.title) + '</div>'
      + '<div class="ideas-card-desc">' + esc(t.short_description) + '</div>'
      + '</button>';
  }

  function esc(str) {
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
