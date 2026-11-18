// Shopping list: stays in step across devices, and keeps working without a connection.
//  - every few seconds it asks the server whether the list changed and refreshes it;
//  - "Have it", "Bought" and "Move back" are sent in the background; when there is no
//    connection they are saved on this device and sent, in order, when it returns;
//  - a service worker keeps the last copy of the list page for opening offline.
(function () {
  if ('serviceWorker' in navigator) {
    navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(function () {});
  }
  // Signing out empties the saved copy of the list on this device.
  document.addEventListener('submit', function (e) {
    if (e.target.hasAttribute && e.target.hasAttribute('data-logout')) {
      try { if (window.caches) { caches.keys().then(function (ks) { ks.forEach(function (k) { caches.delete(k); }); }); } } catch (err) { /* ignore */ }
      try { localStorage.removeItem(QKEY); } catch (err) { /* ignore */ }
    }
  });

  var QKEY = 'vgs_queue';
  var ACTION = /^\/list\/items\/\d+\/(state|purchase)$/;
  var root = document.querySelector('[data-listroot]');
  if (!root) { return; }

  function load() {
    try { var q = JSON.parse(localStorage.getItem(QKEY) || '[]'); return Array.isArray(q) ? q : []; } catch (e) { return []; }
  }
  function save(q) {
    try { localStorage.setItem(QKEY, JSON.stringify(q)); } catch (e) { /* storage full or blocked */ }
  }
  function uid() {
    var a = new Uint8Array(12);
    (window.crypto || window.msCrypto).getRandomValues(a);
    return Array.prototype.map.call(a, function (b) { return ('0' + b.toString(16)).slice(-2); }).join('');
  }
  function banner() { return root.querySelector('[data-syncbanner]'); }
  function say(text) {
    var b = banner();
    if (!b) { return; }
    b.textContent = text || '';
    b.hidden = !text;
  }
  function status() {
    var n = load().length, off = !navigator.onLine;
    if (n) { say(n + ' change' + (n === 1 ? '' : 's') + ' saved on this device, waiting to sync' + (off ? ' (offline)' : '') + '.'); }
    else if (off) { say('Offline: showing the last saved copy of the list. Check-offs will sync when you are back online.'); }
    else if (!stale) { say(''); }
  }
  function markPending() {
    var q = load();
    root.querySelectorAll('li.item.pending').forEach(function (li) { li.classList.remove('pending'); });
    q.forEach(function (op) {
      var f = root.querySelector('form[action="' + op.url + '"]');
      var li = f && f.closest('li.item');
      if (li) { li.classList.add('pending'); }
    });
  }

  var stale = false, busy = false;
  function editing() {
    return !!root.querySelector('details.buy[open]') || (document.activeElement && root.contains(document.activeElement) &&
      /^(INPUT|SELECT|TEXTAREA)$/.test(document.activeElement.tagName));
  }

  // refresh swaps in the newest page, unless you are typing a price.
  function refresh(force) {
    if (!navigator.onLine) { return Promise.resolve(); }
    if (!force && editing()) {
      stale = true;
      say('The list changed on another device. It updates when you finish here, or tap Refresh.');
      var b = banner();
      if (b && !b.querySelector('button')) {
        var btn = document.createElement('button');
        btn.type = 'button'; btn.textContent = 'Refresh';
        btn.addEventListener('click', function () { refresh(true); });
        b.appendChild(document.createTextNode(' ')); b.appendChild(btn);
      }
      return Promise.resolve();
    }
    return fetch(location.href, { credentials: 'same-origin', headers: { 'X-Fetch': '1' } }).then(function (r) {
      if (!r.ok || /\/login/.test(r.url)) { throw new Error('not signed in'); }
      return r.text();
    }).then(function (html) {
      var doc = new DOMParser().parseFromString(html, 'text/html');
      var fresh = doc.querySelector('[data-listroot]');
      if (!fresh) { return; }
      root.innerHTML = fresh.innerHTML;
      root.setAttribute('data-version', fresh.getAttribute('data-version'));
      stale = false;
      markPending();
      status();
    }).catch(function () { /* offline or signed out: keep what is shown */ });
  }

  function post(op) {
    return fetch(op.url, {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'X-Fetch': '1' },
      body: op.body
    });
  }

  // flush sends queued changes in order. A network failure stops it (try again later); a refusal
  // from the server (the item is gone, bad input) drops that change.
  function flush() {
    if (busy || !navigator.onLine) { return Promise.resolve(); }
    var q = load();
    if (!q.length) { return Promise.resolve(); }
    busy = true;
    var dropped = 0;
    function next() {
      q = load();
      if (!q.length) { return Promise.resolve(); }
      return post(q[0]).then(function (r) {
        if (/\/login/.test(r.url)) { throw new Error('signed out'); }
        if (!r.ok) { dropped++; }
        q.shift(); save(q);
        return next();
      });
    }
    return next().then(function () {
      busy = false;
      return refresh(true).then(function () {
        if (dropped) { say(dropped + ' change' + (dropped === 1 ? ' was' : 's were') + ' not applied (the item was removed or changed elsewhere).'); }
      });
    }).catch(function () { busy = false; status(); });
  }

  // Send "Have it", "Bought" and "Move back" without leaving the page.
  document.addEventListener('submit', function (e) {
    var f = e.target;
    if (!f.closest || !f.closest('[data-listroot]') || f.method.toLowerCase() !== 'post') { return; }
    var path = new URL(f.action, location.href).pathname;
    if (!ACTION.test(path) || f.hasAttribute('data-confirm')) { return; }
    e.preventDefault();
    var body = new URLSearchParams(new FormData(f));
    if (/\/purchase$/.test(path) && !body.has('op')) { body.set('op', uid()); }
    var op = { url: path, body: body.toString() };
    var q = load();
    if (q.length || !navigator.onLine) { queue(op); return; }
    post(op).then(function (r) {
      if (/\/login/.test(r.url)) { window.location.href = '/login'; return; }
      if (r.ok) { return refresh(true); }
      return r.text().then(function (t) { window.alert(r.status === 400 && t && t.length < 200 ? t : 'That change could not be saved.'); return refresh(true); });
    }).catch(function () { queue(op); });
  });

  function queue(op) {
    var q = load(); q.push(op); save(q);
    markPending();
    status();
  }

  window.addEventListener('online', function () { flush().then(function () { return poll(); }); });
  window.addEventListener('offline', status);

  // Poll for changes made on other devices.
  function poll() {
    if (document.hidden || !navigator.onLine || busy || load().length) { return Promise.resolve(); }
    return fetch('/list/version', { credentials: 'same-origin' }).then(function (r) {
      if (!r.ok) { return; }
      return r.text().then(function (v) {
        if (v.trim() !== root.getAttribute('data-version')) { return refresh(false); }
      });
    }).catch(function () {});
  }
  setInterval(poll, 8000);
  document.addEventListener('visibilitychange', function () { if (!document.hidden) { flush().then(poll); } });

  markPending();
  status();
  flush();
})();
