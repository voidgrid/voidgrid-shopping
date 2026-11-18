// Passkeys (WebAuthn): register on the Account page, sign in on the login pages.
(function () {
  function b64uToBuf(s) {
    s = s.replace(/-/g, '+').replace(/_/g, '/');
    while (s.length % 4) { s += '='; }
    var bin = atob(s), buf = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) { buf[i] = bin.charCodeAt(i); }
    return buf.buffer;
  }
  function bufToB64u(b) {
    var bytes = new Uint8Array(b), s = '';
    for (var i = 0; i < bytes.length; i++) { s += String.fromCharCode(bytes[i]); }
    return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  }
  function post(url, body) {
    return fetch(url, {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: body === undefined ? '{}' : JSON.stringify(body)
    }).then(function (r) { return r.json().then(function (j) { return { ok: r.ok, json: j }; }); });
  }
  function show(msg) {
    var e = document.querySelector('[data-passkey-error]');
    if (e) { e.textContent = msg; e.hidden = false; } else { window.alert(msg); }
  }
  function supported() { return !!(window.PublicKeyCredential && navigator.credentials); }

  function creationOptions(o) {
    var pk = o.publicKey;
    pk.challenge = b64uToBuf(pk.challenge);
    pk.user.id = b64uToBuf(pk.user.id);
    (pk.excludeCredentials || []).forEach(function (c) { c.id = b64uToBuf(c.id); });
    return o;
  }
  function requestOptions(o) {
    var pk = o.publicKey;
    pk.challenge = b64uToBuf(pk.challenge);
    (pk.allowCredentials || []).forEach(function (c) { c.id = b64uToBuf(c.id); });
    return o;
  }

  document.addEventListener('click', function (e) {
    var reg = e.target.closest('[data-passkey-register]');
    var login = e.target.closest('[data-passkey-login]');
    if (!reg && !login) { return; }
    if (!supported()) { show('This browser does not support passkeys.'); return; }
    if (reg) {
      var nameEl = document.getElementById('passkey-name');
      var name = nameEl ? nameEl.value : '';
      post('/account/passkeys/begin').then(function (r) {
        if (!r.ok) { throw new Error(r.json.error || 'Could not start.'); }
        return navigator.credentials.create(creationOptions(r.json));
      }).then(function (c) {
        return post('/account/passkeys/finish?name=' + encodeURIComponent(name), {
          id: c.id, rawId: bufToB64u(c.rawId), type: c.type,
          response: {
            attestationObject: bufToB64u(c.response.attestationObject),
            clientDataJSON: bufToB64u(c.response.clientDataJSON),
            transports: c.response.getTransports ? c.response.getTransports() : []
          }
        });
      }).then(function (r) {
        if (!r.ok) { throw new Error(r.json.error || 'Could not save the passkey.'); }
        window.location.reload();
      }).catch(function (err) { show(err && err.message ? err.message : 'The passkey was not added.'); });
    } else {
      post('/login/passkey/begin').then(function (r) {
        if (!r.ok) { throw new Error(r.json.error || 'Could not start.'); }
        return navigator.credentials.get(requestOptions(r.json));
      }).then(function (c) {
        return post('/login/passkey/finish', {
          id: c.id, rawId: bufToB64u(c.rawId), type: c.type,
          response: {
            authenticatorData: bufToB64u(c.response.authenticatorData),
            clientDataJSON: bufToB64u(c.response.clientDataJSON),
            signature: bufToB64u(c.response.signature),
            userHandle: c.response.userHandle ? bufToB64u(c.response.userHandle) : ''
          }
        });
      }).then(function (r) {
        if (!r.ok) { throw new Error(r.json.error || 'That passkey was not accepted.'); }
        window.location.href = r.json.redirect || '/recipes';
      }).catch(function (err) { show(err && err.message ? err.message : 'Sign-in with a passkey did not work.'); });
    }
  });
  document.addEventListener('focusin', function (e) {
    if (e.target.matches && e.target.matches('[data-select]')) { e.target.select(); }
  });
})();
