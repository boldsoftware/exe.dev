async function copyToClipboard(button, text) {
    if (navigator.clipboard && window.isSecureContext) {
        try {
            await navigator.clipboard.writeText(text);
            showCopyFeedback(button);
            showToast('SSH command copied to clipboard', button);
        } catch (err) {
            console.error('Failed to copy:', err);
            fallbackCopyTextToClipboard(text, button);
        }
    } else {
        fallbackCopyTextToClipboard(text, button);
    }
}

function fallbackCopyTextToClipboard(text, button) {
    const textArea = document.createElement('textarea');
    textArea.value = text;
    textArea.style.top = '0';
    textArea.style.left = '0';
    textArea.style.position = 'fixed';
    textArea.style.opacity = '0';
    
    document.body.appendChild(textArea);
    textArea.focus();
    textArea.select();
    
    try {
        const successful = document.execCommand('copy');
        if (successful) {
            showCopyFeedback(button);
            showToast('SSH command copied to clipboard', button);
        }
    } catch (err) {
        console.error('Fallback: Unable to copy', err);
    }
    
    document.body.removeChild(textArea);
}

function showCopyFeedback(button) {
    button.classList.add('copied');
    button.title = 'Copied!';
    
    setTimeout(() => {
        button.classList.remove('copied');
        button.title = 'Copy SSH command';
    }, 1000);
}

function handleBoxRowClick(event, boxRow) {
    // Don't expand if clicking on a button or link
    if (event.target.closest('button, a')) {
        return;
    }
    
    toggleExpand(boxRow);
}

function toggleExpand(boxRowOrButton) {
    // Handle both being called with a button (from expand button) or box row (from row click)
    const boxRow = boxRowOrButton.classList?.contains('box-row') 
        ? boxRowOrButton 
        : boxRowOrButton.closest('.box-row');
    
    const wasExpanded = boxRow.classList.contains('expanded');
    boxRow.classList.toggle('expanded');
    
    // If expanding and the box is creating, start the creation stream
    if (!wasExpanded && boxRow.classList.contains('expanded')) {
        const boxName = boxRow.querySelector('.box-name')?.textContent.trim();
        const statusDot = boxRow.querySelector('.status-dot');
        const hasLog = boxRow.dataset.hasLog === 'true';
        
        if (statusDot && statusDot.classList.contains('creating')) {
            if (boxName) {
                showCreationStream(boxName, boxRow);
            }
        } else if (hasLog && boxName) {
            // Box has a stored creation log, show it
            showStoredCreationLog(boxName, boxRow);
        }
    }
}

function showToast(message, button) {
    const toast = document.getElementById('toast');
    toast.textContent = message;
    
    // Position toast near the button
    const rect = button.getBoundingClientRect();
    toast.style.left = `${rect.left + rect.width / 2}px`;
    toast.style.top = `${rect.bottom + 8}px`;
    toast.style.transform = 'translateX(-50%)';
    
    toast.classList.add('show');
    
    setTimeout(() => {
        toast.classList.remove('show');
    }, 2000);
}

// Track which boxes have been initialized to avoid re-creating terminals
const initializedBoxes = new Set();

// Search functionality
function initSearch() {
    const searchInput = document.getElementById('box-search');
    if (!searchInput) return;
    
    // Pre-fill from URL parameter
    const urlParams = new URLSearchParams(window.location.search);
    const filter = urlParams.get('filter');
    
    if (filter) {
        searchInput.value = filter;
        filterBoxes(filter);
        updateSearchClearButton(searchInput);
        
        // Scroll to the filtered box and auto-expand it
        setTimeout(() => {
            const boxRow = findBoxRow(filter);
            if (boxRow) {
                boxRow.scrollIntoView({ behavior: 'smooth', block: 'center' });
                // Auto-expand the filtered box if it's creating
                const statusDot = boxRow.querySelector('.status-dot');
                if (statusDot && statusDot.classList.contains('creating')) {
                    const expandBtn = boxRow.querySelector('.expand-btn');
                    if (expandBtn && !boxRow.classList.contains('expanded')) {
                        expandBtn.click();
                    }
                }
            }
        });
    }
    
    searchInput.addEventListener('input', (e) => {
        const query = e.target.value;
        filterBoxes(query);
        updateSearchClearButton(e.target);
        
        // Update URL without navigation
        const url = new URL(window.location);
        if (query.trim()) {
            url.searchParams.set('filter', query);
        } else {
            url.searchParams.delete('filter');
        }
        window.history.replaceState({}, '', url);
    });
}

