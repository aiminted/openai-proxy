(function () {
  var container = document.getElementById('recent');
  if (!container) return;
  var keyId = container.getAttribute('data-key-id');
  if (!keyId) return;

  function el(tag, text) {
    var n = document.createElement(tag);
    if (text !== undefined && text !== null) n.textContent = String(text);
    return n;
  }

  function render(rows) {
    container.replaceChildren();
    if (!rows || !rows.length) {
      var p = el('p', '아직 요청이 없습니다.');
      p.className = 'empty';
      container.appendChild(p);
      return;
    }
    var table = document.createElement('table');
    var thead = document.createElement('thead');
    var hr = document.createElement('tr');
    ['time', 'endpoint', 'model', 'in', 'out', 'cost', 'status', 'ms'].forEach(function (h) {
      hr.appendChild(el('th', h));
    });
    thead.appendChild(hr);
    table.appendChild(thead);
    var tbody = document.createElement('tbody');
    rows.forEach(function (r) {
      var tr = document.createElement('tr');
      tr.appendChild(el('td', r.created_at));
      var endpointCell = document.createElement('td');
      endpointCell.appendChild(el('code', r.endpoint));
      tr.appendChild(endpointCell);
      tr.appendChild(el('td', r.model || '—'));
      tr.appendChild(el('td', r.input_tokens));
      tr.appendChild(el('td', r.output_tokens));
      tr.appendChild(el('td', '$' + Number(r.cost_usd).toFixed(4)));
      tr.appendChild(el('td', r.status));
      tr.appendChild(el('td', r.duration_ms));
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    container.appendChild(table);
  }

  fetch('/admin/api/usage/' + encodeURIComponent(keyId) + '/recent?limit=50', {
    credentials: 'same-origin'
  })
    .then(function (r) {
      if (!r.ok) throw new Error('http ' + r.status);
      return r.json();
    })
    .then(render)
    .catch(function (err) {
      container.replaceChildren();
      var p = el('p', '불러오기 실패: ' + err.message);
      p.className = 'empty';
      container.appendChild(p);
    });
})();
