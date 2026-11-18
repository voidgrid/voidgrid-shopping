// Small helpers: print buttons and confirmation prompts. No inline script, per the CSP.
document.addEventListener('click', function (e) {
  var t = e.target.closest('[data-print]');
  if (t) { window.print(); }
});
document.addEventListener('submit', function (e) {
  var msg = e.target.getAttribute && e.target.getAttribute('data-confirm');
  if (msg && !window.confirm(msg)) { e.preventDefault(); }
});

// Planner drag and drop: drag a recipe from the list, or a planned entry, onto a meal.
(function () {
  var dragged = null;
  function zoneOf(e) { return e.target.closest ? e.target.closest('.slot[data-date]') : null; }
  document.addEventListener('dragstart', function (e) {
    var el = e.target.closest && e.target.closest('[data-entry],[data-recipe]');
    if (!el) { return; }
    dragged = { entry: el.getAttribute('data-entry'), recipe: el.getAttribute('data-recipe') };
    e.dataTransfer.effectAllowed = 'move';
    e.dataTransfer.setData('text/plain', 'planner');
  });
  document.addEventListener('dragend', function () {
    dragged = null;
    document.querySelectorAll('.slot.over').forEach(function (z) { z.classList.remove('over'); });
  });
  document.addEventListener('dragover', function (e) {
    var z = zoneOf(e);
    if (z && dragged) { e.preventDefault(); z.classList.add('over'); }
  });
  document.addEventListener('dragleave', function (e) {
    var z = zoneOf(e);
    if (z && !z.contains(e.relatedTarget)) { z.classList.remove('over'); }
  });
  document.addEventListener('drop', function (e) {
    var z = zoneOf(e);
    if (!z || !dragged) { return; }
    e.preventDefault();
    var body = new URLSearchParams({ date: z.getAttribute('data-date'), slot: z.getAttribute('data-slot') });
    var url = '/planner/entries';
    if (dragged.entry) { url += '/' + dragged.entry; } else { body.set('recipe_id', dragged.recipe); }
    dragged = null;
    fetch(url, {
      method: 'POST', credentials: 'same-origin',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded', 'X-Fetch': '1' },
      body: body.toString()
    }).then(function (r) {
      if (r.ok) { window.location.reload(); } else { window.alert('That change could not be saved.'); }
    }).catch(function () { window.alert('That change could not be saved.'); });
  });
})();

// Recipe form: ingredient rows (add, add heading, move, remove, Enter for a new row, bulk tab).
(function () {
  var box = document.querySelector('[data-rows]');
  if (!box) { return; }
  var list = box.querySelector('.rowlist');
  var mode = box.querySelector('[name=ing_mode]');
  function make(kind) {
    var t = box.querySelector('template[data-tpl="' + kind + '"]');
    return t.content.firstElementChild.cloneNode(true);
  }
  function add(kind, after) {
    var row = make(kind);
    if (after && after.nextSibling) { list.insertBefore(row, after.nextSibling); } else { list.appendChild(row); }
    var f = row.querySelector('input[type=text]');
    if (f) { f.focus(); }
    return row;
  }
  box.addEventListener('click', function (e) {
    var b = e.target.closest('button');
    if (!b || !box.contains(b)) { return; }
    var row = b.closest('.row');
    if (b.hasAttribute('data-add')) { add(b.getAttribute('data-add'), null); return; }
    if (b.hasAttribute('data-del') && row) {
      if (list.children.length > 1) { row.remove(); } else {
        row.querySelectorAll('input[type=text]').forEach(function (i) { i.value = ''; });
      }
      return;
    }
    if (b.hasAttribute('data-move') && row) {
      var d = parseInt(b.getAttribute('data-move'), 10);
      var other = d < 0 ? row.previousElementSibling : row.nextElementSibling;
      if (other) { if (d < 0) { list.insertBefore(row, other); } else { list.insertBefore(other, row); } }
      return;
    }
    if (b.hasAttribute('data-tab')) {
      var bulk = b.getAttribute('data-tab') === 'bulk';
      var ta = box.querySelector('[data-pane=bulk] textarea');
      if (bulk) {
        var lines = [];
        list.querySelectorAll('.row').forEach(function (r) {
          var v = function (n) { return (r.querySelector('[name=' + n + ']') || { value: '' }).value.trim(); };
          var t;
          if (r.classList.contains('sep')) { t = v('ing_n'); if (t) { t = t.replace(/:$/, '') + ':'; } } else {
            t = [v('ing_a'), v('ing_u'), v('ing_n')].filter(Boolean).join(' ');
            if (v('ing_x')) { t += (t ? ', ' : '') + v('ing_x'); }
          }
          if (t) { lines.push(t); }
        });
        ta.value = lines.join('\n');
      }
      ta.disabled = !bulk;
      list.querySelectorAll('input').forEach(function (i) { i.disabled = bulk; });
      box.querySelector('[data-pane=lines]').hidden = bulk;
      box.querySelector('[data-pane=bulk]').hidden = !bulk;
      mode.value = bulk ? 'bulk' : 'lines';
      box.querySelectorAll('.tab').forEach(function (t) { t.classList.toggle('on', t === b); });
    }
  });
  box.addEventListener('keydown', function (e) {
    var row = e.target.closest && e.target.closest('.row');
    if (e.key === 'Enter' && row && e.target.tagName === 'INPUT') {
      e.preventDefault();
      add(row.classList.contains('sep') ? 'i' : 'i', row);
    }
  });
})();
