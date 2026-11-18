(function () {
  var stream = document.getElementById('stream');
  if (!stream) return;

  var runId = stream.getAttribute('data-run');
  var statusPill = document.getElementById('status');
  var empty = document.getElementById('stream-empty');
  var source;

  function add(el) {
    if (empty) { empty.remove(); empty = null; }
    stream.appendChild(el);
    window.scrollTo({ top: document.body.scrollHeight, behavior: 'smooth' });
  }

  function text(ev) {
    var p = document.createElement('div');
    p.className = 'ev-text';
    p.textContent = ev.text || '';
    return p;
  }

  function tool(ev) {
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

  function note(cls, ev) {
    var p = document.createElement('div');
    p.className = cls;
    p.textContent = cls === 'ev-error' ? 'Error: ' + (ev.error || 'unknown') : 'Done';
    return p;
  }

  function onEvent(ev) {
    switch (ev.type) {
      case 'text':
        add(text(ev));
        break;
      case 'tool':
        add(tool(ev));
        break;
      case 'error':
        add(note('ev-error', ev));
        break;
      case 'done':
        if (statusPill) {
          statusPill.textContent = ev.error ? 'failed' : 'finished';
          statusPill.className = 'pill ' + (ev.error ? 'pill-error' : 'pill-up_to_date');
        }
        if (!ev.error) add(note('ev-done', ev));
        break;
    }
  }

  function connect() {
    source = new EventSource('/run/' + encodeURIComponent(runId) + '/events');
    source.onmessage = function (msg) {
      try { onEvent(JSON.parse(msg.data)); } catch (e) { /* ignore malformed frame */ }
    };
    source.onerror = function () {
      // The server closes the stream when the job finishes.
      source.close();
      if (statusPill && statusPill.textContent === 'running') statusPill.textContent = 'ended';
    };
  }

  connect();
})();
