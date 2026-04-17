(function() {
    'use strict';

    // ── State ──
    var allUsers = [];
    var selectedUsers = {};
    var batchID = '';
    var usersLoaded = false;
    var batchRunning = false;
    var batchSucceeded = 0;
    var batchFailed = 0;
    var batchTotal = 0;

    // ── DOM refs ──
    var searchInput = document.getElementById('userSearch');
    var searchResults = document.getElementById('searchResults');
    var searchResultsBody = document.getElementById('searchResultsBody');
    var matchCount = document.getElementById('matchCount');
    var selectAllBtn = document.getElementById('selectAllBtn');
    var selectedTags = document.getElementById('selectedTags');
    var selectedNone = document.getElementById('selectedNone');
    var submitBtn = document.getElementById('batchSubmitBtn');
    var cancelBtn = document.getElementById('batchCancelBtn');
    var maxSelected = 10;
    var cancelAllBtn = document.getElementById('cancelAllBtn');
    var liveCount = document.getElementById('liveCount');
    var liveTableContainer = document.getElementById('liveTableContainer');
    var batchStatus = document.getElementById('batchStatus');
    var batchStatusText = document.getElementById('batchStatusText');
    var batchLogToggle = document.getElementById('batchLogToggle');
    var batchLog = document.getElementById('batchLog');
    var batchLogPre = document.getElementById('batchLogPre');

    // ── Live migrations polling ──
    function refreshLiveTable() {
        fetch('/debug/migrations?format=json')
            .then(function(r) { return r.json(); })
            .then(function(data) {
                var migs = data.live_migrations || [];
                liveCount.textContent = migs.length;

                if (migs.length === 0) {
                    liveTableContainer.innerHTML = '<p class="empty">No in-flight VM migrations.</p>';
                    return;
                }

                var html = '<table id="liveTable">' +
                    '<thead><tr><th>VM</th><th>Source \u2192 Target</th><th>Type</th><th>State</th><th>Transferred</th><th>Rate</th><th>Duration</th></tr></thead><tbody>';
                migs.forEach(function(m) {
                    html += '<tr>' +
                        '<td><a href="/debug/vms/' + escapeHtml(m.box_name) + '">' + escapeHtml(m.box_name) + '</a></td>' +
                        '<td><code>' + escapeHtml(m.source) + '</code> \u2192 <code>' + escapeHtml(m.target) + '</code></td>' +
                        '<td>' + (m.live ? 'live' : 'cold') + '</td>' +
                        '<td>' + escapeHtml(m.state) + '</td>' +
                        '<td>' + escapeHtml(m.transferred) + '</td>' +
                        '<td>' + escapeHtml(m.transfer_rate) + '</td>' +
                        '<td>' + escapeHtml(m.duration) + '</td>' +
                        '</tr>';
                });
                html += '</tbody></table>';
                liveTableContainer.innerHTML = html;
            })
            .catch(function() {}); // silent on error, will retry
    }

    setInterval(refreshLiveTable, 2000);

    // ── Cancel All ──
    cancelAllBtn.addEventListener('click', function() {
        if (!confirm('Cancel ALL in-flight migrations and batches?')) return;
        cancelAllBtn.disabled = true;
        cancelAllBtn.textContent = 'Cancelling...';
        fetch('/debug/migrations/cancel-all', { method: 'POST' })
            .then(function(r) { return r.json(); })
            .then(function(data) {
                cancelAllBtn.textContent = 'Cancelled ' + data.migrations_cancelled + ' migration(s), ' + data.batches_cancelled + ' batch(es)';
                setTimeout(function() {
                    cancelAllBtn.disabled = false;
                    cancelAllBtn.textContent = 'Cancel All';
                }, 3000);
            })
            .catch(function(err) {
                cancelAllBtn.textContent = 'Error: ' + err;
                setTimeout(function() {
                    cancelAllBtn.disabled = false;
                    cancelAllBtn.textContent = 'Cancel All';
                }, 3000);
            });
    });

    // ── User search ──
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

        var display = matches.slice(0, 100);
        matchCount.textContent = matches.length + ' match' + (matches.length === 1 ? '' : 'es');
        selectAllBtn.style.display = '';
        searchInput._currentMatches = matches;

        display.forEach(function(u) {
            var tr = document.createElement('tr');
            var isSelected = !!selectedUsers[u.user_id];
            if (isSelected) tr.className = 'selected';

            tr.innerHTML = '<td><input type="checkbox"' + (isSelected ? ' checked' : '') + '></td>' +
                '<td>' + escapeHtml(u.email) + '</td>' +
                '<td>' + escapeHtml(u.region || '') + '</td>' +
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

        searchResults.style.display = 'block';
    }

    function toggleUser(u) {
        if (selectedUsers[u.user_id]) {
            delete selectedUsers[u.user_id];
        } else {
            if (Object.keys(selectedUsers).length >= maxSelected) return;
            selectedUsers[u.user_id] = { email: u.email, user_id: u.user_id };
        }
        renderSelectedTags();
    }

    function renderSelectedTags() {
        selectedTags.innerHTML = '';
        var ids = Object.keys(selectedUsers);
        selectedNone.style.display = ids.length ? 'none' : '';
        submitBtn.disabled = ids.length === 0 || batchRunning;

        ids.forEach(function(uid) {
            var u = selectedUsers[uid];
            var tag = document.createElement('span');
            tag.className = 'queue-item';
            tag.textContent = u.email + ' \u00d7';
            tag.title = 'Click to remove';
            tag.addEventListener('click', function() {
                delete selectedUsers[uid];
                renderSelectedTags();
                renderSearchResults(searchInput.value);
            });
            selectedTags.appendChild(tag);
        });
    }

    function escapeHtml(s) {
        var div = document.createElement('div');
        div.appendChild(document.createTextNode(s || ''));
        return div.innerHTML;
    }

    var searchTimeout;
    searchInput.addEventListener('input', function() {
        clearTimeout(searchTimeout);
        searchTimeout = setTimeout(function() {
            ensureUsersLoaded(function() {
                renderSearchResults(searchInput.value);
            });
        }, 150);
    });

    selectAllBtn.addEventListener('click', function() {
        var matches = searchInput._currentMatches || [];
        matches.forEach(function(u) {
            if (Object.keys(selectedUsers).length >= maxSelected) return;
            if (!selectedUsers[u.user_id]) {
                selectedUsers[u.user_id] = { email: u.email, user_id: u.user_id };
            }
        });
        renderSelectedTags();
        renderSearchResults(searchInput.value);
    });

    // ── Batch log toggle ──
    batchLogToggle.addEventListener('click', function() {
        if (batchLog.style.display === 'none' || !batchLog.style.display) {
            batchLog.style.display = 'block';
            batchLogToggle.textContent = '[hide log]';
        } else {
            batchLog.style.display = 'none';
            batchLogToggle.textContent = '[show log]';
        }
    });

    // ── Batch status rendering ──
    function updateBatchStatus() {
        if (!batchRunning) return;
        batchStatusText.textContent = 'Migrating... succeeded: ' + batchSucceeded + ', failed: ' + batchFailed + ', total: ' + batchTotal;
    }

    function parseBatchProgress(text) {
        // Count succeeded/failed from progress lines.
        var lines = text.split('\n');
        for (var i = 0; i < lines.length; i++) {
            var line = lines[i];
            // Match lines like "[user@email] Done. Succeeded: 1, Failed: 0, Total: 1"
            var doneMatch = line.match(/Done\. Succeeded: (\d+), Failed: (\d+), Total: (\d+)/);
            if (doneMatch) {
                batchSucceeded += parseInt(doneMatch[1]);
                batchFailed += parseInt(doneMatch[2]);
                batchTotal += parseInt(doneMatch[3]);
            }
            var cancelMatch = line.match(/Cancelled\. Succeeded: (\d+), Failed: (\d+), Total: (\d+)/);
            if (cancelMatch) {
                batchSucceeded += parseInt(cancelMatch[1]);
                batchFailed += parseInt(cancelMatch[2]);
                batchTotal += parseInt(cancelMatch[3]);
            }
        }
        updateBatchStatus();
    }

    // ── Submit batch migration ──
    submitBtn.addEventListener('click', function() {
        var ids = Object.keys(selectedUsers);
        if (ids.length === 0) return;

        if (!confirm('Migrate VMs for ' + ids.length + ' user(s)? All selected users will run concurrently.')) return;

        batchRunning = true;
        batchSucceeded = 0;
        batchFailed = 0;
        batchTotal = 0;
        batchID = '';

        submitBtn.disabled = true;
        submitBtn.textContent = 'Migrating...';
        cancelBtn.style.display = '';
        cancelBtn.disabled = false;
        cancelBtn.textContent = 'Cancel Batch';
        searchInput.disabled = true;

        batchStatus.style.display = 'block';
        batchStatus.className = 'running';
        batchStatusText.textContent = 'Starting batch migration for ' + ids.length + ' user(s)...';
        batchLogPre.textContent = '';
        batchLog.style.display = 'none';
        batchLogToggle.textContent = '[show log]';

        var body = new URLSearchParams();
        ids.forEach(function(uid) {
            body.append('user_ids[]', uid);
        });

        fetch('/debug/migrations/batch', {
            method: 'POST',
            body: body
        }).then(function(response) {
            if (!response.ok) {
                return response.text().then(function(t) {
                    batchStatusText.textContent = 'HTTP error ' + response.status + ': ' + t;
                    batchStatus.className = 'error';
                    batchLogPre.textContent += 'HTTP error ' + response.status + ': ' + t + '\n';
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
                    batchLogPre.textContent += text;

                    var idMatch = text.match(/BATCH_ID:(\S+)/);
                    if (idMatch) batchID = idMatch[1];

                    parseBatchProgress(text);

                    if (text.indexOf('BATCH_SUCCESS') !== -1) {
                        batchStatus.className = 'success';
                        batchStatusText.textContent = 'Batch complete \u2014 succeeded: ' + batchSucceeded + ', failed: ' + batchFailed + ', total: ' + batchTotal;
                        cancelBtn.style.display = 'none';
                    } else if (text.indexOf('BATCH_ERROR') !== -1) {
                        batchStatus.className = 'error';
                        batchStatusText.textContent = 'Batch finished with errors \u2014 succeeded: ' + batchSucceeded + ', failed: ' + batchFailed + ', total: ' + batchTotal;
                        cancelBtn.style.display = 'none';
                    } else if (text.indexOf('BATCH_CANCELLED') !== -1) {
                        batchStatus.className = 'error';
                        batchStatusText.textContent = 'Batch cancelled \u2014 succeeded: ' + batchSucceeded + ', failed: ' + batchFailed + ', total: ' + batchTotal;
                        cancelBtn.style.display = 'none';
                    }

                    read();
                });
            }
            read();
        }).catch(function(err) {
            batchStatusText.textContent = 'Fetch error: ' + err;
            batchStatus.className = 'error';
            batchLogPre.textContent += '\nFetch error: ' + err + '\n';
            resetControls();
        });
    });

    function resetControls() {
        batchRunning = false;
        submitBtn.textContent = 'Migrate Selected Users';
        submitBtn.disabled = Object.keys(selectedUsers).length === 0;
        cancelBtn.style.display = 'none';
        searchInput.disabled = false;
    }

    // ── Cancel batch ──
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
            batchLogPre.textContent += '\nCancel requested...\n';
        }).catch(function(err) {
            batchLogPre.textContent += '\nCancel failed: ' + err + '\n';
            cancelBtn.disabled = false;
            cancelBtn.textContent = 'Cancel Batch';
        });
    });
})();
