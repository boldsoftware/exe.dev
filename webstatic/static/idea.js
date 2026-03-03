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

  const config = window.__IDEA_CONFIG__ || {};
  let ideas = [];
  // Map of template_id -> user's rating (1-5), populated for logged-in users.
  let myRatings = {};
  let currentSlug = null;
  // True when the modal was opened by pushing a history entry (card click).
  // False when the page loaded with a slug already in the URL.
  let modalPushedState = false;

  function getSlugFromPath() {
    const m = location.pathname.match(/^\/idea\/([a-z0-9][a-z0-9-]+[a-z0-9])$/);
    return m ? m[1] : null;
  }

  async function init() {
    const fetches = [fetch('/api/ideas').then(r => r.ok ? r.json() : [])];
    if (config.canRate) {
      fetches.push(fetch('/api/ideas/my-ratings').then(r => r.ok ? r.json() : {}));
    }

    try {
      const results = await Promise.all(fetches);
      ideas = results[0] || [];
      if (results[1]) {
        // API returns string keys from JSON; convert to int.
        const raw = results[1];
        for (const k of Object.keys(raw)) {
          myRatings[parseInt(k, 10)] = raw[k];
        }
      }
    } catch (e) {
      // Ideas unavailable
    }
    render(ideas);
    bindEvents();

    // Open idea from URL path if present (direct navigation to /idea/<slug>).
    // Replace the current history entry with /idea, then push the slug on top
    // so browser-back closes the modal and lands on /idea.
    const slugFromPath = getSlugFromPath();
    if (slugFromPath) {
      history.replaceState(null, '', '/idea');
      openModal(slugFromPath);
    }
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

  function populateModal(t) {
    const icon = t.icon_url || '\uD83D\uDCE6';
    const isEmoji = !icon.startsWith('http') && !icon.startsWith('/');
    $('#modal-icon').innerHTML = isEmoji ? icon : `<img src="${esc(icon)}" style="width:40px;height:40px;border-radius:8px;">`;
    $('#modal-title').textContent = t.title;
    $('#modal-desc').textContent = t.short_description;
    $('#modal-category').textContent = categoryLabels[t.category] || t.category;

    // Show prompt or image section
    const promptSection = $('#modal-prompt-section');
    const imageSection = $('#modal-image-section');
    if (t.prompt) {
      promptSection.style.display = '';
      imageSection.style.display = 'none';
      $('#modal-prompt').value = t.prompt;
    } else if (t.image) {
      promptSection.style.display = 'none';
      imageSection.style.display = '';
      $('#modal-image').textContent = t.image;
    } else {
      promptSection.style.display = '';
      imageSection.style.display = 'none';
      $('#modal-prompt').value = '';
    }

    // Build /new URL
    const params = new URLSearchParams();
    if (t.prompt) params.set('prompt', t.prompt);
    if (t.image) params.set('image', t.image);
    if (t.vm_shortname) {
      params.set('name', t.vm_shortname + '-' + randomSuffix());
      params.set('idea', t.vm_shortname);
    }
    $('#modal-use-btn').href = '/new?' + params.toString();

    // Rating section
    const ratingSection = $('#modal-rating-section');
    if (config.canRate) {
      ratingSection.style.display = '';
      const userRating = myRatings[t.id] || 0;
      const label = $('#modal-rating-label');
      label.textContent = userRating ? 'You rated this:' : 'Rate this idea';
      renderStars(t.id, userRating);
    } else {
      ratingSection.style.display = 'none';
    }
  }

  function openModal(slug) {
    const t = ideas.find(x => x.slug === slug);
    if (!t) return;

    currentSlug = slug;
    modalPushedState = true;
    populateModal(t);

    // Push a new history entry so browser-back closes the modal.
    history.pushState({ ideaSlug: slug }, '', '/idea/' + slug);

    const modal = $('#idea-modal');
    modal.classList.add('open');
    document.body.style.overflow = 'hidden';
  }

  function renderStars(templateId, userRating) {
    const container = $('#modal-stars');
    let html = '';
    for (let i = 1; i <= 5; i++) {
      const filled = i <= userRating ? 'filled' : '';
      html += `<button type="button" class="idea-modal-star-btn ${filled}" data-template-id="${templateId}" data-rating="${i}">\u2605</button>`;
    }
    container.innerHTML = html;
  }

  async function submitRating(templateId, rating) {
    try {
      const res = await fetch('/api/ideas/rate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ template_id: templateId, rating: rating }),
      });
      if (!res.ok) return;
      const data = await res.json();

      // Update local state
      myRatings[templateId] = rating;
      const t = ideas.find(x => x.id === templateId);
      if (t) {
        t.avg_rating = data.avg_rating;
        t.rating_count = data.rating_count;
      }

      // Update modal stars and label
      renderStars(templateId, rating);
      const label = $('#modal-rating-label');
      if (label) label.textContent = 'You rated this:';

      // Re-render cards so the grid reflects the new average
      const searchInput = $('#idea-search');
      const query = searchInput ? searchInput.value.trim() : '';
      filterIdeas(query);
    } catch (e) {
      // Rating failed silently
    }
  }

  function closeModal() {
    if (!currentSlug) return;
    currentSlug = null;

    const modal = $('#idea-modal');
    modal.classList.remove('open');
    document.body.style.overflow = '';

    // Pop the history entry that openModal pushed.
    if (modalPushedState) {
      modalPushedState = false;
      history.back();
    }
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

  function slugify(str) {
    return str.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '').slice(0, 64);
  }

  function openSubmitModal() {
    const modal = $('#idea-submit-modal');
    // Reset form state
    const form = $('#idea-submit-form');
    if (form) form.reset();
    $('#submit-error').hidden = true;
    $('#submit-success').hidden = true;
    $('#submit-btn').disabled = false;
    modal.classList.add('open');
    document.body.style.overflow = 'hidden';
    const titleInput = $('#submit-title');
    if (titleInput) titleInput.focus();
  }

  function closeSubmitModal() {
    const modal = $('#idea-submit-modal');
    modal.classList.remove('open');
    document.body.style.overflow = '';
  }

  async function handleSubmit(e) {
    e.preventDefault();
    const errEl = $('#submit-error');
    const successEl = $('#submit-success');
    const btn = $('#submit-btn');
    errEl.hidden = true;
    successEl.hidden = true;

    const title = $('#submit-title').value.trim();
    const desc = $('#submit-desc').value.trim();
    const category = $('#submit-category').value;
    const prompt = $('#submit-prompt').value.trim();
    const slug = slugify(title);

    if (!title || !prompt) {
      errEl.textContent = 'Title and prompt are required.';
      errEl.hidden = false;
      return;
    }
    if (slug.length < 3) {
      errEl.textContent = 'Title must produce a valid slug (at least 3 characters).';
      errEl.hidden = false;
      return;
    }

    btn.disabled = true;
    btn.textContent = 'Submitting...';

    try {
      const res = await fetch('/api/ideas/submit', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title, slug, short_description: desc, category, prompt }),
      });
      if (!res.ok) {
        const text = await res.text();
        throw new Error(text || 'Submission failed');
      }
      successEl.hidden = false;
      btn.textContent = 'Submitted!';
      // Close after a moment
      setTimeout(closeSubmitModal, 1500);
    } catch (err) {
      errEl.textContent = err.message;
      errEl.hidden = false;
      btn.disabled = false;
      btn.textContent = 'Submit';
    }
  }

  function bindEvents() {
    // Card clicks
    document.addEventListener('click', e => {
      const card = e.target.closest('.idea-card');
      if (card) {
        openModal(card.dataset.slug);
        return;
      }

      // Star rating clicks
      const starBtn = e.target.closest('.idea-modal-star-btn');
      if (starBtn) {
        const templateId = parseInt(starBtn.dataset.templateId, 10);
        const rating = parseInt(starBtn.dataset.rating, 10);
        submitRating(templateId, rating);
        return;
      }
    });

    // Detail modal close
    const backdrop = $('#idea-modal .idea-modal-backdrop');
    if (backdrop) backdrop.addEventListener('click', closeModal);
    const closeBtn = $('#idea-modal .idea-modal-close');
    if (closeBtn) closeBtn.addEventListener('click', closeModal);

    // Escape key
    document.addEventListener('keydown', e => {
      if (e.key === 'Escape') {
        // Close submit modal if open
        const submitModal = $('#idea-submit-modal');
        if (submitModal && submitModal.classList.contains('open')) {
          closeSubmitModal();
          return;
        }
        // Close search if open, otherwise close detail modal
        const searchWrap = $('#idea-search-wrap');
        if (searchWrap && !searchWrap.hidden) {
          searchWrap.hidden = true;
          $('#idea-search-toggle').style.display = '';
          $('#idea-search').value = '';
          filterIdeas('');
          return;
        }
        closeModal();
      }
    });

    // Search toggle
    const searchToggle = $('#idea-search-toggle');
    const searchWrap = $('#idea-search-wrap');
    const searchInput = $('#idea-search');
    if (searchToggle && searchWrap && searchInput) {
      searchToggle.addEventListener('click', () => {
        searchWrap.hidden = false;
        searchToggle.style.display = 'none';
        searchInput.focus();
      });

      let debounce;
      searchInput.addEventListener('input', () => {
        clearTimeout(debounce);
        debounce = setTimeout(() => filterIdeas(searchInput.value.trim()), 150);
      });
    }

    // Browser back/forward
    window.addEventListener('popstate', e => {
      const slug = getSlugFromPath();
      if (slug) {
        // Forward navigation into a modal
        const t = ideas.find(x => x.slug === slug);
        if (t) {
          currentSlug = slug;
          modalPushedState = false; // arrived via popstate, not pushState
          populateModal(t);
          const modal = $('#idea-modal');
          modal.classList.add('open');
          document.body.style.overflow = 'hidden';
        }
      } else {
        // Back navigation out of a modal
        const modal = $('#idea-modal');
        modal.classList.remove('open');
        document.body.style.overflow = '';
        currentSlug = null;
        modalPushedState = false;
      }
    });

    // Submit idea
    const submitLink = $('#idea-submit-link');
    if (submitLink) {
      submitLink.addEventListener('click', () => {
        if (!config.isLoggedIn) {
          window.location.href = '/auth?redirect=/idea';
          return;
        }
        openSubmitModal();
      });
    }

    // Submit modal close
    const submitBackdrop = $('#idea-submit-modal .idea-modal-backdrop');
    if (submitBackdrop) submitBackdrop.addEventListener('click', closeSubmitModal);
    const submitClose = $('.idea-submit-close');
    if (submitClose) submitClose.addEventListener('click', closeSubmitModal);

    // Submit form
    const submitForm = $('#idea-submit-form');
    if (submitForm) {
      submitForm.addEventListener('submit', handleSubmit);
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
