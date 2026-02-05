// List of 100 color names for session identification
const colorNames = [
    'amber', 'aqua', 'azure', 'beige', 'bisque', 'black', 'blue', 'bronze', 'brown', 'coral',
    'cream', 'crimson', 'cyan', 'denim', 'ebony', 'emerald', 'fuchsia', 'gold', 'gray', 'green',
    'indigo', 'ivory', 'jade', 'khaki', 'lavender', 'lemon', 'lilac', 'lime', 'linen', 'magenta',
    'maroon', 'mauve', 'mint', 'navy', 'ochre', 'olive', 'orange', 'orchid', 'peach', 'pearl',
    'periwinkle', 'pink', 'plum', 'purple', 'red', 'rose', 'ruby', 'rust', 'saffron', 'sage',
    'salmon', 'sand', 'sapphire', 'scarlet', 'sepia', 'silver', 'slate', 'snow', 'tan', 'teal',
    'thistle', 'tomato', 'turquoise', 'umber', 'vanilla', 'violet', 'wheat', 'white', 'wine', 'yellow',
    'amethyst', 'apricot', 'ash', 'auburn', 'buff', 'cardinal', 'carmine', 'cerise', 'charcoal', 'cherry',
    'chestnut', 'chocolate', 'cinnamon', 'claret', 'cobalt', 'copper', 'cornflower', 'eggplant', 'flax', 'garnet',
    'ginger', 'goldenrod', 'gunmetal', 'hazel', 'honeydew', 'ice', 'iron', 'jasmine', 'jet', 'mango'
];

function getRandomColorName() {
    return colorNames[Math.floor(Math.random() * colorNames.length)];
}

function getOrSetSessionName() {
    const params = new URLSearchParams(window.location.search);
    let name = params.get('name');
    
    if (!name) {
        // Generate a random color name and add to URL
        name = getRandomColorName();
        params.set('name', name);
        const newUrl = window.location.pathname + '?' + params.toString();
        window.history.replaceState({}, '', newUrl);
    }
    
    return name;
}

class TerminalClient {
    constructor() {
        this.terminal = null;
        this.ws = null;
        this.sessionName = getOrSetSessionName();
        this.terminalId = this.getTerminalId();
        this.themeManager = new ThemeManager();
        this.boxName = this.getBoxName();
        this.connected = false;
        this.retryCount = 0;
        this.maxRetries = 5;
    }

    getTerminalId() {
        const urlParams = new URLSearchParams(window.location.search);
        return urlParams.get('t') || '1';
    }

    getBoxName() {
        const hostname = window.location.hostname;
        // Extract box name from hostname (box.xterm.exe.cloud or box.xterm.exe.xyz)
        const parts = hostname.split('.');
        if (parts.length >= 3) {
            return parts[0];
        }
        return null;
    }

    updatePageTitle() {
        const titleEl = document.getElementById('page-title');
        const headerEl = document.getElementById('terminal-title');
        if (this.boxName) {
            const title = `${this.boxName} - Terminal`;
            if (titleEl) titleEl.textContent = title;
            if (headerEl) headerEl.textContent = title;
        } else {
            if (titleEl) titleEl.textContent = 'Terminal';
            if (headerEl) headerEl.textContent = 'Terminal';
        }
    }

    init() {
        this.updatePageTitle();
        
        // Modifier state for control bar
        this.ctrlActive = false;
        this.altActive = false;
        
        this.terminal = new Terminal({
            cursorBlink: true,
            fontSize: 14,
            fontFamily: 'Consolas, "Liberation Mono", Menlo, Courier, monospace',
            theme: this.themeManager.getTerminalTheme(),
            screenReaderMode: true
        });

        this.terminal.open(document.getElementById('terminal'));
        
        // Enable clickable links
        const webLinksAddon = new WebLinksAddon.WebLinksAddon();
        this.terminal.loadAddon(webLinksAddon);
        
        this.setupEventHandlers();
        this.setupThemeToggle();
        this.setupControlBar();
        this.connect();
        this.fitTerminal();
        
        // Focus the terminal so cursor is active immediately
        this.terminal.focus();
        
        window.addEventListener('resize', () => this.fitTerminal());
    }

    setupThemeToggle() {
        const toggleBtn = document.getElementById('theme-toggle');
        if (toggleBtn) {
            toggleBtn.addEventListener('click', () => {
                this.themeManager.toggle();
                this.terminal.options.theme = this.themeManager.getTerminalTheme();
            });
        }
    }