function updateSearchClearButton(input) {
    const searchBox = input.closest('.search-box');
    if (input.value.trim()) {
        searchBox.classList.add('has-value');
    } else {
        searchBox.classList.remove('has-value');
    }
}

function clearSearch() {
    const searchInput = document.getElementById('box-search');
    if (searchInput) {
        searchInput.value = '';
        filterBoxes('');
        updateSearchClearButton(searchInput);
        searchInput.focus();
    }
}

function filterBoxes(query) {
    const boxRows = document.querySelectorAll('.box-row');
    const lowerQuery = query.toLowerCase().trim();
    
    if (lowerQuery === '') {
        // No filter, show all
        boxRows.forEach(row => row.style.display = '');
        return;
    }
    
    // Check if any box matches exactly
    let hasExactMatch = false;
    boxRows.forEach(row => {
        const boxName = row.querySelector('.box-name')?.textContent.toLowerCase() || '';
        if (boxName === lowerQuery) {
            hasExactMatch = true;
        }
    });
    
    // Filter based on exact vs substring match
    boxRows.forEach(row => {
        const boxName = row.querySelector('.box-name')?.textContent.toLowerCase() || '';
        if (hasExactMatch) {
            // Show only exact match
            row.style.display = boxName === lowerQuery ? '' : 'none';
        } else {
            // Show all substring matches
            row.style.display = boxName.includes(lowerQuery) ? '' : 'none';
        }
    });
}

function findBoxRow(boxName) {
    const boxRows = document.querySelectorAll('.box-row');
    for (const row of boxRows) {
        const name = row.querySelector('.box-name')?.textContent.trim();
        if (name === boxName) {
            return row;
        }
    }
    return null;
}

function showStoredCreationLog(hostname, boxRow) {
    // Check if already initialized
    if (initializedBoxes.has(hostname)) {
        return;
    }
    initializedBoxes.add(hostname);
    
    // Create terminal container
    const boxDetails = boxRow.querySelector('.box-details');
    if (!boxDetails) return;
    
    let termContainer = document.createElement('div');
    termContainer.className = 'box-terminal';
    termContainer.id = `term-${hostname}`;
    boxDetails.appendChild(termContainer);
    
    // Initialize terminal with shared theme
    const themeManager = new ThemeManager();
    const term = new Terminal({
        fontSize: 14,
        fontFamily: 'Consolas, "Liberation Mono", Menlo, Courier, monospace',
        disableStdin: true,
        cursorBlink: false,
        scrollback: 5000,
        theme: themeManager.getTerminalTheme()
    });
    term.open(termContainer);
    
    // Enable clickable links
    const webLinksAddon = new WebLinksAddon.WebLinksAddon();
    term.loadAddon(webLinksAddon);
    
    // Fit terminal
    setTimeout(() => {
        const cols = Math.max(40, Math.floor(termContainer.clientWidth / 9));
        const rows = Math.max(10, Math.floor(200 / 18));
        term.resize(cols, rows);
    }, 0);
    
    // Fetch and display the stored log
    (async () => {
        try {
            const resp = await fetch('/m/box/creation-log?hostname=' + encodeURIComponent(hostname));
            if (resp.ok) {
                // Read the response as binary data and convert to Uint8Array for xterm.js
                const arrayBuffer = await resp.arrayBuffer();
                const data = new Uint8Array(arrayBuffer);
                if (data.length > 0) {
                    term.write(data);
                }
            }
        } catch (err) {
            term.write('Error loading creation log\r\n');
            console.error('Failed to load creation log:', err);
        }
    })();
}

function updateBoxStatusToRunning(boxRow) {
    // Update the status dot from 'creating' to 'running'
    const statusDot = boxRow.querySelector('.status-dot');
    if (statusDot) {
        statusDot.classList.remove('creating');
        statusDot.classList.add('running');
        statusDot.title = 'running';
    }
    
    // Update the status text in the expanded details
    const statusValue = boxRow.querySelector('.box-info-value');
    if (statusValue && statusValue.previousElementSibling?.textContent === 'Status:') {
        statusValue.textContent = 'running';
    }
}

