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
                            term.write('\r\n✓ Box created successfully!\r\n');
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

// Initialize on page load
if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initSearch);
} else {
    initSearch();
}
