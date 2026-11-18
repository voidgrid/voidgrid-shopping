// Offline support for the shopping list only. It keeps the app's static files and the last copy of
// the list page; every other page always comes from the network. Check-offs made offline are queued
// by sync.js, not here. The cache is emptied when you sign out.
var CACHE = 'vgs-list-v1';
var SHELL = ['/static/app.css', '/static/app.js', '/static/sync.js', '/static/favicon.svg'];

self.addEventListener('install', function (e) {
  e.waitUntil(caches.open(CACHE).then(function (c) { return c.addAll(SHELL); }).then(function () { return self.skipWaiting(); }));
});

self.addEventListener('activate', function (e) {
  e.waitUntil(caches.keys().then(function (keys) {
    return Promise.all(keys.filter(function (k) { return k !== CACHE; }).map(function (k) { return caches.delete(k); }));
  }).then(function () { return self.clients.claim(); }));
});

function withTimeout(p, ms) {
  return new Promise(function (resolve, reject) {
    var t = setTimeout(function () { reject(new Error('timeout')); }, ms);
    p.then(function (r) { clearTimeout(t); resolve(r); }, function (e) { clearTimeout(t); reject(e); });
  });
}

self.addEventListener('fetch', function (e) {
  var req = e.request;
  if (req.method !== 'GET') { return; }
  var url = new URL(req.url);
  if (url.origin !== self.location.origin) { return; }

  if (url.pathname.indexOf('/static/') === 0) {
    // static files: answer from the cache, refresh it in the background
    e.respondWith(caches.open(CACHE).then(function (c) {
      return c.match(req).then(function (hit) {
        var net = fetch(req).then(function (r) { if (r.ok) { c.put(req, r.clone()); } return r; }).catch(function () { return hit; });
        return hit || net;
      });
    }));
    return;
  }

  if (url.pathname === '/list') {
    // the list page: the network when it answers, otherwise the last copy
    e.respondWith(caches.open(CACHE).then(function (c) {
      return withTimeout(fetch(req), req.mode === 'navigate' ? 5000 : 15000).then(function (r) {
        // a sign-in redirect or an error is never stored as the list
        if (r.ok && !r.redirected) { c.put(new Request(url.pathname + url.search), r.clone()); }
        return r;
      }).catch(function () {
        return c.match(new Request(url.pathname + url.search)).then(function (hit) {
          return hit || c.match(new Request('/list')).then(function (any) {
            return any || new Response('You are offline and no copy of the list is saved yet. Open the list once while online.',
              { status: 503, headers: { 'Content-Type': 'text/plain; charset=utf-8' } });
          });
        });
      });
    }));
  }
});