    setupControlBar() {
        const ctrlBtn = document.getElementById('ctrl-mod');
        const altBtn = document.getElementById('alt-mod');
        const kbToggle = document.getElementById('kb-toggle');
        
        // Handle modifier toggle
        if (ctrlBtn) {
            ctrlBtn.addEventListener('click', (e) => {
                e.preventDefault();
                this.ctrlActive = !this.ctrlActive;
                ctrlBtn.classList.toggle('active', this.ctrlActive);
                ctrlBtn.setAttribute('aria-pressed', this.ctrlActive ? 'true' : 'false');
                this.terminal.focus();
            });
        }
        
        if (altBtn) {
            altBtn.addEventListener('click', (e) => {
                e.preventDefault();
                this.altActive = !this.altActive;
                altBtn.classList.toggle('active', this.altActive);
                altBtn.setAttribute('aria-pressed', this.altActive ? 'true' : 'false');
                this.terminal.focus();
            });
        }
        
        // Handle paste button
        const pasteBtn = document.getElementById('paste-btn');
        if (pasteBtn) {
            pasteBtn.addEventListener('click', async (e) => {
                e.preventDefault();
                try {
                    const text = await navigator.clipboard.readText();
                    if (text && this.connected && this.ws && this.ws.readyState === WebSocket.OPEN) {
                        this.ws.send(JSON.stringify({
                            type: 'input',
                            data: text
                        }));
                    }
                } catch (err) {
                    console.error('Failed to read clipboard:', err);
                }
                this.terminal.focus();
            });
        }

        // Handle keyboard space toggle
        this.keyboardMode = false;
        if (kbToggle) {
            kbToggle.addEventListener('click', (e) => {
                e.preventDefault();
                this.keyboardMode = !this.keyboardMode;
                kbToggle.classList.toggle('active', this.keyboardMode);
                kbToggle.setAttribute('aria-pressed', this.keyboardMode ? 'true' : 'false');
                document.querySelector('.terminal-frame').classList.toggle('keyboard-mode', this.keyboardMode);
                // Small delay to let CSS transition complete before resizing
                setTimeout(() => {
                    this.fitTerminal();
                    this.terminal.focus();
                }, 50);
            });
        }
        
        // Handle control buttons
        const controlBar = document.getElementById('control-bar');
        if (controlBar) {
            controlBar.querySelectorAll('.control-btn:not(.modifier):not(#kb-toggle)').forEach(btn => {
                btn.addEventListener('click', (e) => {
                    e.preventDefault();
                    this.handleControlButton(btn);
                });
            });
        }
    }

    handleControlButton(btn) {
        let data = '';
        const key = btn.dataset.key;
        const ctrl = btn.dataset.ctrl;
        
        if (ctrl) {
            // Ctrl+letter - send the control character
            const charCode = ctrl.toLowerCase().charCodeAt(0) - 96;
            data = String.fromCharCode(charCode);
        } else if (key) {
            // Special keys - apply modifiers if active
            const seq = this.getKeySequence(key, this.ctrlActive, this.altActive);
            data = seq;
        }
        
        if (data && this.connected && this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify({
                type: 'input',
                data: data
            }));
        }
        
