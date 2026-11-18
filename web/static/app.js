(function () {
  'use strict';

  /* ---------- run stream (project session) ---------- */
  var stream = document.getElementById('stream');
  if (stream) {
    var runId = stream.getAttribute('data-run');
    var statusPill = document.getElementById('status');
    var empty = document.getElementById('stream-empty');

    function add(el) {
      if (empty) { empty.remove(); empty = null; }
      stream.appendChild(el);
      window.scrollTo({ top: document.body.scrollHeight, behavior: 'smooth' });
    }
    function textEl(ev) {
      var p = document.createElement('div');
      p.className = 'ev-text';
      p.textContent = ev.text || '';
      return p;
    }
    function toolEl(ev) {
      var p = document.createElement('div');
      p.className = 'ev-tool';
      var name = document.createElement('b');
      name.textContent = ev.tool || 'tool';
      var detail = document.createElement('span');
      detail.textContent = ev.detail || ev.status || '';
      p.appendChild(name);
      p.appendChild(detail);
      return p;
    }
    function noteEl(cls, ev) {
      var p = document.createElement('div');
      p.className = cls;
      p.textContent = cls === 'ev-error' ? 'Error: ' + (ev.error || 'unknown') : 'Done';
      return p;
    }
    function onRunEvent(ev) {
      switch (ev.type) {
        case 'text': add(textEl(ev)); break;
        case 'tool': add(toolEl(ev)); break;
        case 'error': add(noteEl('ev-error', ev)); break;
        case 'notice': {
          var n = document.createElement('div');
          n.className = 'ev-notice';
          n.textContent = ev.text || '';
          add(n);
          break;
        }
        case 'done':
          if (statusPill) {
            statusPill.textContent = ev.error ? 'failed' : 'finished';
            statusPill.className = 'pill ' + (ev.error ? 'pill-error' : 'pill-up_to_date');
          }
          if (!ev.error) add(noteEl('ev-done', ev));
          break;
      }
    }
    var source = new EventSource('/run/' + encodeURIComponent(runId) + '/events');
    source.onmessage = function (msg) {
      try { onRunEvent(JSON.parse(msg.data)); } catch (e) { /* ignore */ }
    };
    source.onerror = function () {
      source.close();
      if (statusPill && statusPill.textContent === 'running') statusPill.textContent = 'ended';
    };
  }

  /* ---------- job page (clone / pull) ---------- */
  var jobLog = document.getElementById('job-log');
  if (jobLog) {
    var jobId = jobLog.getAttribute('data-job');
    var jobState = document.getElementById('job-state');
    var jobElapsed = document.getElementById('job-elapsed');
    var jobOpen = document.getElementById('job-open');
    var started = Date.now();

    var tick = setInterval(function () {
      if (jobElapsed) jobElapsed.textContent = Math.round((Date.now() - started) / 1000) + 's';
    }, 1000);

    var js = new EventSource('/jobs/' + encodeURIComponent(jobId) + '/events');
    js.onmessage = function (msg) {
      var ev;
      try { ev = JSON.parse(msg.data); } catch (e) { return; }
      if (ev.type === 'log') {
        jobLog.textContent += ev.message + '\n';
        jobLog.scrollTop = jobLog.scrollHeight;
      } else if (ev.type === 'state') {
        clearInterval(tick);
        if (jobState) {
          jobState.textContent = ev.state;
          jobState.className = 'pill ' + (ev.state === 'failed' ? 'pill-error' : 'pill-up_to_date');
        }
        if (ev.error) jobLog.textContent += '\nError: ' + ev.error + '\n';
        if (ev.state === 'done' && jobOpen) jobOpen.hidden = false;
        js.close();
      }
    };
    js.onerror = function () { js.close(); clearInterval(tick); };
  }

  /* ---------- projects: search, filters, clone dialog ---------- */
  var list = document.getElementById('repo-list');
  if (!list) return;

  var q = document.getElementById('q');
  var qClear = document.getElementById('q-clear');
  var chips = Array.prototype.slice.call(document.querySelectorAll('.chip'));
  var noResults = document.getElementById('no-results');
  var countEl = document.getElementById('repo-count');
  var rows = Array.prototype.slice.call(list.querySelectorAll('.repo'));
  var filter = 'all';

  function matchesFilter(row) {
    var status = row.getAttribute('data-status');
    var loaded = row.getAttribute('data-loaded') === '1';
    switch (filter) {
      case 'loaded': return loaded;
      case 'not_loaded': return !loaded;
      case 'update': return status === 'behind' || status === 'diverged';
      case 'error': return status === 'error' || status === 'dirty';
      default: return true;
    }
  }

  function apply() {
    var term = (q.value || '').trim().toLowerCase();
    var shown = 0;
    rows.forEach(function (row) {
      var hay = (row.getAttribute('data-name') + ' ' +
                 row.getAttribute('data-desc') + ' ' +
                 row.getAttribute('data-lang')).toLowerCase();
      var ok = matchesFilter(row) && (term === '' || hay.indexOf(term) !== -1);
      row.hidden = !ok;
      if (ok) shown++;
    });
    if (noResults) noResults.hidden = shown !== 0;
    if (countEl) countEl.textContent = shown;
    if (qClear) qClear.hidden = term === '';
  }

  q.addEventListener('input', apply);
  qClear.addEventListener('click', function () { q.value = ''; q.focus(); apply(); });
  chips.forEach(function (c) {
    c.addEventListener('click', function () {
      chips.forEach(function (x) { x.classList.remove('is-active'); });
      c.classList.add('is-active');
      filter = c.getAttribute('data-filter');
      apply();
    });
  });
  apply();

  /* ---------- clone dialog ---------- */
  var dlg = document.getElementById('clone-dialog');
  var target = document.getElementById('clone-target');
  var form = document.getElementById('clone-form');
  var progress = document.getElementById('clone-progress');
  var logEl = document.getElementById('clone-log');
  var stateEl = document.getElementById('clone-state');
  var elapsedEl = document.getElementById('clone-elapsed');
  var startBtn = document.getElementById('clone-start');
  var cancelBtn = document.getElementById('clone-cancel');
  var openBtn = document.getElementById('clone-open');
  var modelInput = document.getElementById('clone-model');
  var roleInput = document.getElementById('clone-role');

  var current = null;   // { owner, repo }
  var es = null;
  var timer = null;
  var t0 = 0;

  function resetDialog() {
    if (es) { es.close(); es = null; }
    if (timer) { clearInterval(timer); timer = null; }
    form.hidden = false;
    progress.hidden = true;
    logEl.textContent = '';
    startBtn.hidden = false;
    startBtn.disabled = false;
    startBtn.textContent = 'Start clone';
    cancelBtn.textContent = 'Cancel';
    openBtn.hidden = true;
    openBtn.style.display = 'none';
    startBtn.style.display = '';
    startBtn.hidden = false;
    stateEl.textContent = 'cloning';
    stateEl.className = 'pill pill-running';
    elapsedEl.textContent = '0s';
  }

  function openDialog(owner, repo) {
    current = { owner: owner, repo: repo };
    target.textContent = owner + '/' + repo;
    openBtn.href = '/projects/' + encodeURIComponent(owner) + '/' + encodeURIComponent(repo);
    resetDialog();
    if (typeof dlg.showModal === 'function') dlg.showModal();
    else dlg.setAttribute('open', '');
  }

  function closeDialog() {
    if (es) { es.close(); es = null; }
    if (timer) { clearInterval(timer); timer = null; }
    if (typeof dlg.close === 'function') dlg.close();
    else dlg.removeAttribute('open');
  }

  function appendLog(msg) {
    logEl.textContent += msg + '\n';
    logEl.scrollTop = logEl.scrollHeight;
  }

  function follow(jobId) {
    form.hidden = true;
    progress.hidden = false;
    startBtn.hidden = true;
    cancelBtn.textContent = 'Close';
    t0 = Date.now();
    timer = setInterval(function () {
      elapsedEl.textContent = Math.round((Date.now() - t0) / 1000) + 's';
    }, 1000);

    es = new EventSource('/jobs/' + encodeURIComponent(jobId) + '/events');
    es.onmessage = function (msg) {
      var ev;
      try { ev = JSON.parse(msg.data); } catch (e) { return; }
      if (ev.type === 'log') {
        appendLog(ev.message);
      } else if (ev.type === 'state') {
        clearInterval(timer); timer = null;
        stateEl.textContent = ev.state;
        stateEl.className = 'pill ' + (ev.state === 'failed' ? 'pill-error' : 'pill-up_to_date');
        if (ev.error) appendLog('Error: ' + ev.error);
        if (ev.state === 'done') {
          openBtn.hidden = false;
          openBtn.style.display = '';
          startBtn.style.display = 'none';
          var row = list.querySelector('.repo[data-owner="' + current.owner + '"][data-name="' + current.repo + '"]');
          if (row) {
            row.setAttribute('data-loaded', '1');
            row.setAttribute('data-status', 'up_to_date');
            var pill = row.querySelector('.pill');
            if (pill) { pill.textContent = 'loaded'; pill.className = 'pill pill-up_to_date'; }
            var name = row.querySelector('.repo-name');
            if (name && name.tagName !== 'A') {
              var a = document.createElement('a');
              a.className = 'repo-name';
              a.href = openBtn.href;
              a.textContent = current.repo;
              name.replaceWith(a);
            }
            var actions = row.querySelector('.repo-actions');
            if (actions) {
              actions.innerHTML = '';
              var open = document.createElement('a');
              open.className = 'btn btn-secondary btn-sm';
              open.href = openBtn.href;
              open.textContent = 'Open';
              actions.appendChild(open);
            }
          }
        }
        es.close(); es = null;
      }
    };
    es.onerror = function () { if (es) { es.close(); es = null; } };
  }

  startBtn.addEventListener('click', function () {
    if (!current) return;
    startBtn.disabled = true;
    startBtn.textContent = 'Starting…';

    var body = new URLSearchParams({
      owner: current.owner,
      repo: current.repo,
      model: modelInput.value.trim(),
      role: roleInput.value.trim()
    });

    fetch('/projects/clone', {
      method: 'POST',
      headers: { 'Accept': 'application/json', 'Content-Type': 'application/x-www-form-urlencoded' },
      body: body.toString()
    }).then(function (r) {
      if (!r.ok) throw new Error('HTTP ' + r.status);
      return r.json();
    }).then(function (data) {
      if (!data.job) throw new Error('no job id');
      follow(data.job);
    }).catch(function (err) {
      startBtn.disabled = false;
      startBtn.textContent = 'Start clone';
      appendLog('Could not start clone: ' + err.message);
      progress.hidden = false;
    });
  });

  cancelBtn.addEventListener('click', closeDialog);

  list.addEventListener('click', function (e) {
    var clone = e.target.closest('.js-clone');
    if (clone) {
      openDialog(clone.getAttribute('data-owner'), clone.getAttribute('data-repo'));
      return;
    }
    var pull = e.target.closest('.js-pull');
    if (pull) {
      pull.disabled = true;
      pull.textContent = 'Pulling…';
      fetch('/projects/pull', {
        method: 'POST',
        headers: { 'Accept': 'application/json', 'Content-Type': 'application/x-www-form-urlencoded' },
        body: 'owner=' + encodeURIComponent(pull.getAttribute('data-owner')) +
              '&repo=' + encodeURIComponent(pull.getAttribute('data-repo'))
      }).then(function (r) { return r.json(); }).then(function (data) {
        if (data.job) window.location.href = '/jobs/' + encodeURIComponent(data.job);
      }).catch(function () {
        pull.disabled = false;
        pull.textContent = 'Pull';
      });
    }
  });
})();