function showCreationStream(hostname, boxRow) {
    // Check if already initialized to avoid duplicate terminals
    if (initializedBoxes.has(hostname)) {
        return;
    }
    initializedBoxes.add(hostname);
    
    // Create terminal container
    const boxDetails = boxRow.querySelector('.box-details');
    if (!boxDetails) return;
    
    let termContainer = document.createElement('div');
    termContainer.className = 'box-terminal';
    termContainer.id = `term-${hostname}`;
    boxDetails.appendChild(termContainer);
    
    // Initialize terminal with shared theme
    const themeManager = new ThemeManager();
    const term = new Terminal({
        fontSize: 14,
        fontFamily: 'Consolas, "Liberation Mono", Menlo, Courier, monospace',
        disableStdin: true,
        cursorBlink: false,
        scrollback: 5000,
        theme: themeManager.getTerminalTheme()
    });
    term.open(termContainer);
    
    // Enable clickable links
    const webLinksAddon = new WebLinksAddon.WebLinksAddon();
    term.loadAddon(webLinksAddon);
    
    // Fit terminal
    setTimeout(() => {
        const cols = Math.max(40, Math.floor(termContainer.clientWidth / 9));
        const rows = Math.max(10, Math.floor(200 / 18));
        term.resize(cols, rows);
    }, 0);
    
    // Start creation stream
    (async () => {
        try {
            const resp = await fetch('/m/creating/stream?hostname=' + encodeURIComponent(hostname));
            if (!resp.ok) {
                const msg = (await resp.text()) || ('HTTP ' + resp.status);
                term.write('Error: ' + msg + '\r\n');
                return;
            }
            
            // Parse SSE stream
            const reader = resp.body.getReader();
            const decoder = new TextDecoder();
            let buf = '';
            let curEvent = '';
            
            while (true) {
                // Read chunks from the stream (SSE protocol sends text)
                const { value, done } = await reader.read();
                if (done) break;
                
                // Decode the chunk and add to buffer
                buf += decoder.decode(value, { stream: true });
                
                // Process complete SSE messages (lines)
                let idx;
                while ((idx = buf.indexOf('\n')) !== -1) {
                    const line = buf.slice(0, idx);
                    buf = buf.slice(idx + 1);
                    
                    if (line.startsWith('event: ')) {
                        curEvent = line.slice(7).trim();
                    } else if (line.startsWith('data: ')) {
                        const data = line.slice(6);
                        
                        // Handle different event types
                        if (!curEvent || curEvent === 'message') {
                            // Base64-encoded terminal output - decode and write to terminal
                            try {
                                const bytes = Uint8Array.fromBase64(data);
                                term.write(bytes);
                            } catch (e) {
                                console.error('Failed to decode base64 data:', e);
                            }
                        } else if (curEvent === 'fail') {
                            term.write('\r\nError: ' + (data || 'failed') + '\r\n');
                        } else if (curEvent === 'done') {
                            term.write('\r\n✓ VM created successfully!\r\n');
                            // Update the box status to running without full page reload
                            updateBoxStatusToRunning(boxRow);
                        }
                    } else if (line === '') {
                        curEvent = '';
                    }
                }
            }
        } catch (err) {
            term.write('Error: connection lost\r\n');
            console.error('Creation failed:', err);
        }
    })();
}

// Command Modal - generic widget for running shell commands
class CommandModal {
    #overlay = null;
    #title = null;
    #display = null;
    #output = null;
    #runBtn = null;
    #currentCommand = '';
    #inputEl = null;
    #isDanger = false;
    #commandSucceeded = false;
    #needsReload = false;

