// Passkey (WebAuthn) support for exe.dev

// Check if WebAuthn is supported
function isPasskeySupported() {
    return window.PublicKeyCredential !== undefined;
}

// Check if conditional UI (autofill) is supported
async function isConditionalUISupported() {
    if (!isPasskeySupported()) return false;
    try {
        return await PublicKeyCredential.isConditionalMediationAvailable();
    } catch {
        return false;
    }
}

// Base64URL encode/decode helpers
// Note: No native base64url in browsers yet; these are standard implementations
function base64URLEncode(buffer) {
    const bytes = new Uint8Array(buffer);
    const binary = Array.from(bytes, b => String.fromCharCode(b)).join('');
    return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
}

function base64URLDecode(str) {
    str = str.replace(/-/g, '+').replace(/_/g, '/');
    while (str.length % 4) str += '=';
    const binary = atob(str);
    return Uint8Array.from(binary, c => c.charCodeAt(0)).buffer;
}

// Register a new passkey
async function registerPasskey(name) {
    if (!isPasskeySupported()) {
        throw new Error('Passkeys are not supported on this device');
    }

    // Start registration
    const startResp = await fetch('/passkey/register/start', {
        method: 'POST',
        credentials: 'same-origin',
    });

    if (!startResp.ok) {
        const text = await startResp.text();
        throw new Error(text || 'Failed to start registration');
    }

    const options = await startResp.json();

    // Convert challenge and user ID from base64url
    options.publicKey.challenge = base64URLDecode(options.publicKey.challenge);
    options.publicKey.user.id = base64URLDecode(options.publicKey.user.id);

    // Convert excluded credential IDs if present
    if (options.publicKey.excludeCredentials) {
        options.publicKey.excludeCredentials = options.publicKey.excludeCredentials.map(cred => ({
            ...cred,
            id: base64URLDecode(cred.id),
        }));
    }

    // Create the credential
    let credential;
    try {
        credential = await navigator.credentials.create(options);
    } catch (err) {
        if (err.name === 'NotAllowedError') {
            const e = new Error('Passkey creation declined; you can always add passkeys on the profile page.');
            e.cancelled = true;
            throw e;
        }
        throw err;
    }

    // Prepare the response
    const attestationResponse = {
        id: credential.id,
        rawId: base64URLEncode(credential.rawId),
        type: credential.type,
        response: {
            clientDataJSON: base64URLEncode(credential.response.clientDataJSON),
            attestationObject: base64URLEncode(credential.response.attestationObject),
        },
    };

    // If authenticator provides transports, include them
    if (credential.response.getTransports) {
        attestationResponse.response.transports = credential.response.getTransports();
    }

    // Finish registration
    const finishResp = await fetch('/passkey/register/finish?name=' + encodeURIComponent(name), {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
        },
        credentials: 'same-origin',
        body: JSON.stringify(attestationResponse),
    });

    if (!finishResp.ok) {
        const text = await finishResp.text();
        throw new Error(text || 'Failed to complete registration');
    }

    return await finishResp.json();
}

// Authenticate with a passkey (mediation can be 'optional' for button click, or 'conditional' for autofill)
// redirectTo is an optional URL to redirect to after successful authentication
// extraParams is an optional object of extra query params to pass to the finish URL
async function authenticateWithPasskey(mediation, redirectTo, extraParams) {
    if (!isPasskeySupported()) {
        throw new Error('Passkeys are not supported on this device');
    }

    // Start authentication
    const startResp = await fetch('/passkey/login/start', {
        method: 'POST',
        credentials: 'same-origin',
    });

    if (!startResp.ok) {
        const text = await startResp.text();
        throw new Error(text || 'Failed to start authentication');
    }

    const options = await startResp.json();

    // Convert challenge from base64url
    options.publicKey.challenge = base64URLDecode(options.publicKey.challenge);

    // Convert allowed credential IDs if present
    if (options.publicKey.allowCredentials) {
        options.publicKey.allowCredentials = options.publicKey.allowCredentials.map(cred => ({
            ...cred,
            id: base64URLDecode(cred.id),
        }));
    }

    // Set mediation mode
    if (mediation) {
        options.mediation = mediation;
    }

    // Get the credential
    let credential;
    try {
        credential = await navigator.credentials.get(options);
    } catch (err) {
        if (err.name === 'NotAllowedError') {
            throw new Error('Authentication was cancelled');
        }
        throw err;
    }

    if (!credential) {
        throw new Error('No credential selected');
    }

    // Prepare the response
    const assertionResponse = {
        id: credential.id,
        rawId: base64URLEncode(credential.rawId),
        type: credential.type,
        response: {
            clientDataJSON: base64URLEncode(credential.response.clientDataJSON),
            authenticatorData: base64URLEncode(credential.response.authenticatorData),
            signature: base64URLEncode(credential.response.signature),
        },
    };

    // Include user handle if present
    if (credential.response.userHandle) {
        assertionResponse.response.userHandle = base64URLEncode(credential.response.userHandle);
    }

    // Finish authentication
    const finishParams = new URLSearchParams();
    if (redirectTo) {
        finishParams.set('redirect_to', redirectTo);
    }
    if (extraParams) {
        for (const [k, v] of Object.entries(extraParams)) {
            if (v) finishParams.set(k, v);
        }
    }
    const qs = finishParams.toString();
    let finishUrl = '/passkey/login/finish' + (qs ? '?' + qs : '');
    const finishResp = await fetch(finishUrl, {
        method: 'POST',
        headers: {
            'Content-Type': 'application/json',
        },
        credentials: 'same-origin',
        body: JSON.stringify(assertionResponse),
    });

    if (!finishResp.ok) {
        const text = await finishResp.text();
        throw new Error(text || 'Failed to complete authentication');
    }

    const result = await finishResp.json();

    // Redirect on success
    if (result.redirect) {
        window.location.href = result.redirect;
    }

    return result;
}

// Start conditional mediation (autofill) authentication
// This should be called on page load; it will resolve when user selects a passkey from autofill
// redirectTo is an optional URL to redirect to after successful authentication
async function startConditionalAuth(redirectTo, extraParams) {
    if (!await isConditionalUISupported()) {
        return null;
    }
    return authenticateWithPasskey('conditional', redirectTo, extraParams);
}

// Delete a passkey
async function deletePasskey(id) {
    const form = document.createElement('form');
    form.method = 'POST';
    form.action = '/passkey/delete';

    const input = document.createElement('input');
    input.type = 'hidden';
    input.name = 'id';
    input.value = id;

    form.appendChild(input);
    document.body.appendChild(form);
    form.submit();
}

// Generate a default passkey name based on platform
function getDefaultPasskeyName() {
    const ua = navigator.userAgent;
    if (/iPhone/.test(ua)) return 'iPhone';
    if (/iPad/.test(ua)) return 'iPad';
    if (/Macintosh/.test(ua)) return 'Mac';
    if (/Windows/.test(ua)) return 'Windows';
    if (/Android/.test(ua)) return 'Android';
    if (/Linux/.test(ua)) return 'Linux';
    return 'Passkey';
}

// Export functions for use in templates
window.passkey = {
    isSupported: isPasskeySupported,
    isConditionalUISupported: isConditionalUISupported,
    register: registerPasskey,
    authenticate: authenticateWithPasskey,
    startConditionalAuth: startConditionalAuth,
    delete: deletePasskey,
    getDefaultName: getDefaultPasskeyName,
};
