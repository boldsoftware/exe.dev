(function() {
    'use strict';

    var allUsers = [];
    var selectedUsers = {}; // keyed by user_id
    var batchID = '';
    var usersLoaded = false;

    var searchInput = document.getElementById('userSearch');
    var searchResults = document.getElementById('searchResults');
    var searchResultsBody = document.getElementById('searchResultsBody');
    var matchCount = document.getElementById('matchCount');
    var selectAllBtn = document.getElementById('selectAllBtn');
    var selectedTags = document.getElementById('selectedTags');
    var selectedNone = document.getElementById('selectedNone');
    var submitBtn = document.getElementById('batchSubmitBtn');
    var cancelBtn = document.getElementById('batchCancelBtn');
    var concurrencyInput = document.getElementById('concurrencyInput');
    var progressLog = document.getElementById('batchProgress');

    // Load users on first search interaction.
    function ensureUsersLoaded(cb) {
        if (usersLoaded) { cb(); return; }
        fetch('/debug/users?format=json')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                allUsers = (data || []).filter(function(u) { return !u.created_for_login_with_exe; });
                usersLoaded = true;
                cb();
            })
            .catch(function(err) {
                matchCount.textContent = 'Failed to load users: ' + err;
            });
    }

    function renderSearchResults(query) {
        var q = query.toLowerCase().trim();
        searchResultsBody.innerHTML = '';

        if (q.length < 2) {
            searchResults.style.display = 'none';
            matchCount.textContent = '';
            selectAllBtn.style.display = 'none';
            return;
        }

        var matches = allUsers.filter(function(u) {
            return u.email.toLowerCase().indexOf(q) !== -1 ||
                   u.user_id.toLowerCase().indexOf(q) !== -1;
        });

        if (matches.length === 0) {
            searchResults.style.display = 'none';
            matchCount.textContent = 'No matches';
            selectAllBtn.style.display = 'none';
            return;
        }

        // Cap display at 100.
        var display = matches.slice(0, 100);
        matchCount.textContent = matches.length + ' match' + (matches.length === 1 ? '' : 'es');
        selectAllBtn.style.display = '';

        // Store current matches for select-all.
        searchInput._currentMatches = matches;

        display.forEach(function(u) {
            var tr = document.createElement('tr');
            var isSelected = !!selectedUsers[u.user_id];
            if (isSelected) tr.className = 'selected';

            tr.innerHTML = '<td><input type="checkbox"' + (isSelected ? ' checked' : '') + '></td>' +
                '<td>' + escapeHtml(u.email) + '</td>' +
                '<td>' + escapeHtml(u.user_id.substring(0, 8)) + '</td>' +
                '<td><code>' + escapeHtml(u.user_id) + '</code></td>';

            tr.addEventListener('click', function(e) {
                if (e.target.tagName === 'A') return;
                toggleUser(u);
                var cb = tr.querySelector('input[type=checkbox]');
                cb.checked = !!selectedUsers[u.user_id];
                tr.className = selectedUsers[u.user_id] ? 'selected' : '';
            });

            searchResultsBody.appendChild(tr);
        });

        searchResults.style.display = '';
    }

    function toggleUser(u) {
        if (selectedUsers[u.user_id]) {
            delete selectedUsers[u.user_id];
        } else {
            selectedUsers[u.user_id] = { email: u.email, user_id: u.user_id };
        }
        renderSelectedTags();
    }

    function renderSelectedTags() {
        selectedTags.innerHTML = '';
        var ids = Object.keys(selectedUsers);
        selectedNone.style.display = ids.length ? 'none' : '';
        submitBtn.disabled = ids.length === 0;

        ids.forEach(function(uid) {
            var u = selectedUsers[uid];
            var tag = document.createElement('span');
            tag.className = 'queue-item';
            tag.textContent = u.email + ' \u00d7';
            tag.title = 'Click to remove';
            tag.addEventListener('click', function() {
                delete selectedUsers[uid];
                renderSelectedTags();
                // Update search results checkboxes if visible.
                renderSearchResults(searchInput.value);
            });
            selectedTags.appendChild(tag);
        });
    }

    function escapeHtml(s) {
        var div = document.createElement('div');
        div.appendChild(document.createTextNode(s));
        return div.innerHTML;
    }

    // Search input handler.
    var searchTimeout;
    searchInput.addEventListener('input', function() {
        clearTimeout(searchTimeout);
        searchTimeout = setTimeout(function() {
            ensureUsersLoaded(function() {
                renderSearchResults(searchInput.value);
            });
        }, 150);
    });

    // Select all visible matches.
    selectAllBtn.addEventListener('click', function() {
        var matches = searchInput._currentMatches || [];
        matches.forEach(function(u) {
            if (!selectedUsers[u.user_id]) {
                selectedUsers[u.user_id] = { email: u.email, user_id: u.user_id };
            }
        });
        renderSelectedTags();
        renderSearchResults(searchInput.value);
    });

    // Submit batch migration.
    submitBtn.addEventListener('click', function() {
        var ids = Object.keys(selectedUsers);
        if (ids.length === 0) return;

        var concurrency = parseInt(concurrencyInput.value) || 1;
        if (!confirm('Migrate VMs for ' + ids.length + ' user(s) with concurrency ' + concurrency + '?')) return;

        submitBtn.disabled = true;
        submitBtn.textContent = 'Migrating...';
        cancelBtn.style.display = '';
        cancelBtn.disabled = false;
        cancelBtn.textContent = 'Cancel';
        searchInput.disabled = true;
        concurrencyInput.disabled = true;
        progressLog.style.display = 'block';
        progressLog.className = '';
        progressLog.textContent = 'Starting batch migration...\n';

        var body = new URLSearchParams();
        ids.forEach(function(uid) {
            body.append('user_ids[]', uid);
        });
        body.append('concurrency', concurrency.toString());

        var userScrolledUp = false;
        progressLog.addEventListener('scroll', function() {
            var atBottom = progressLog.scrollHeight - progressLog.scrollTop - progressLog.clientHeight < 20;
            userScrolledUp = !atBottom;
        });

        fetch('/debug/migrations/batch', {
            method: 'POST',
            body: body
        }).then(function(response) {
            if (!response.ok) {
                return response.text().then(function(t) {
                    progressLog.textContent += 'HTTP error ' + response.status + ': ' + t + '\n';
                    progressLog.className = 'error';
                    resetControls();
                });
            }
            var reader = response.body.getReader();
            var decoder = new TextDecoder();

            function read() {
                reader.read().then(function(result) {
                    if (result.done) {
                        resetControls();
                        return;
                    }
                    var text = decoder.decode(result.value, {stream: true});
                    progressLog.textContent += text;
                    if (!userScrolledUp) {
                        progressLog.scrollTop = progressLog.scrollHeight;
                    }

                    // Capture batch ID for cancel support.
                    var idMatch = text.match(/BATCH_ID:(\S+)/);
                    if (idMatch) batchID = idMatch[1];

                    if (text.indexOf('BATCH_SUCCESS') !== -1) {
                        progressLog.className = 'success';
                        cancelBtn.style.display = 'none';
                    } else if (text.indexOf('BATCH_ERROR') !== -1 || text.indexOf('BATCH_CANCELLED') !== -1) {
                        progressLog.className = 'error';
                        cancelBtn.style.display = 'none';
                    }

                    read();
                });
            }
            read();
        }).catch(function(err) {
            progressLog.textContent += '\nFetch error: ' + err + '\n';
            progressLog.className = 'error';
            resetControls();
        });
    });

    function resetControls() {
        submitBtn.textContent = 'Migrate Selected Users';
        submitBtn.disabled = Object.keys(selectedUsers).length === 0;
        cancelBtn.style.display = 'none';
        searchInput.disabled = false;
        concurrencyInput.disabled = false;
    }

    // Cancel batch migration.
    cancelBtn.addEventListener('click', function() {
        if (!batchID) return;
        cancelBtn.disabled = true;
        cancelBtn.textContent = 'Cancelling...';
        fetch('/debug/vms/cancel-migration', {
            method: 'POST',
            headers: {'Content-Type': 'application/x-www-form-urlencoded'},
            body: 'batch_id=' + encodeURIComponent(batchID)
        }).then(function(resp) {
            if (!resp.ok) return resp.text().then(function(t) { throw new Error(t); });
            progressLog.textContent += '\nCancel requested...\n';
        }).catch(function(err) {
            progressLog.textContent += '\nCancel failed: ' + err + '\n';
            cancelBtn.disabled = false;
            cancelBtn.textContent = 'Cancel';
        });
    });
})();
