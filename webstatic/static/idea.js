// /idea page — browse and search ideas
(function () {
  'use strict';

  const $ = (sel, ctx) => (ctx || document).querySelector(sel);
  const $$ = (sel, ctx) => Array.from((ctx || document).querySelectorAll(sel));

  const categoryLabels = {
    'dev-tools': 'Dev Tools',
    'web-apps': 'Web Apps',
    'ai-ml': 'AI & ML',
    'databases': 'Databases',
    'games': 'Games & Fun',
    'self-hosted': 'Self-Hosted',
    'other': 'Other',
  };

  let ideas = [];

  async function init() {
    try {
      const res = await fetch('/api/ideas');
      if (res.ok) ideas = await res.json();
    } catch (e) {
      // Ideas unavailable
    }
    render(ideas);
    bindEvents();
  }

  function render(list) {
    const grid = $('#idea-grid');
    const empty = $('#idea-empty');
    if (!grid) return;

    if (list.length === 0) {
      grid.innerHTML = '';
      empty.style.display = '';
      return;
    }
    empty.style.display = 'none';

    let html = '';
    for (const t of list) {
      html += card(t);
    }
    grid.innerHTML = html;
  }

  function card(t) {
    const icon = t.icon_url || '\uD83D\uDCE6';
    const isEmoji = !icon.startsWith('http') && !icon.startsWith('/');
    const iconHtml = isEmoji
      ? `<span class="idea-card-icon">${icon}</span>`
      : `<span class="idea-card-icon"><img src="${esc(icon)}" alt=""></span>`;

    const catLabel = categoryLabels[t.category] || t.category;


    let stars = '';
    for (let i = 1; i <= 5; i++) {
      stars += i <= Math.round(t.avg_rating)
        ? '<span class="idea-card-star filled">\u2605</span>'
        : '<span class="idea-card-star">\u2605</span>';
    }
    const ratingCount = t.rating_count > 0 ? `<span class="idea-card-rating-count">(${t.rating_count})</span>` : '';

    return `<button type="button" class="idea-card" data-slug="${esc(t.slug)}">
      <div class="idea-card-header">
        ${iconHtml}
        <span class="idea-card-title">${esc(t.title)}</span>
      </div>
      <div class="idea-card-badges">
        <span class="idea-card-badge idea-card-badge-category">${esc(catLabel)}</span>
      </div>
      <div class="idea-card-desc">${esc(t.short_description)}</div>
      <div class="idea-card-rating">${stars}${ratingCount}</div>
    </button>`;
  }

  function openModal(slug) {
    const t = ideas.find(x => x.slug === slug);
    if (!t) return;

    const icon = t.icon_url || '\uD83D\uDCE6';
    const isEmoji = !icon.startsWith('http') && !icon.startsWith('/');
    $('#modal-icon').innerHTML = isEmoji ? icon : `<img src="${esc(icon)}" style="width:40px;height:40px;border-radius:8px;">`;
    $('#modal-title').textContent = t.title;
    $('#modal-desc').textContent = t.short_description;
    $('#modal-category').textContent = categoryLabels[t.category] || t.category;
    const ta = $('#modal-prompt');
    ta.value = t.prompt;

    // Build /new URL with prompt prefilled and shortname-based VM name
    const params = new URLSearchParams();
    params.set('prompt', t.prompt);
    if (t.vm_shortname) {
      params.set('name', t.vm_shortname + '-' + randomSuffix());
    }
    $('#modal-use-btn').href = '/new?' + params.toString();

    const modal = $('#idea-modal');
    modal.classList.add('open');
    document.body.style.overflow = 'hidden';
  }

  function closeModal() {
    const modal = $('#idea-modal');
    modal.classList.remove('open');
    document.body.style.overflow = '';
  }

  function filterIdeas(query) {
    if (!query) {
      render(ideas);
      return;
    }
    const q = query.toLowerCase();
    const filtered = ideas.filter(t =>
      t.title.toLowerCase().includes(q) ||
      t.short_description.toLowerCase().includes(q) ||
      t.prompt.toLowerCase().includes(q) ||
      (categoryLabels[t.category] || '').toLowerCase().includes(q)
    );
    render(filtered);
  }

  function bindEvents() {
    // Card clicks
    document.addEventListener('click', e => {
      const card = e.target.closest('.idea-card');
      if (card) {
        openModal(card.dataset.slug);
        return;
      }
    });

    // Modal close
    const backdrop = $('.idea-modal-backdrop');
    if (backdrop) backdrop.addEventListener('click', closeModal);
    const closeBtn = $('.idea-modal-close');
    if (closeBtn) closeBtn.addEventListener('click', closeModal);

    // Escape key
    document.addEventListener('keydown', e => {
      if (e.key === 'Escape') closeModal();
    });

    // Search
    const searchInput = $('#idea-search');
    if (searchInput) {
      let debounce;
      searchInput.addEventListener('input', () => {
        clearTimeout(debounce);
        debounce = setTimeout(() => filterIdeas(searchInput.value.trim()), 150);
      });
    }
  }

  const suffixWords = [
    'alpha','bravo','delta','echo','fox','gold','hawk','jade','kilo',
    'lima','nova','oak','pine','rain','sky','star','tide','wolf','zen',
  ];

  function randomSuffix() {
    return suffixWords[Math.floor(Math.random() * suffixWords.length)];
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
