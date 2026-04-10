// Lightweight terminal replay player
(function () {
  'use strict';

  // ── SVG icons ──
  var ICON_PLAY = '<svg viewBox="0 0 16 16" fill="currentColor"><path d="M4 2.5v11l9-5.5z"/></svg>';
  var ICON_PAUSE = '<svg viewBox="0 0 16 16" fill="currentColor"><path d="M4 2h3v12H4zm5 0h3v12H9z"/></svg>';
  var ICON_RESTART = '<svg viewBox="0 0 16 16" fill="currentColor"><path d="M2 8a6 6 0 0 1 10.3-4.2l-1.5 1.5A4 4 0 0 0 4 8a4 4 0 0 0 7.4 2.1l1.6 1.1A6 6 0 0 1 2 8z"/><path d="M12 2v4h-4l1.5-1.5L12 2z"/></svg>';

  // ── Event types ──
  // Each event in a recording is: { t: ms, type: string, ... }
  //   type:'prompt'  -> { text: '$ ' } (renders green prompt)
  //   type:'type'    -> { text: 'ssh exe.dev' } (typed char by char)
  //   type:'output'  -> { text: '...' } (appears instantly)
  //   type:'wait'    -> { ms: 500 } (pause before next event)
  //   type:'clear'   -> {} (clears screen)

  function TermPlayer(container, recordings) {
    this.container = container;
    this.recordings = recordings;
    this.activeTab = 0;
    this.events = [];
    this.eventIndex = 0;
    this.charIndex = 0;
    this.playing = false;
    this.timer = null;
    this.elapsed = 0;
    this.totalDuration = 0;
    this.lines = '';
    this.tickInterval = null;
    this.startTime = 0;
    this.pauseElapsed = 0;
    this.build();
    this.switchTab(0);
  }

  TermPlayer.prototype.build = function () {
    var self = this;
    var el = this.container;
    el.classList.add('term-player');

    // Title bar
    var titlebar = document.createElement('div');
    titlebar.className = 'tp-titlebar';
    titlebar.innerHTML =
      '<div class="tp-dots">' +
      '<span class="tp-dot tp-dot-close"></span>' +
      '<span class="tp-dot tp-dot-min"></span>' +
      '<span class="tp-dot tp-dot-max"></span>' +
      '</div>' +
      '<span class="tp-title"><img src="/static/exy.png" alt="" class="tp-title-logo">exe.dev</span>';
    el.appendChild(titlebar);

    // Tabs
    var tabs = document.createElement('div');
    tabs.className = 'tp-tabs';
    for (var i = 0; i < this.recordings.length; i++) {
      var btn = document.createElement('button');
      btn.className = 'tp-tab';
      var fullSpan = document.createElement('span');
      fullSpan.className = 'tp-tab-full';
      fullSpan.textContent = this.recordings[i].title;
      var shortSpan = document.createElement('span');
      shortSpan.className = 'tp-tab-short';
      shortSpan.textContent = this.recordings[i].shortTitle || this.recordings[i].title;
      btn.appendChild(fullSpan);
      btn.appendChild(shortSpan);
      btn.setAttribute('data-tab', i);
      btn.addEventListener('click', (function (idx) {
        return function () { self.switchTab(idx); };
      })(i));
      tabs.appendChild(btn);
    }
    el.appendChild(tabs);
    this.tabsEl = tabs;

    // Screen
    var screen = document.createElement('div');
    screen.className = 'tp-screen';
    var linesEl = document.createElement('div');
    linesEl.className = 'tp-lines';
    screen.appendChild(linesEl);
    el.appendChild(screen);
    this.screenEl = screen;
    this.linesEl = linesEl;

    // Controls
    var controls = document.createElement('div');
    controls.className = 'tp-controls';

    this.playBtn = document.createElement('button');
    this.playBtn.className = 'tp-btn tp-play-btn';
    this.playBtn.innerHTML = ICON_PLAY;
    this.playBtn.addEventListener('click', function () { self.togglePlay(); });
    controls.appendChild(this.playBtn);

    this.restartBtn = document.createElement('button');
    this.restartBtn.className = 'tp-btn tp-restart-btn';
    this.restartBtn.innerHTML = ICON_RESTART;
    this.restartBtn.addEventListener('click', function () { self.restart(); });
    controls.appendChild(this.restartBtn);

    var progress = document.createElement('div');
    progress.className = 'tp-progress';
    this.progressFill = document.createElement('div');
    this.progressFill.className = 'tp-progress-fill';
    this.progressFill.style.width = '0%';
    progress.appendChild(this.progressFill);
    progress.addEventListener('click', function (e) {
      var rect = progress.getBoundingClientRect();
      var pct = (e.clientX - rect.left) / rect.width;
      self.seekTo(pct);
    });
    controls.appendChild(progress);

    el.appendChild(controls);
  };

  TermPlayer.prototype.switchTab = function (idx) {
    if (idx === this.activeTab && this.events.length > 0) return;
    this.stop();
    this.activeTab = idx;

    // Update tab highlight
    var btns = this.tabsEl.querySelectorAll('.tp-tab');
    for (var i = 0; i < btns.length; i++) {
      btns[i].classList.toggle('active', i === idx);
    }

    var rec = this.recordings[idx];
    this.events = rec.events || [];
    this.totalDuration = this.calcDuration();
    this.reset();
    this.play();
  };

  TermPlayer.prototype.calcDuration = function () {
    var d = 0;
    for (var i = 0; i < this.events.length; i++) {
      var ev = this.events[i];
      d += ev.t || 0;
      if (ev.type === 'type') d += (ev.text || '').length * 45;
      if (ev.type === 'wait') d += ev.ms || 0;
    }
    return d;
  };

  TermPlayer.prototype.reset = function () {
    this.eventIndex = 0;
    this.charIndex = 0;
    this.elapsed = 0;
    this.pauseElapsed = 0;
    this.lines = '';
    this.linesEl.innerHTML = '';
    this.updateProgress();
    this.playBtn.innerHTML = ICON_PLAY;
  };

  TermPlayer.prototype.play = function () {
    if (this.playing) return;
    this.playing = true;
    this.playBtn.innerHTML = ICON_PAUSE;
    this.startTime = Date.now() - this.pauseElapsed;
    this.scheduleNext();
    var self = this;
    this.tickInterval = setInterval(function () { self.updateProgress(); }, 100);
  };

  TermPlayer.prototype.pause = function () {
    if (!this.playing) return;
    this.playing = false;
    this.playBtn.innerHTML = ICON_PLAY;
    this.pauseElapsed = Date.now() - this.startTime;
    if (this.timer) { clearTimeout(this.timer); this.timer = null; }
    if (this.tickInterval) { clearInterval(this.tickInterval); this.tickInterval = null; }
  };

  TermPlayer.prototype.stop = function () {
    this.pause();
    this.reset();
  };

  TermPlayer.prototype.togglePlay = function () {
    if (this.eventIndex >= this.events.length && !this.playing) {
      this.restart();
      return;
    }
    if (this.playing) this.pause();
    else this.play();
  };

  TermPlayer.prototype.restart = function () {
    this.stop();
    this.play();
  };

  TermPlayer.prototype.scheduleNext = function () {
    if (!this.playing) return;
    if (this.eventIndex >= this.events.length) {
      // Done
      this.playing = false;
      this.playBtn.innerHTML = ICON_PLAY;
      if (this.tickInterval) { clearInterval(this.tickInterval); this.tickInterval = null; }
      this.updateProgress();
      return;
    }

    var ev = this.events[this.eventIndex];
    var self = this;

    // Handle typing char by char
    if (ev.type === 'type' && this.charIndex < (ev.text || '').length) {
      var delay = this.charIndex === 0 ? (ev.t || 0) : 45;
      this.timer = setTimeout(function () {
        if (!self.playing) return;
        self.appendChar(ev.text[self.charIndex]);
        self.charIndex++;
        self.pauseElapsed = Date.now() - self.startTime;
        self.scheduleNext();
      }, delay);
      return;
    }

    var eventDelay = ev.t || 0;
    if (ev.type === 'wait') eventDelay = ev.ms || 0;

    this.timer = setTimeout(function () {
      if (!self.playing) return;
      self.executeEvent(ev);
      self.eventIndex++;
      self.charIndex = 0;
      self.pauseElapsed = Date.now() - self.startTime;
      self.scheduleNext();
    }, eventDelay);
  };

  TermPlayer.prototype.executeEvent = function (ev) {
    switch (ev.type) {
      case 'prompt':
        this.appendHTML('<span class="tp-prompt-text">' + esc(ev.text || '$ ') + '</span>');
        this.showCursor();
        break;
      case 'type':
        // Already handled char by char; just move past
        break;
      case 'enter':
        this.hideCursor();
        this.appendHTML('\n');
        break;
      case 'output':
        var cls = ev.cls || 'tp-output-text';
        this.appendHTML('<span class="' + cls + '">' + esc(ev.text || '') + '</span>');
        break;
      case 'replace-line':
        // Simulates \r\033[K — replace the current (last) line content
        this.replaceLastLine(ev.text || '', ev.cls || 'tp-dim');
        break;
      case 'clear':
        this.lines = '';
        this.linesEl.innerHTML = '';
        break;
      case 'wait':
        break;
    }
    this.scrollToBottom();
  };

  TermPlayer.prototype.replaceLastLine = function (text, cls) {
    // Find the last newline in the rendered content and replace everything after it
    var html = this.linesEl.innerHTML;
    var lastNL = html.lastIndexOf('\n');
    if (lastNL === -1) {
      // No newline yet, replace everything
      this.linesEl.innerHTML = '';
    } else {
      this.linesEl.innerHTML = html.substring(0, lastNL + 1);
    }
    if (text) {
      this.appendHTML('<span class="' + cls + '">' + esc(text) + '</span>');
    }
  };

  TermPlayer.prototype.appendChar = function (ch) {
    // Insert before cursor
    var cursor = this.linesEl.querySelector('.tp-cursor');
    var span = document.createElement('span');
    span.className = 'tp-cmd-text';
    span.textContent = ch;
    if (cursor) {
      cursor.parentNode.insertBefore(span, cursor);
    } else {
      this.linesEl.appendChild(span);
    }
    this.scrollToBottom();
  };

  TermPlayer.prototype.appendHTML = function (html) {
    // Remove cursor first if present
    var cursor = this.linesEl.querySelector('.tp-cursor');
    if (cursor) cursor.remove();
    this.linesEl.insertAdjacentHTML('beforeend', html);
  };

  TermPlayer.prototype.showCursor = function () {
    var existing = this.linesEl.querySelector('.tp-cursor');
    if (!existing) {
      this.linesEl.insertAdjacentHTML('beforeend', '<span class="tp-cursor"></span>');
    }
  };

  TermPlayer.prototype.hideCursor = function () {
    var cursor = this.linesEl.querySelector('.tp-cursor');
    if (cursor) cursor.remove();
  };

  TermPlayer.prototype.scrollToBottom = function () {
    this.screenEl.scrollTop = this.screenEl.scrollHeight;
  };

  TermPlayer.prototype.updateProgress = function () {
    if (this.totalDuration <= 0) return;
    var now = this.playing ? (Date.now() - this.startTime) : this.pauseElapsed;
    var pct = Math.min(now / this.totalDuration * 100, 100);
    this.progressFill.style.width = pct + '%';

  };

  TermPlayer.prototype.seekTo = function (pct) {
    // Seek by replaying all events up to the target time
    var target = pct * this.totalDuration;
    var wasPlaying = this.playing;
    this.pause();

    // Reset state
    this.eventIndex = 0;
    this.charIndex = 0;
    this.lines = '';
    this.linesEl.innerHTML = '';

    // Replay events up to target time
    var elapsed = 0;
    for (var i = 0; i < this.events.length; i++) {
      var ev = this.events[i];
      var evDuration = ev.t || 0;
      if (ev.type === 'type') evDuration += (ev.text || '').length * 45;
      if (ev.type === 'wait') evDuration = ev.ms || 0;

      if (elapsed + evDuration > target) {
        this.eventIndex = i;
        // For type events, figure out how many chars we should have typed
        if (ev.type === 'type') {
          var remaining = target - elapsed - (ev.t || 0);
          this.charIndex = Math.max(0, Math.floor(remaining / 45));
          // Execute the prompt and partial type
          this.executeEventInstant(ev, this.charIndex);
          this.showCursor();
        }
        break;
      }

      elapsed += evDuration;
      this.executeEventInstant(ev, -1);
      this.eventIndex = i + 1;
    }

    this.pauseElapsed = target;
    this.updateProgress();
    this.scrollToBottom();

    if (wasPlaying) {
      this.startTime = Date.now() - target;
      this.play();
    }
  };

  TermPlayer.prototype.executeEventInstant = function (ev, charLimit) {
    switch (ev.type) {
      case 'prompt':
        this.appendHTML('<span class="tp-prompt-text">' + esc(ev.text || '$ ') + '</span>');
        break;
      case 'type':
        var text = ev.text || '';
        if (charLimit >= 0) text = text.slice(0, charLimit);
        this.appendHTML('<span class="tp-cmd-text">' + esc(text) + '</span>');
        break;
      case 'enter':
        this.hideCursor();
        this.appendHTML('\n');
        break;
      case 'output':
        var cls = ev.cls || 'tp-output-text';
        this.appendHTML('<span class="' + cls + '">' + esc(ev.text || '') + '</span>');
        break;
      case 'replace-line':
        this.replaceLastLine(ev.text || '', ev.cls || 'tp-dim');
        break;
      case 'clear':
        this.linesEl.innerHTML = '';
        break;
    }
  };

  function esc(str) {
    return str.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  // ── Recording definitions ──
  // Prompt matches the real exe.dev REPL: cyan hostname, white ▶
  var REPL_PROMPT = 'exe.dev ▶ ';

  var recordings = [
    {
      title: 'launch openclaw', shortTitle: 'openclaw',
      events: [
        // User SSHs in from their local terminal
        { t: 300, type: 'prompt', text: '$ ' },
        { t: 200, type: 'type', text: 'ssh exe.dev' },
        { t: 400, type: 'enter' },
        { t: 0, type: 'output', text: '\n' },
        // exe.dev REPL prompt appears immediately
        { t: 0, type: 'prompt', text: REPL_PROMPT },
        // User types the new command with --prompt
        { t: 400, type: 'type', text: 'new --name my-openclaw --prompt "Install OpenClaw \uD83E\uDD9E. exe.dev handles auth"' },
        { t: 400, type: 'enter' },
        { t: 300, type: 'output', text: '\n' },
        // Creation output (matches real exed format)
        { t: 100, type: 'output', text: 'Creating ' },
        { t: 0,   type: 'output', text: 'my-openclaw', cls: 'tp-cmd-text tp-bold' },
        { t: 0,   type: 'output', text: ' using image ' },
        { t: 0,   type: 'output', text: 'boldsoftware/exeuntu', cls: 'tp-cmd-text tp-bold' },
        { t: 0,   type: 'output', text: '...\n' },
        // Spinner progress (replace-line simulates \r\033[K)
        { t: 200,  type: 'replace-line', text: '\u280b 0.2s Initializing...', cls: 'tp-dim' },
        { t: 200,  type: 'replace-line', text: '\u2839 0.4s Starting VM...', cls: 'tp-dim' },
        { t: 300,  type: 'replace-line', text: '\u283c 0.7s Configuring SSH...', cls: 'tp-dim' },
        // Clear spinner, show result
        { t: 200,  type: 'replace-line', text: '' },
        { t: 0,    type: 'output', text: 'Ready in 0.9s!\n', cls: 'tp-cmd-text tp-bold' },
        { t: 200,  type: 'output', text: '\n' },
        // Services list (matches real interactive output)
        { t: 0,    type: 'output', text: 'Coding agent\n', cls: 'tp-cmd-text tp-bold' },
        { t: 0,    type: 'output', text: 'https://my-openclaw.shelley.exe.xyz\n', cls: 'tp-url-text' },
        { t: 150,  type: 'output', text: '\n' },
        { t: 0,    type: 'output', text: 'App', cls: 'tp-cmd-text tp-bold' },
        { t: 0,    type: 'output', text: ' (HTTPS proxy \u2192 :8000)\n', cls: 'tp-dim' },
        { t: 0,    type: 'output', text: 'https://my-openclaw.exe.xyz\n', cls: 'tp-url-text' },
        { t: 150,  type: 'output', text: '\n' },
        { t: 0,    type: 'output', text: 'SSH\n', cls: 'tp-cmd-text tp-bold' },
        { t: 0,    type: 'output', text: 'ssh my-openclaw.exe.dev\n', cls: 'tp-url-text' },
        { t: 200,  type: 'output', text: '\n' },
        { t: 0,    type: 'prompt', text: REPL_PROMPT },
        { t: 0,    type: 'wait', ms: 800 },
      ],
    },
    {
      title: 'start a blog', shortTitle: 'blog',
      events: [
        // SSH into exe.dev
        { t: 300, type: 'prompt', text: '$ ' },
        { t: 200, type: 'type', text: 'ssh exe.dev' },
        { t: 400, type: 'enter' },
        { t: 0, type: 'output', text: '\n' },
        // Create a VM with auto-generated name
        { t: 0, type: 'prompt', text: REPL_PROMPT },
        { t: 400, type: 'type', text: 'new' },
        { t: 300, type: 'enter' },
        { t: 200, type: 'output', text: '\n' },
        { t: 100, type: 'output', text: 'Creating ' },
        { t: 0,   type: 'output', text: 'fun-sphinx', cls: 'tp-cmd-text tp-bold' },
        { t: 0,   type: 'output', text: ' using image ' },
        { t: 0,   type: 'output', text: 'boldsoftware/exeuntu', cls: 'tp-cmd-text tp-bold' },
        { t: 0,   type: 'output', text: '...\n' },
        { t: 200,  type: 'replace-line', text: '\u280b 0.2s Initializing...', cls: 'tp-dim' },
        { t: 200,  type: 'replace-line', text: '\u2839 0.4s Starting VM...', cls: 'tp-dim' },
        { t: 300,  type: 'replace-line', text: '\u283c 0.7s Configuring SSH...', cls: 'tp-dim' },
        { t: 200,  type: 'replace-line', text: '' },
        { t: 0,    type: 'output', text: 'Ready in 0.9s!\n', cls: 'tp-cmd-text tp-bold' },
        { t: 200,  type: 'output', text: '\n' },
        { t: 0,    type: 'output', text: 'Coding agent\n', cls: 'tp-cmd-text tp-bold' },
        { t: 0,    type: 'output', text: 'https://fun-sphinx.shelley.exe.xyz\n', cls: 'tp-url-text' },
        { t: 100,  type: 'output', text: '\n' },
        { t: 0,    type: 'output', text: 'App', cls: 'tp-cmd-text tp-bold' },
        { t: 0,    type: 'output', text: ' (HTTPS proxy \u2192 :8000)\n', cls: 'tp-dim' },
        { t: 0,    type: 'output', text: 'https://fun-sphinx.exe.xyz\n', cls: 'tp-url-text' },
        { t: 100,  type: 'output', text: '\n' },
        { t: 0,    type: 'output', text: 'SSH\n', cls: 'tp-cmd-text tp-bold' },
        { t: 0,    type: 'output', text: 'ssh fun-sphinx.exe.dev\n', cls: 'tp-url-text' },
        { t: 300,  type: 'output', text: '\n' },
        // Quit the lobby
        { t: 0, type: 'prompt', text: REPL_PROMPT },
        { t: 300, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: 'Goodbye!\n' },
        { t: 200, type: 'output', text: '\n' },
        // Back at local shell — rsync blog to nginx doc root
        { t: 0, type: 'prompt', text: '$ ' },
        { t: 300, type: 'type', text: 'rsync -r blog/ fun-sphinx.exe.dev:/var/www/html/' },
        { t: 400, type: 'enter' },
        { t: 600, type: 'output', text: '\n' },
        // SSH into the new VM to enable nginx
        { t: 0, type: 'prompt', text: '$ ' },
        { t: 300, type: 'type', text: 'ssh fun-sphinx.exe.dev -- sudo systemctl enable --now nginx' },
        { t: 400, type: 'enter' },
        { t: 500, type: 'output', text: '\n' },
        // Make the site public via exe.dev
        { t: 0, type: 'prompt', text: '$ ' },
        { t: 300, type: 'type', text: 'ssh exe.dev share set-public fun-sphinx' },
        { t: 400, type: 'enter' },
        { t: 300, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: '\u2713 Route updated successfully\n', cls: 'tp-prompt-text' },
        { t: 0, type: 'output', text: '  Port: 80\n', cls: 'tp-output-text' },
        { t: 0, type: 'output', text: '  Share: public\n', cls: 'tp-output-text' },
        { t: 200, type: 'output', text: '\n' },
        // Curl the public URL
        { t: 0, type: 'prompt', text: '$ ' },
        { t: 300, type: 'type', text: 'curl https://fun-sphinx.exe.xyz' },
        { t: 400, type: 'enter' },
        { t: 400, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: '<html><body>welcome to the team blog\n', cls: 'tp-cmd-text' },
        { t: 100, type: 'output', text: '\n' },
        { t: 0, type: 'prompt', text: '$ ' },
        { t: 0, type: 'wait', ms: 800 },
      ],
    },
    {
      title: 'create a team', shortTitle: 'team',
      events: [
        // SSH into exe.dev
        { t: 300, type: 'prompt', text: '$ ' },
        { t: 200, type: 'type', text: 'ssh exe.dev' },
        { t: 400, type: 'enter' },
        { t: 0, type: 'output', text: '\n' },
        // Enable teams
        { t: 0, type: 'prompt', text: REPL_PROMPT },
        { t: 400, type: 'type', text: 'team enable' },
        { t: 300, type: 'enter' },
        { t: 200, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: 'Teams lets you:\n' },
        { t: 0, type: 'output', text: '  - Manage shared billing for your organization\n' },
        { t: 0, type: 'output', text: '  - Invite members and control access\n' },
        { t: 0, type: 'output', text: '  - Share VMs across your team\n' },
        { t: 100, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: 'Enable teams? (yes/no): ' },
        { t: 400, type: 'type', text: 'yes' },
        { t: 300, type: 'enter' },
        { t: 0, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: 'Team name: ' },
        { t: 300, type: 'type', text: 'Acme' },
        { t: 300, type: 'enter' },
        { t: 200, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: 'Team ' },
        { t: 0, type: 'output', text: 'Acme', cls: 'tp-cmd-text tp-bold' },
        { t: 0, type: 'output', text: ' created!\n' },
        { t: 0, type: 'output', text: 'Use ' },
        { t: 0, type: 'output', text: 'team add <email>', cls: 'tp-cmd-text tp-bold' },
        { t: 0, type: 'output', text: ' to invite members.\n' },
        { t: 200, type: 'output', text: '\n' },
        // Invite a colleague
        { t: 0, type: 'prompt', text: REPL_PROMPT },
        { t: 400, type: 'type', text: 'team add alice@acme.com' },
        { t: 300, type: 'enter' },
        { t: 300, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: 'Invited alice@acme.com to the team\n' },
        { t: 200, type: 'output', text: '\n' },
        // Share the blog VM with the team
        { t: 0, type: 'prompt', text: REPL_PROMPT },
        { t: 400, type: 'type', text: 'share add fun-sphinx team' },
        { t: 300, type: 'enter' },
        { t: 300, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: '\u2713 Shared fun-sphinx with team Acme\n', cls: 'tp-prompt-text' },
        { t: 200, type: 'output', text: '\n' },
        { t: 0, type: 'prompt', text: REPL_PROMPT },
        { t: 0, type: 'wait', ms: 800 },
      ],
    },
    {
      title: 'clone',
      events: [
        // SSH into exe.dev
        { t: 300, type: 'prompt', text: '$ ' },
        { t: 200, type: 'type', text: 'ssh exe.dev' },
        { t: 400, type: 'enter' },
        { t: 0, type: 'output', text: '\n' },
        // Clone the openclaw VM
        { t: 0, type: 'prompt', text: REPL_PROMPT },
        { t: 400, type: 'type', text: 'cp my-openclaw' },
        { t: 300, type: 'enter' },
        { t: 200, type: 'output', text: '\n' },
        // Spinner
        { t: 200,  type: 'replace-line', text: '\u280b 0.2s Snapshotting...', cls: 'tp-dim' },
        { t: 300,  type: 'replace-line', text: '\u2839 0.5s Cloning...', cls: 'tp-dim' },
        { t: 300,  type: 'replace-line', text: '\u283c 0.8s Starting VM...', cls: 'tp-dim' },
        { t: 200,  type: 'replace-line', text: '' },
        // Result
        { t: 0, type: 'output', text: 'Created ' },
        { t: 0, type: 'output', text: 'peak-dragon', cls: 'tp-cmd-text tp-bold' },
        { t: 0, type: 'output', text: ' from ' },
        { t: 0, type: 'output', text: 'my-openclaw', cls: 'tp-cmd-text tp-bold' },
        { t: 0, type: 'output', text: ' in 1.0s\n' },
        { t: 150, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: 'https://peak-dragon.exe.xyz\n', cls: 'tp-url-text' },
        { t: 150, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: 'ssh ', cls: 'tp-url-text' },
        { t: 0, type: 'output', text: 'peak-dragon.exe.dev', cls: 'tp-url-text tp-bold' },
        { t: 0, type: 'output', text: '\n' },
        { t: 200, type: 'output', text: '\n' },
        { t: 0, type: 'prompt', text: REPL_PROMPT },
        { t: 0, type: 'wait', ms: 800 },
      ],
    },
    {
      title: 'share',
      events: [
        // SSH into exe.dev
        { t: 300, type: 'prompt', text: '$ ' },
        { t: 200, type: 'type', text: 'ssh exe.dev' },
        { t: 400, type: 'enter' },
        { t: 0, type: 'output', text: '\n' },
        // Share the VM with a friend
        { t: 0, type: 'prompt', text: REPL_PROMPT },
        { t: 400, type: 'type', text: 'share add my-openclaw alice@example.com' },
        { t: 300, type: 'enter' },
        { t: 300, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: '\u2713 Invitation sent to alice@example.com\n', cls: 'tp-prompt-text' },
        { t: 100, type: 'output', text: '\n' },
        { t: 0, type: 'output', text: 'alice@example.com will receive an email with access instructions.\n' },
        { t: 0, type: 'output', text: 'You can also share this URL directly: ' },
        { t: 0, type: 'output', text: 'https://my-openclaw.exe.xyz\n', cls: 'tp-url-text' },
        { t: 0, type: 'output', text: '(They\u2019ll need to log in with alice@example.com to access it)\n', cls: 'tp-dim' },
        { t: 200, type: 'output', text: '\n' },
        { t: 0, type: 'prompt', text: REPL_PROMPT },
        { t: 0, type: 'wait', ms: 800 },
      ],
    },
  ];

  function init() {
    var el = document.getElementById('term-player');
    if (!el) return;
    new TermPlayer(el, recordings);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
