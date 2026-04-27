(function () {
  "use strict";

  // ---------- helpers ----------
  function el(tag, text, attrs) {
    var n = document.createElement(tag);
    if (text != null) n.textContent = String(text);
    if (attrs) for (var k in attrs) n.setAttribute(k, attrs[k]);
    return n;
  }
  function relTime(iso) {
    if (!iso) return "—";
    var t = new Date(iso).getTime();
    if (isNaN(t)) return iso;
    var diff = Math.round((Date.now() - t) / 1000);
    var abs = Math.abs(diff);
    if (abs < 60) return diff < 0 ? "곧" : "방금";
    if (abs < 3600) return Math.round(abs / 60) + "분 " + (diff < 0 ? "후" : "전");
    if (abs < 86400) return Math.round(abs / 3600) + "시간 " + (diff < 0 ? "후" : "전");
    if (abs < 86400 * 30) return Math.round(abs / 86400) + "일 " + (diff < 0 ? "후" : "전");
    return new Date(iso).toLocaleDateString();
  }

  // ---------- copy buttons ----------
  document.querySelectorAll("[data-copy]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var sel = btn.getAttribute("data-copy");
      var target = document.querySelector(sel);
      if (!target) return;
      var text = target.textContent || "";
      navigator.clipboard.writeText(text).then(function () {
        var prev = btn.textContent;
        btn.textContent = "copied";
        btn.classList.add("ok");
        setTimeout(function () { btn.textContent = prev; btn.classList.remove("ok"); }, 1200);
      }).catch(function () { btn.textContent = "copy 실패"; });
    });
  });

  // ---------- confirm-on-submit ----------
  document.querySelectorAll("[data-confirm]").forEach(function (btn) {
    btn.addEventListener("click", function (e) {
      var msg = btn.getAttribute("data-confirm");
      if (!confirm(msg)) e.preventDefault();
    });
  });

  // ---------- expiry presets ----------
  document.querySelectorAll(".presets").forEach(function (group) {
    var target = document.querySelector(group.getAttribute("data-target"));
    if (!target) return;
    group.querySelectorAll("button").forEach(function (b) {
      b.addEventListener("click", function () {
        var days = b.getAttribute("data-days");
        if (!days) { target.value = ""; return; }
        var d = new Date();
        d.setDate(d.getDate() + parseInt(days, 10));
        // YYYY-MM-DD
        target.value = d.toISOString().slice(0, 10);
      });
    });
  });

  // ---------- search + active filter ----------
  var search = document.getElementById("key-search");
  var hideInactive = document.getElementById("hide-inactive");
  var tbody = document.getElementById("keys-tbody");
  var emptyFiltered = document.getElementById("empty-filtered");

  function applyFilter() {
    if (!tbody) return;
    var q = (search && search.value || "").trim().toLowerCase();
    var hideOff = !!(hideInactive && hideInactive.checked);
    var visible = 0;
    tbody.querySelectorAll("tr").forEach(function (row) {
      if (!row.dataset || !row.dataset.search) return; // empty-state row
      var matchesText = !q || row.dataset.search.toLowerCase().indexOf(q) !== -1;
      var matchesActive = !hideOff || row.dataset.active === "true";
      var show = matchesText && matchesActive;
      row.style.display = show ? "" : "none";
      if (show) visible++;
    });
    if (emptyFiltered) emptyFiltered.classList.toggle("hidden", visible !== 0 || tbody.children.length === 0);
  }
  if (search) search.addEventListener("input", applyFilter);
  if (hideInactive) hideInactive.addEventListener("change", applyFilter);
  applyFilter();

  // ---------- recent requests on key detail ----------
  var recent = document.getElementById("recent");
  if (recent) {
    var keyId = recent.getAttribute("data-key-id");
    fetch("/admin/api/usage/" + encodeURIComponent(keyId) + "/recent?limit=50",
          { credentials: "same-origin" })
      .then(function (r) { if (!r.ok) throw new Error("http " + r.status); return r.json(); })
      .then(function (rows) {
        recent.replaceChildren();
        if (!rows || !rows.length) {
          var p = el("p", "아직 요청이 없습니다."); p.className = "empty";
          recent.appendChild(p); return;
        }
        var table = el("table"); table.className = "recent-table";
        var thead = el("thead"); var hr = el("tr");
        ["time", "endpoint", "model", "in", "out", "cost", "status", "ms"].forEach(function (h) {
          hr.appendChild(el("th", h));
        });
        thead.appendChild(hr); table.appendChild(thead);
        var tb = el("tbody");
        rows.forEach(function (r) {
          var tr = el("tr");
          var t = el("td", relTime(r.created_at)); t.title = r.created_at; tr.appendChild(t);
          var ec = el("td"); ec.appendChild(el("code", r.endpoint)); tr.appendChild(ec);
          tr.appendChild(el("td", r.model || "—"));
          var i1 = el("td", r.input_tokens); i1.className = "num"; tr.appendChild(i1);
          var i2 = el("td", r.output_tokens); i2.className = "num"; tr.appendChild(i2);
          var i3 = el("td", "$" + Number(r.cost_usd).toFixed(6)); i3.className = "num"; tr.appendChild(i3);
          var st = el("td", r.status); st.className = "num " + (r.status >= 400 ? "danger" : ""); tr.appendChild(st);
          var ms = el("td", r.duration_ms); ms.className = "num"; tr.appendChild(ms);
          tb.appendChild(tr);
        });
        table.appendChild(tb);
        recent.appendChild(table);
      })
      .catch(function (err) {
        recent.replaceChildren();
        var p = el("p", "불러오기 실패: " + err.message); p.className = "empty";
        recent.appendChild(p);
      });
  }

  // ---------- inline edit (PATCH via fetch) ----------
  var form = document.getElementById("edit-form");
  if (form) {
    var status = document.getElementById("edit-status");
    form.addEventListener("submit", function (e) {
      e.preventDefault();
      if (status) { status.textContent = "저장 중…"; status.classList.remove("danger", "ok"); }
      var fd = new FormData(form);
      var body = {
        owner: (fd.get("owner") || "").trim(),
        note: fd.get("note") || "",
      };
      var exp = (fd.get("expires_at") || "").trim();
      if (exp) {
        // datetime-local is YYYY-MM-DDTHH:MM in local time. Convert to UTC ISO.
        body.expires_at = new Date(exp).toISOString();
      } else {
        body.expires_at = null;
      }
      function num(name, asInt) {
        var v = (fd.get(name) || "").trim();
        if (!v) return null;
        var n = asInt ? parseInt(v, 10) : parseFloat(v);
        return isNaN(n) ? null : n;
      }
      body.rpm_limit = num("rpm_limit", true);
      body.token_quota = num("token_quota", true);
      body.dollar_quota = num("dollar_quota", false);

      var keyId = form.getAttribute("data-key-id");
      fetch("/admin/api/keys/" + encodeURIComponent(keyId), {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        credentials: "same-origin",
        body: JSON.stringify(body),
      }).then(function (r) {
        if (r.status === 204) {
          if (status) { status.textContent = "저장됨"; status.classList.add("ok"); }
        } else {
          return r.text().then(function (t) {
            if (status) { status.textContent = "오류 " + r.status + ": " + t.slice(0, 120); status.classList.add("danger"); }
          });
        }
      }).catch(function (err) {
        if (status) { status.textContent = "네트워크 오류: " + err.message; status.classList.add("danger"); }
      });
    });
  }
})();
