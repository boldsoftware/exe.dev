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
        
        this.terminal = new Terminal({
            cursorBlink: true,
            fontSize: 14,
            fontFamily: 'Consolas, "Liberation Mono", Menlo, Courier, monospace',
            theme: this.themeManager.getTerminalTheme()
        });

        this.terminal.open(document.getElementById('terminal'));
        
        // Enable clickable links
        const webLinksAddon = new WebLinksAddon.WebLinksAddon();
        this.terminal.loadAddon(webLinksAddon);
        
        this.setupEventHandlers();
        this.setupThemeToggle();
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

    setupEventHandlers() {
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
        
        // Calculate available space
        const width = rect.width - 20; // Account for padding
        const height = window.innerHeight - terminalContainer.offsetTop - 40;
        
        // Calculate character dimensions (approximate)
        const charWidth = 9; // Approximate character width
        const lineHeight = 18; // Approximate line height
        
        const cols = Math.floor(width / charWidth);
        const rows = Math.floor(height / lineHeight);
        
        this.terminal.resize(cols, rows);
        this.sendResize();
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