    // Arrow function to preserve 'this' binding for event listener
    #handleEscape = (e) => {
        if (e.key === 'Escape') {
            this.close();
        }
    };

    #init() {
        this.#overlay = document.getElementById('cmd-modal-overlay');
        this.#title = document.getElementById('cmd-modal-title');
        this.#display = document.getElementById('cmd-display');
        this.#output = document.getElementById('cmd-output');
        this.#runBtn = document.getElementById('cmd-run-btn');
    }

    #escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    #getFullCommand() {
        if (this.#inputEl) {
            const input = this.#inputEl.value.trim();
            if (!input) return null;
            return `${this.#currentCommand} ${input}`;
        }
        return this.#currentCommand;
    }

    #resetInputState() {
        if (this.#commandSucceeded) {
            this.#commandSucceeded = false;
            this.#runBtn.disabled = false;
            this.#runBtn.textContent = 'Run';
            this.#output.classList.remove('visible', 'success', 'error');
        }
    }

    #showSuccess(message) {
        this.#output.classList.add('visible', 'success');
        this.#output.textContent = message || 'Done';
        this.#runBtn.textContent = 'Run';
        this.#runBtn.disabled = true;
        this.#commandSucceeded = true;
        this.#needsReload = true;
    }

    #showError(message) {
        this.#output.classList.add('visible', 'error');
        this.#output.textContent = message || 'Command failed';
        this.#runBtn.textContent = 'Run';
        this.#runBtn.disabled = false;
    }

    /**
     * Open modal with a command
     * @param {Object} options
     * @param {string} options.title - Modal title
     * @param {string} [options.command] - Full command to run (no input needed)
     * @param {string} [options.commandPrefix] - Command prefix (input appended)
     * @param {string} [options.inputPlaceholder] - Placeholder for input field
     * @param {boolean} [options.danger] - Use red "Run" button
     */
    open(options) {
        if (!this.#overlay) this.#init();

        this.#title.textContent = options.title || 'Run Command';
        this.#isDanger = options.danger || false;

        // Reset state
        this.#output.classList.remove('visible', 'success', 'error');
        this.#output.textContent = '';
        this.#runBtn.disabled = false;
        this.#runBtn.textContent = 'Run';
        this.#commandSucceeded = false;
        this.#needsReload = false;

        // Set button style
        this.#runBtn.classList.toggle('danger', this.#isDanger);

        if (options.command) {
            this.#currentCommand = options.command;
            this.#inputEl = null;
            this.#display.innerHTML = `<span class="cmd-text-static">${this.#escapeHtml(options.command)}</span>`;
        } else if (options.commandPrefix) {
            this.#currentCommand = options.commandPrefix;
            this.#display.innerHTML = `
                <span class="cmd-text-static">${this.#escapeHtml(options.commandPrefix)} </span>
                <input type="text" class="cmd-input" id="cmd-input-field"
                       placeholder="${options.inputPlaceholder || ''}" autocomplete="off">
            `.trim();

            this.#inputEl = document.getElementById('cmd-input-field');
            this.#inputEl.addEventListener('keydown', (e) => {
                if (e.key === 'Enter') this.run();
            });
            this.#inputEl.addEventListener('input', () => this.#resetInputState());
            setTimeout(() => this.#inputEl.focus(), 50);
        }

        this.#overlay.classList.add('visible');
        document.addEventListener('keydown', this.#handleEscape);
    }

    close() {
        this.#overlay?.classList.remove('visible');
        document.removeEventListener('keydown', this.#handleEscape);
        // Reload if any command succeeded, preserving expanded state
        if (this.#needsReload) {
            saveExpandedState();
            window.location.reload();
        }
    }

    async run() {
        if (this.#commandSucceeded) return;

        const command = this.#getFullCommand();
        if (!command) {
            this.#inputEl?.focus();
            return;
        }

        this.#runBtn.disabled = true;
        this.#runBtn.textContent = 'Running...';
        this.#output.classList.remove('success', 'error');

        try {
            const response = await fetch('/m/cmd', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ command }),
            });

            const result = await response.json();

            if (result.success) {
                this.#showSuccess(result.output);
            } else {
                this.#showError(result.output || result.error);
            }
        } catch (err) {
            this.#showError(`Network error: ${err.message}`);
        }
    }

    // Static convenience methods for common actions
    static shareByEmail(boxName) {
        cmdModal.open({
            title: 'Share VM',
            commandPrefix: `share add ${boxName}`,
            inputPlaceholder: 'user@example.com'
        });
    }

    static createShareLink(boxName) {
        cmdModal.open({
            title: 'Create Share Link',
            command: `share add-link ${boxName}`
        });
    }

    static removeShare(boxName, email) {
        cmdModal.open({
            title: 'Remove Access',
            command: `share remove ${boxName} ${email}`,
            danger: true
        });
    }

    static removeShareLink(boxName, token) {
        cmdModal.open({
            title: 'Remove Share Link',
            command: `share remove-link ${boxName} ${token}`,
            danger: true
        });
    }

    static deleteBox(boxName) {
        cmdModal.open({
            title: 'Delete VM',
            command: `rm ${boxName}`,
            danger: true
        });
    }

    static restartBox(boxName) {
        cmdModal.open({
            title: 'Restart VM',
            command: `restart ${boxName}`
        });
    }

    static setPublic(boxName) {
        cmdModal.open({
            title: 'Make Public',
            command: `share set-public ${boxName}`
        });
    }

    static setPrivate(boxName) {
        cmdModal.open({
            title: 'Make Private',
            command: `share set-private ${boxName}`
        });
    }

    static setPort(boxName) {
        cmdModal.open({
            title: 'Set Proxy Port',
            commandPrefix: `share port ${boxName}`,
            inputPlaceholder: 'port (e.g. 8080)'
        });
    }
}

// Global instance
const cmdModal = new CommandModal();

// State preservation for reload after actions
function saveExpandedState() {
    const expanded = [];
    document.querySelectorAll('.box-row.expanded').forEach(row => {
        const name = row.querySelector('.box-name')?.textContent.trim();
        if (name) expanded.push(name);
    });
    if (expanded.length > 0) {
        sessionStorage.setItem('expandedBoxes', JSON.stringify(expanded));
    }
}

function restoreExpandedState() {
    const stored = sessionStorage.getItem('expandedBoxes');
    if (!stored) return;

    sessionStorage.removeItem('expandedBoxes');

    try {
        const expanded = JSON.parse(stored);
        document.querySelectorAll('.box-row').forEach(row => {
            const name = row.querySelector('.box-name')?.textContent.trim();
            if (name && expanded.includes(name)) {
                row.classList.add('expanded');
            }
        });
    } catch (e) {
        console.error('Failed to restore expanded state:', e);
    }
}

// Global functions for onclick handlers in HTML
const openShareModal = (boxName) => CommandModal.shareByEmail(boxName);
const openShareLinkModal = (boxName) => CommandModal.createShareLink(boxName);
const openRemoveShareModal = (boxName, email) => CommandModal.removeShare(boxName, email);
const openRemoveShareLinkModal = (boxName, token) => CommandModal.removeShareLink(boxName, token);
const openDeleteBoxModal = (boxName) => CommandModal.deleteBox(boxName);
const openRestartBoxModal = (boxName) => CommandModal.restartBox(boxName);
const openSetPublicModal = (boxName) => CommandModal.setPublic(boxName);
const openSetPrivateModal = (boxName) => CommandModal.setPrivate(boxName);
const openSetPortModal = (boxName) => CommandModal.setPort(boxName);

// VSCode modal functionality
let vscodeBaseURL = '';

function openVSCodeModal(url) {
    // Extract the base URL (everything before the path after ssh-remote+connection/)
    // URL format: vscode://vscode-remote/ssh-remote+boxname@host:port/path?windowId=_blank
    const match = url.match(/^(vscode:\/\/vscode-remote\/ssh-remote\+[^/]+)/);
    if (match) {
        vscodeBaseURL = match[1];
    } else {
        vscodeBaseURL = url.replace(/\/[^?]+/, '');
    }

    // Reset working directory to default
    const workdirInput = document.getElementById('vscode-workdir');
    workdirInput.value = '/home/exedev';

    updateVSCodeURL();

    const modal = document.getElementById('vscode-modal');
    modal.classList.add('show');
}

function closeVSCodeModal(event) {
    // If called with event, only close if clicking overlay (not content)
    if (event && event.target !== event.currentTarget) {
        return;
    }
    const modal = document.getElementById('vscode-modal');
    modal.classList.remove('show');
}

function updateVSCodeURL() {
    const workdir = document.getElementById('vscode-workdir').value || '/home/exedev';
    const fullURL = vscodeBaseURL + workdir + '?windowId=_blank';

    document.getElementById('vscode-url-box').textContent = fullURL;
    document.getElementById('vscode-open-btn').href = fullURL;
}

function copyVSCodeURL() {
    const url = document.getElementById('vscode-url-box').textContent;
    const btn = document.getElementById('vscode-copy-btn');

    if (navigator.clipboard && window.isSecureContext) {
        navigator.clipboard.writeText(url).then(() => {
            showCopyFeedback(btn);
        }).catch(() => {
            fallbackCopyTextToClipboard(url, btn);
        });
    } else {
        fallbackCopyTextToClipboard(url, btn);
    }
}

// Close modal on Escape key
document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
        closeVSCodeModal();
    }
});

// Initialize on page load
function initDashboard() {
    restoreExpandedState();
    initSearch();
}

if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initDashboard);
} else {
    initDashboard();
}
