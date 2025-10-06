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

function toggleExpand(button) {
    const boxRow = button.closest('.box-row');
    boxRow.classList.toggle('expanded');
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