        // Reset modifiers after use (one-shot behavior)
        this.resetModifiers();
        this.terminal.focus();
    }

    getKeySequence(key, ctrl, alt) {
        // ANSI escape sequences for special keys
        const sequences = {
            'Escape': '\x1b',
            'Tab': '\t',
            'ArrowUp': '\x1b[A',
            'ArrowDown': '\x1b[B',
            'ArrowRight': '\x1b[C',
            'ArrowLeft': '\x1b[D',
            'Home': '\x1b[H',
            'End': '\x1b[F',
            'PageUp': '\x1b[5~',
            'PageDown': '\x1b[6~',
            'Delete': '\x1b[3~',
        };
        
        let seq = sequences[key] || '';
        
        // Modify sequences for ctrl/alt if needed
        if (seq && (ctrl || alt)) {
            // For arrow keys with modifiers, use CSI u format
            // Format: CSI code ; modifier ~
            // Modifier: 1 + (shift ? 1 : 0) + (alt ? 2 : 0) + (ctrl ? 4 : 0)
            const modifier = 1 + (alt ? 2 : 0) + (ctrl ? 4 : 0);
            if (key.startsWith('Arrow')) {
                const codes = { 'ArrowUp': 'A', 'ArrowDown': 'B', 'ArrowRight': 'C', 'ArrowLeft': 'D' };
                seq = `\x1b[1;${modifier}${codes[key]}`;
            }
        }
        
        return seq;
    }

    resetModifiers() {
        this.ctrlActive = false;
        this.altActive = false;
        const ctrlBtn = document.getElementById('ctrl-mod');
        const altBtn = document.getElementById('alt-mod');
        if (ctrlBtn) {
            ctrlBtn.classList.remove('active');
            ctrlBtn.setAttribute('aria-pressed', 'false');
        }
        if (altBtn) {
            altBtn.classList.remove('active');
            altBtn.setAttribute('aria-pressed', 'false');
        }
    }

    setupEventHandlers() {
        // Intercept keyboard events when modifiers are active
        this.terminal.attachCustomKeyEventHandler((event) => {
            // Only handle keydown for printable characters when modifiers are active
            if (event.type !== 'keydown') return true;
            if (!this.ctrlActive && !this.altActive) return true;
            
            // Check if it's a printable character (a-z)
            if (event.key.length === 1 && event.key.match(/[a-z]/i)) {
                event.preventDefault();
                event.stopPropagation();
                
                let data;
                if (this.ctrlActive) {
                    // Convert to control character (Ctrl+A = 0x01, etc.)
                    data = String.fromCharCode(event.key.toLowerCase().charCodeAt(0) - 96);
                } else if (this.altActive) {
                    // Alt sends ESC + character
                    data = '\x1b' + event.key;
                }
                
                if (data && this.connected && this.ws && this.ws.readyState === WebSocket.OPEN) {
                    this.ws.send(JSON.stringify({
                        type: 'input',
                        data: data
                    }));
                }
                this.resetModifiers();
                return false; // Prevent default handling
            }
            return true;
        });
        
        this.terminal.onData(data => {
            if (this.connected && this.ws && this.ws.readyState === WebSocket.OPEN) {
                this.ws.send(JSON.stringify({
                    type: 'input',
                    data: data
                }));
            }
        });
    }

    connect() {
        this.setStatus('connecting', 'Connecting to terminal...');
        this.removeStatusClickListener();
        
        // Close existing connection
        if (this.ws) {
            this.ws.close();
        }

        // Determine websocket URL with session name
        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const wsUrl = `${protocol}//${window.location.host}/terminal/ws/${this.terminalId}?name=${encodeURIComponent(this.sessionName)}`;
        
        this.ws = new WebSocket(wsUrl);

        this.ws.onopen = () => {
            this.connected = true;
            this.retryCount = 0; // Reset retry counter on successful connection
            this.setStatus('connected', 'Connected');
            setTimeout(() => this.hideStatus(), 2000);
            
            // Send initial terminal size and redraw signal
            this.sendResize();
        };

        this.ws.onmessage = (event) => {
            try {
                const msg = JSON.parse(event.data);
                if (msg.type === 'output' && msg.data) {
                    const data = this.base64ToUint8Array(msg.data);
                    this.terminal.write(data);
                }
            } catch (e) {
                console.error('Error processing message:', e);
            }
        };

        this.ws.onerror = (error) => {
            console.error('WebSocket error:', error);
        };

        this.ws.onclose = () => {
            this.connected = false;
            this.retryCount++;
            
            if (this.retryCount <= this.maxRetries) {
                this.setStatus('error', `Connection lost. Reconnecting... (${this.retryCount}/${this.maxRetries})`);
                setTimeout(() => this.connect(), 2000);
            } else {
                this.setStatus('disconnected', 'Disconnected. Click to reconnect.');
                this.makeStatusClickable();
            }
        };
    }

    setStatus(type, message) {
        const statusEl = document.getElementById('connection-status');
        const textEl = statusEl.querySelector('.status-text');
        statusEl.className = `connection-status ${type}`;
        textEl.textContent = message;
    }

    hideStatus() {
        const statusEl = document.getElementById('connection-status');
        statusEl.style.display = 'none';
    }

    makeStatusClickable() {
        const statusEl = document.getElementById('connection-status');
        statusEl.style.cursor = 'pointer';
        this.statusClickHandler = () => {
            this.retryCount = 0;
            this.connect();
        };
        statusEl.addEventListener('click', this.statusClickHandler);
    }

    removeStatusClickListener() {
        const statusEl = document.getElementById('connection-status');
        statusEl.style.cursor = 'default';
        if (this.statusClickHandler) {
            statusEl.removeEventListener('click', this.statusClickHandler);
            this.statusClickHandler = null;
        }
    }

    sendResize() {
        if (!this.terminal) return;
        
        if (this.connected && this.ws && this.ws.readyState === WebSocket.OPEN) {
            this.ws.send(JSON.stringify({
                type: 'resize',
                cols: this.terminal.cols,
                rows: this.terminal.rows
            }));
        }
    }

    fitTerminal() {
        if (!this.terminal) return;

        const terminalContainer = document.getElementById('terminal');
        const rect = terminalContainer.getBoundingClientRect();
        
        // Check if we're on mobile (control bar visible)
        const controlBar = document.getElementById('control-bar');
        const isMobile = controlBar && window.getComputedStyle(controlBar).display !== 'none';
        
        // Calculate available space
        const width = rect.width - (isMobile ? 8 : 20);
        let height;
        
        if (isMobile) {
            // On mobile, terminal-frame has explicit height, use its bounds
            const terminalFrame = document.querySelector('.terminal-frame');
            const frameRect = terminalFrame.getBoundingClientRect();
            height = frameRect.height - (rect.top - frameRect.top) - 10;
        } else {
            // On desktop, calculate from viewport height
            height = window.innerHeight - terminalContainer.offsetTop - 40;
        }
        
        // Calculate character dimensions (approximate)
        const charWidth = 9;
        const lineHeight = 18;
        
        const cols = Math.floor(width / charWidth);
        const rows = Math.floor(height / lineHeight);
        
        if (cols > 0 && rows > 0) {
            this.terminal.resize(cols, rows);
            this.sendResize();
        }
    }

    base64ToUint8Array(base64String) {
        // Modern browsers
        if (typeof Uint8Array.fromBase64 === 'function') {
            return Uint8Array.fromBase64(base64String);
        }
        
        // Fallback for older browsers
        const binaryString = atob(base64String);
        return Uint8Array.from(binaryString, char => char.charCodeAt(0));
    }
}
