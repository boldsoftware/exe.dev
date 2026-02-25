// Idea integration for /new page — segmented control + drawer
(function () {
  'use strict';

  const state = {
    templates: [],
    selectedTemplate: null,
    pendingTemplate: null,
    hostnameTouched: false,
    lastGeneratedHostname: null,
  };

  const $ = (sel, ctx) => (ctx || document).querySelector(sel);

  // --- Init ---
  async function init() {
    try {
      const res = await fetch('/api/ideas');
      if (res.ok) {
        state.templates = await res.json();
      }
    } catch (e) {
      // Templates unavailable
    }
    // Hide the Ideas tab if no ideas
    if (state.templates.length === 0) {
      const seg = $('#seg-control');
      if (seg) seg.style.display = 'none';
      return;
    }
    bindEvents();

    // If the page was loaded with a pre-selected idea, show the pill.
    const form = $('#create-vm-form');
    const preselectedSlug = form && form.dataset.idea;
    if (preselectedSlug) {
      const t = state.templates.find(x => x.slug === preselectedSlug);
      if (t) {
        state.selectedTemplate = t;
        showTemplatePill(t);
      }
    }
  }

  // --- Segmented control ---
  function setMode(mode) {
    const seg = $('#seg-control');
    if (!seg) return;
    seg.dataset.active = mode;
    for (const btn of seg.querySelectorAll('.seg-btn')) {
      btn.classList.toggle('active', btn.dataset.mode === mode);
    }
    if (mode === 'templates') {
      openDrawer();
    }
  }

  // --- Drawer ---
  function openDrawer() {
    const drawer = $('#template-drawer');
    if (!drawer) return;
    drawer.classList.add('open');
    document.body.style.overflow = 'hidden';
    renderDrawer();
  }

  function closeDrawer() {
    const drawer = $('#template-drawer');
    if (!drawer) return;
    drawer.classList.remove('open');
    document.body.style.overflow = '';
    // Switch segmented control back to Describe
    setModeQuiet('describe');
  }

  // Set mode visually without triggering drawer open
  function setModeQuiet(mode) {
    const seg = $('#seg-control');
    if (!seg) return;
    seg.dataset.active = mode;
    for (const btn of seg.querySelectorAll('.seg-btn')) {
      btn.classList.toggle('active', btn.dataset.mode === mode);
    }
  }

  function renderDrawer() {
    renderTemplateSections();
  }

  function renderTemplateSections() {
    const container = $('#template-sections');
    if (!container) return;
    const cats = getCategories();
    let html = '';

    for (const cat of cats) {
      const templates = state.templates.filter(t => t.category === cat.slug);
      if (templates.length === 0) continue;
      html += `<section class="template-section">`;
      html += `<h3 class="section-title">${cat.label}</h3>`;
      html += `<div class="drawer-grid">${templates.map(t => templateCard(t)).join('')}</div>`;
      html += `</section>`;
    }

    if (html === '') {
      html = '<div class="no-results">No ideas available.</div>';
    }
    container.innerHTML = html;
  }

  function templateCard(t) {
    const stars = renderStars(t.avg_rating);
    const ratingText = t.rating_count > 0 ? `<span class="rating-count">(${t.rating_count})</span>` : '';

    const icon = t.icon_url || '\uD83D\uDCE6';
    const isEmoji = !icon.startsWith('http') && !icon.startsWith('/');
    const iconHtml = isEmoji
      ? `<span class="card-icon-emoji">${icon}</span>`
      : `<img class="card-icon-img" src="${escHtml(icon)}" alt="">`;
    const selectedClass = state.selectedTemplate && state.selectedTemplate.slug === t.slug ? ' selected' : '';

    return `<button type="button" class="template-card${selectedClass}" data-slug="${escHtml(t.slug)}">
      <div class="card-header">
        ${iconHtml}
      </div>
      <div class="card-title">${escHtml(t.title)}</div>
      <div class="card-desc">${escHtml(t.short_description)}</div>
      <div class="card-footer">
        <div class="card-rating">${stars}${ratingText}</div>
      </div>
    </button>`;
  }

  function renderStars(avg) {
    let html = '';
    for (let i = 1; i <= 5; i++) {
      html += i <= Math.round(avg)
        ? '<span class="star filled">\u2605</span>'
        : '<span class="star">\u2605</span>';
    }
    return html;
  }

  // --- Template selection ---
  function isImageTemplate(t) {
    return t.image && !t.prompt;
  }

  function requestSelectTemplate(slug) {
    const t = state.templates.find(x => x.slug === slug);
    if (!t) return;

    // Image-only templates don't touch the prompt, so no conflict possible.
    if (isImageTemplate(t)) {
      applyTemplate(t, 'replace');
      return;
    }

    const promptInput = $('#prompt');
    const currentText = promptInput ? promptInput.value.trim() : '';
    const wasFromTemplate = state.selectedTemplate && currentText === state.selectedTemplate.prompt.trim();

    if (currentText === '' || wasFromTemplate) {
      applyTemplate(t, 'replace');
    } else {
      state.pendingTemplate = t;
      showInsertConfirm();
    }
  }

  const suffixWords = [
    'alpha','bravo','delta','echo','fox','gold','hawk','jade','kilo',
    'lima','nova','oak','pine','rain','sky','star','tide','wolf','zen',
  ];

  function randomSuffix() {
    return suffixWords[Math.floor(Math.random() * suffixWords.length)];
  }

  function applyTemplate(t, mode) {
    const promptInput = $('#prompt');
    if (!promptInput) return;

    if (isImageTemplate(t)) {
      // Image-only template: set the image field instead of the prompt.
      const imgInput = $('#image-input');
      if (imgInput) {
        imgInput.value = t.image;
        imgInput.dispatchEvent(new Event('input', { bubbles: true }));
      }
      // Open the options section so the user sees the image.
      const opts = $('#options-section');
      if (opts) opts.open = true;
    } else {
      if (mode === 'replace') {
        promptInput.value = t.prompt;
      } else if (mode === 'append') {
        promptInput.value = promptInput.value.trimEnd() + '\n\n' + t.prompt;
      }
    }
    state.selectedTemplate = t;

    // Update hostname if user hasn't manually edited it
    if (!state.hostnameTouched && t.vm_shortname) {
      const hostnameInput = $('#hostname');
      if (hostnameInput) {
        const newName = t.vm_shortname + '-' + randomSuffix();
        hostnameInput.value = newName;
        state.lastGeneratedHostname = newName;
        // Trigger availability check
        hostnameInput.dispatchEvent(new Event('input', { bubbles: true }));
      }
    }

    showTemplatePill(t);
    closeDrawer();
    hideInsertConfirm();
  }

  function clearSelection() {
    if (state.selectedTemplate) {
      if (isImageTemplate(state.selectedTemplate)) {
        // Clear the image field and re-enable the prompt.
        const imgInput = $('#image-input');
        if (imgInput) {
          imgInput.value = '';
          imgInput.dispatchEvent(new Event('input', { bubbles: true }));
        }
      } else {
        const promptInput = $('#prompt');
        if (promptInput && promptInput.value.trim() === state.selectedTemplate.prompt.trim()) {
          promptInput.value = '';
        }
      }
    }
    // If hostname still matches the last generated name, allow future templates to update it
    const hostnameInput = $('#hostname');
    if (hostnameInput && hostnameInput.value === state.lastGeneratedHostname) {
      state.hostnameTouched = false;
    }
    state.selectedTemplate = null;
    hideTemplatePill();
    renderTemplateSections();
  }

  // --- Template pill ---
  function showTemplatePill(t) {
    const pill = $('#template-pill');
    if (!pill) return;
    const icon = t.icon_url || '\uD83D\uDCE6';
    const isEmoji = !icon.startsWith('http') && !icon.startsWith('/');
    pill.querySelector('.template-pill-icon').innerHTML = isEmoji
      ? icon
      : `<img src="${escHtml(icon)}" style="width:14px;height:14px;border-radius:3px;">`;
    pill.querySelector('.template-pill-title').textContent = t.title;
    pill.style.display = 'inline-flex';
  }

  function hideTemplatePill() {
    const pill = $('#template-pill');
    if (pill) pill.style.display = 'none';
  }

  // --- Insert confirmation ---
  function showInsertConfirm() {
    const el = $('#insert-confirm');
    if (el) el.style.display = 'flex';
  }

  function hideInsertConfirm() {
    const el = $('#insert-confirm');
    if (el) el.style.display = 'none';
    state.pendingTemplate = null;
  }

  // --- Categories ---
  function getCategories() {
    return [
      { slug: 'dev-tools', label: 'Dev Tools' },
      { slug: 'web-apps', label: 'Web Apps' },
      { slug: 'ai-ml', label: 'AI & ML' },
      { slug: 'databases', label: 'Databases' },
      { slug: 'games', label: 'Games & Fun' },
      { slug: 'self-hosted', label: 'Self-Hosted' },
      { slug: 'other', label: 'Other' },
    ];
  }

  // --- Event Binding ---
  function bindEvents() {
    // Segmented control
    const seg = $('#seg-control');
    if (seg) {
      seg.addEventListener('click', e => {
        const btn = e.target.closest('.seg-btn');
        if (!btn) return;
        setMode(btn.dataset.mode);
      });
    }

    // Drawer backdrop / close
    const drawerBackdrop = $('#template-drawer .drawer-backdrop');
    if (drawerBackdrop) drawerBackdrop.addEventListener('click', closeDrawer);
    const drawerClose = $('#template-drawer .drawer-close');
    if (drawerClose) drawerClose.addEventListener('click', closeDrawer);

    // Template cards
    document.addEventListener('click', e => {
      const card = e.target.closest('.template-card');
      if (card) requestSelectTemplate(card.dataset.slug);
    });

    // Clear pill
    const clearBtn = $('.template-pill-clear');
    if (clearBtn) {
      clearBtn.addEventListener('click', e => {
        e.preventDefault();
        clearSelection();
      });
    }

    // Insert confirmation
    document.addEventListener('click', e => {
      const btn = e.target.closest('.insert-confirm-btn');
      if (!btn) return;
      const action = btn.dataset.action;
      if (action === 'replace' && state.pendingTemplate) {
        applyTemplate(state.pendingTemplate, 'replace');
      } else if (action === 'append' && state.pendingTemplate) {
        applyTemplate(state.pendingTemplate, 'append');
      } else {
        hideInsertConfirm();
      }
    });

    // Escape key
    document.addEventListener('keydown', e => {
      if (e.key === 'Escape') {
        if ($('#insert-confirm')?.style.display !== 'none') {
          hideInsertConfirm();
        } else if ($('#template-drawer')?.classList.contains('open')) {
          closeDrawer();
        }
      }
    });

    // Track manual hostname edits
    const hostnameInput = $('#hostname');
    if (hostnameInput) {
      // Record the initial server-generated hostname so we can detect manual edits
      state.lastGeneratedHostname = hostnameInput.value;
      hostnameInput.addEventListener('input', () => {
        // If the value doesn't match our last generated name, user has touched it
        if (hostnameInput.value !== state.lastGeneratedHostname) {
          state.hostnameTouched = true;
        }
      });
    }

    // If user clears prompt text, clear template selection (only for prompt-based templates)
    const promptInput = $('#prompt');
    if (promptInput) {
      promptInput.addEventListener('input', () => {
        if (state.selectedTemplate && !isImageTemplate(state.selectedTemplate) && promptInput.value.trim() === '') {
          state.selectedTemplate = null;
          hideTemplatePill();
          renderTemplateSections();
        }
      });
    }
  }

  // --- Utils ---
  function escHtml(str) {
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
