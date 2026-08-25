/*
 * Gerege Client — гэрээний дэлгэц.
 *
 * FRAMEWORK-ГҮЙ нь санаатай. Энэ хуудас бүрхүүлээс (цөмийн Next.js апп)
 * ГАДУУР амьдардаг тул түүний build-ээс юу ч авч чадахгүй, ба платформын CSP
 * нь `script-src 'self'` — CDN ажиллахгүй. Нэг файл, нэг fetch давхарга,
 * hash-аар зам.
 *
 * ЯАГААД БҮРХҮҮЛИЙН ДОТОР БИШ ВЭ. Цөмийн frontend нь модулийн дэлгэцийг
 * ерөнхийлөн зурдаг механизмгүй: дэлгэц бүр тэр репод гараар бичигддэг ба
 * v1.13.0-д гэрээний дэлгэц байхгүй. Бид цөмийн репог хөндөхгүй тул зам нь
 * ганц: модуль өөрөө HTML үйлчилнэ. iframe ч сонголт биш — бүрхүүл
 * `X-Frame-Options: DENY` явуулдаг.
 *
 * Замууд:
 *   #/            миний гэрээнүүд        #/new         шинэ гэрээ
 *   #/c/<id>      гэрээ (гаргагчийн нүд)  #/inbox       надад ирсэн
 *   #/inbox/<pid> ирсэн гэрээ (нэг)
 */
"use strict";

var API = "/api/v1/documents";
var app = document.getElementById("app");
var poll = null;

/* ------------------------------------------------------------- туслахууд */

function h(tag, attrs, children) {
  var el = document.createElement(tag);
  if (attrs) Object.keys(attrs).forEach(function (k) {
    if (k === "text") el.textContent = attrs[k];
    else if (k.slice(0, 2) === "on") el.addEventListener(k.slice(2), attrs[k]);
    else if (attrs[k] !== null && attrs[k] !== undefined && attrs[k] !== false) el.setAttribute(k, attrs[k]);
  });
  (children || []).forEach(function (c) { if (c) el.appendChild(c); });
  return el;
}

/* api нь ганц fetch давхарга.
 *
 * 401 дээр нэвтрэх дэлгэц рүү явуулна — энэ хуудас бүрхүүлээс гадуур тул
 * бүрхүүлийн дахин нэвтрүүлэх логик үүнд хүрэхгүй. */
function api(path, opts) {
  opts = opts || {};
  opts.headers = Object.assign({ "Content-Type": "application/json" }, opts.headers || {});
  opts.credentials = "same-origin";
  return fetch(path, opts).then(function (res) {
    if (res.status === 401) {
      location.assign("/login?next=" + encodeURIComponent(location.pathname + location.hash));
      throw new Error("нэвтрээгүй");
    }
    if (res.status === 204) return {};
    return res.json().catch(function () { return {}; }).then(function (body) {
      if (!res.ok) throw new Error(body.error || body.message || ("алдаа " + res.status));
      return body;
    });
  });
}

function stopPolling() { if (poll) { clearTimeout(poll); poll = null; } }

var CONTRACT_STATE = {
  NONE: "—", DRAFT: "Ноорог", SENT: "Илгээгдсэн", PARTIALLY_SIGNED: "Хэсэгчлэн зурагдсан",
  EXECUTED: "Хүчин төгөлдөр", DECLINED: "Татгалзсан", WITHDRAWN: "Эргүүлж татсан",
  EXPIRED: "Хугацаа дууссан", TERMINATED: "Цуцлагдсан"
};
var PARTY_STATE = {
  draft: "Ноорог", invited: "Илгээгдсэн", viewed: "Уншсан", signed: "Гарын үсэг зурсан",
  declined: "Татгалзсан", withdrawn: "Эргүүлж татсан", expired: "Хугацаа дууссан"
};
var PARTY_ROLE = { issuer: "Гаргагч", counterparty: "Тал", witness: "Гэрч", guarantor: "Батлан даагч" };
var PARTY_KIND = {
  member: "Дотоод хэрэглэгч", tenant: "Энэ платформ дээрх байгууллага",
  peer: "Өөр суулгац", person: "Иргэн (дансгүй)", organisation: "Байгууллага (дансгүй)"
};

function badge(state, map) { return h("span", { class: "badge " + state, text: (map || CONTRACT_STATE)[state] || state }); }
function fmtDate(s) { if (!s) return ""; var d = new Date(s); return isNaN(d) ? s : d.toLocaleDateString("mn-MN"); }
function fmtWhen(s) { if (!s) return ""; var d = new Date(s); return isNaN(d) ? s : d.toLocaleString("mn-MN"); }
function alertBox(kind, text) { return h("div", { class: "alert " + kind, text: text }); }
function show(node) { app.innerHTML = ""; app.appendChild(node); }
function fail(err) { show(h("div", {}, [alertBox("err", err.message || String(err))])); }

/* Мөнгө нь дэлгэцэд гарахдаа уншигдахуйц байх ёстой: 15000000 гэдэг тоо
   хүнд утга хэлэхгүй. */
function fmtMoney(amount, currency) {
  if (amount === null || amount === undefined) return "";
  var n = Number(amount);
  if (isNaN(n)) return "";
  return n.toLocaleString("mn-MN") + (currency ? " " + currency : "");
}

/* --------------------------------------------------------------- ёслол
 *
 * PIN2 ёслолын нэг л дүрэм: АСУУСААР БАЙХ. Гарын үсэг нь poll-ийн ДОТОР
 * бүртгэгддэг тул хэн ч асуухгүй бол иргэний зөвшөөрсөн гарын үсэг хэзээ ч
 * бичигдэхгүй. 3 секунд тутам, дуустал. */
function ceremony(startPath, pollPath, onDone) {
  var box = h("div", { class: "card" }, [h("p", { class: "muted", text: "Утас руу хүсэлт илгээж байна…" })]);
  show(box);
  api(startPath, { method: "POST", body: "{}" }).then(function (s) {
    box.innerHTML = "";
    box.appendChild(h("h2", { text: "Утсаа шалгана уу" }));
    box.appendChild(h("p", { class: "muted", text: "eID апп дээрээ баталгаажуулах код таарч байгаа эсэхийг хараад PIN2 кодоо оруулна уу." }));
    if (s.verification_code) box.appendChild(h("div", { class: "vcode", text: s.verification_code }));
    var status = alertBox("wait", "Хүлээгдэж байна…");
    box.appendChild(status);

    var tick = function () {
      api(pollPath, { method: "POST", body: "{}" }).then(function (p) {
        if (p.state === "COMPLETE") { stopPolling(); onDone(); return; }
        if (p.state === "PENDING") { poll = setTimeout(tick, 3000); return; }
        stopPolling();
        status.className = "alert err";
        status.textContent = p.state === "REFUSED" ? "Иргэн татгалзлаа." : "Ёслол дуусав: " + p.state;
      }).catch(function (err) {
        stopPolling();
        status.className = "alert err";
        status.textContent = err.message;
      });
    };
    poll = setTimeout(tick, 3000);
  }).catch(fail);
}

/* ═══════════════════════════════════════════ 1. МИНИЙ ГЭРЭЭНҮҮД */

function viewList() {
  api(API + "/contracts").then(function (data) {
    var list = data.contracts || [];
    var wrap = h("div", {}, [
      h("div", { class: "row" }, [
        h("h1", { class: "grow", text: "Миний гэрээнүүд" }),
        h("a", { class: "btn primary", href: "#/new", text: "Шинэ гэрээ" })
      ])
    ]);
    if (!list.length) {
      wrap.appendChild(h("div", { class: "card" }, [
        h("div", { class: "empty" }, [
          h("p", { text: "Хараахан гэрээ байхгүй байна." }),
          h("p", { class: "muted", text: "«Шинэ гэрээ» дарж гарчиг өгөөд, бичвэрээ бичиж, талуудаа нэрлээд илгээнэ." })
        ])
      ]));
      return show(wrap);
    }
    var rows = list.map(function (c) {
      return h("tr", { class: "click", onclick: function () { location.hash = "#/c/" + c.id; } }, [
        h("td", {}, [
          h("div", { text: c.title }),
          c.contract_number ? h("div", { class: "muted", style: "font-size:12.5px", text: "№ " + c.contract_number }) : null
        ]),
        h("td", { text: c.counterparties || "—" }),
        h("td", {}, [badge(c.contract_state)]),
        h("td", { text: c.signed_count + " / " + c.required_count }),
        h("td", { text: fmtMoney(c.amount, c.currency) }),
        h("td", { class: "muted", text: fmtDate(c.sent_at || c.created_at) })
      ]);
    });
    wrap.appendChild(h("div", { class: "card" }, [
      h("table", {}, [
        h("thead", {}, [h("tr", {}, [
          h("th", { text: "Гэрээ" }), h("th", { text: "Талууд" }), h("th", { text: "Төлөв" }),
          h("th", { text: "Гарын үсэг" }), h("th", { text: "Дүн" }), h("th", { text: "Огноо" })
        ])]),
        h("tbody", {}, rows)
      ])
    ]));
    show(wrap);
  }).catch(fail);
}

function viewNew() {
  var title = h("input", { placeholder: "Жишээ нь: Түншлэлийн гэрээ" });
  var msg = h("div", {});
  var save = h("button", { class: "primary", text: "Үүсгэх", onclick: function () {
    if (!title.value.trim()) { msg.innerHTML = ""; msg.appendChild(alertBox("err", "Гарчиг бичнэ үү")); return; }
    save.disabled = true;
    api(API + "/", { method: "POST", body: JSON.stringify({ title: title.value.trim(), doc_type: "CONTRACT" }) })
      .then(function (doc) { location.hash = "#/c/" + doc.id; })
      .catch(function (e) { save.disabled = false; msg.innerHTML = ""; msg.appendChild(alertBox("err", e.message)); });
  } });
  show(h("div", {}, [
    h("h1", { text: "Шинэ гэрээ" }),
    h("div", { class: "card" }, [
      h("label", { text: "Гэрээний гарчиг" }), title,
      h("p", { class: "hint", text: "Гарчгийг дараа ч засаж болно. Талууд нэмэгдэх агшинд энэ баримт гэрээ болно." }),
      msg,
      h("div", { class: "row", style: "margin-top:12px" }, [save, h("a", { class: "btn ghost", href: "#/", text: "Болих" })])
    ])
  ]));
}

/* ═══════════════════════════════════════════ 2. ГЭРЭЭ (ГАРГАГЧИЙН НҮД) */

function viewContract(id) {
  Promise.all([
    api(API + "/" + id + "/parties"),
    api(API + "/" + id + "/body").catch(function () { return { body: "" }; }),
    api(API + "/contracts")
  ]).then(function (r) {
    var shape = r[0], body = r[1].body || "", meta = null;
    (r[2].contracts || []).forEach(function (c) { if (c.id === id) meta = c; });
    renderContract(id, shape, body, meta);
  }).catch(fail);
}

function renderContract(id, shape, body, meta) {
  var parties = shape.parties || [];
  var state = shape.contract_state || "DRAFT";
  var editable = state === "DRAFT" || state === "NONE";

  var wrap = h("div", {}, [
    h("div", { class: "row" }, [
      h("a", { class: "plain", href: "#/", text: "← Гэрээнүүд" })
    ]),
    h("div", { class: "row", style: "margin-top:6px" }, [
      h("h1", { class: "grow", text: shape.title || (meta && meta.title) || "Гэрээ" }),
      badge(state)
    ])
  ]);

  /* — Талуудын явц. Гэрээ хаана байгааг НЭГ харцаар. */
  if (parties.length) {
    var steps = h("div", { class: "steps" }, parties.map(function (p) {
      return h("span", { class: "step" }, [
        h("span", { class: "dot " + p.state }),
        h("span", { text: p.display_name }),
        h("span", { class: "muted", text: PARTY_STATE[p.state] || p.state })
      ]);
    }));
    wrap.appendChild(steps);
  }

  wrap.appendChild(sectionFacts(id, meta, editable));
  wrap.appendChild(sectionBody(id, body, editable));
  wrap.appendChild(sectionParties(id, parties, state));
  wrap.appendChild(sectionSend(id, parties, state, shape.mode));
  show(wrap);
}

/* — Гэрээний хэрэг баримт */
function sectionFacts(id, meta, editable) {
  var num = h("input", { value: (meta && meta.contract_number) || "", placeholder: "2026/01" });
  var amount = h("input", { type: "number", step: "0.01", value: (meta && meta.amount) || "" });
  var currency = h("input", { value: (meta && meta.currency) || "MNT", maxlength: "3" });
  var from = h("input", { type: "date", value: (meta && meta.effective_from) ? String(meta.effective_from).slice(0, 10) : "" });
  var to = h("input", { type: "date", value: (meta && meta.effective_to) ? String(meta.effective_to).slice(0, 10) : "" });
  var due = h("input", { type: "date", value: (meta && meta.due_at) ? String(meta.due_at).slice(0, 10) : "" });
  var msg = h("div", {});

  var save = h("button", { class: "ghost small", text: "Хадгалах", onclick: function () {
    save.disabled = true;
    api(API + "/" + id + "/contract", { method: "PUT", body: JSON.stringify({
      contract_number: num.value.trim(),
      amount: amount.value === "" ? null : Number(amount.value),
      currency: currency.value.trim(),
      effective_from: from.value, effective_to: to.value,
      due_at: due.value ? due.value + "T23:59:59Z" : ""
    }) }).then(function () {
      save.disabled = false; msg.innerHTML = ""; msg.appendChild(alertBox("ok", "Хадгаллаа."));
    }).catch(function (e) { save.disabled = false; msg.innerHTML = ""; msg.appendChild(alertBox("err", e.message)); });
  } });

  return h("div", { class: "card" }, [
    h("div", { class: "row" }, [h("h2", { class: "grow", text: "Гэрээний мэдээлэл" }), save]),
    h("div", { class: "grid2" }, [
      h("div", {}, [h("label", { text: "Гэрээний дугаар" }), num]),
      h("div", {}, [h("label", { text: "Хариу өгөх эцсийн хугацаа" }), due]),
      h("div", {}, [h("label", { text: "Дүн" }), amount]),
      h("div", {}, [h("label", { text: "Валют" }), currency]),
      h("div", {}, [h("label", { text: "Хүчинтэй эхлэх" }), from]),
      h("div", {}, [h("label", { text: "Хүчинтэй дуусах" }), to])
    ]),
    msg
  ]);
}

/* — Гэрээний бичвэр */
function sectionBody(id, body, editable) {
  var area = h("textarea", { placeholder: "Гэрээний бичвэрээ энд бичнэ. {{СУРГУУЛЬ}}, {{ЗАХИРАЛ}} мэтийн орлуулга тал бүрийн хувь дээр автоматаар бөглөгдөнө." });
  area.value = body;
  var msg = h("div", {});
  var save = h("button", { class: "ghost small", text: "Хадгалах", onclick: function () {
    save.disabled = true;
    api(API + "/" + id + "/body", { method: "PUT", body: JSON.stringify({ body: area.value }) })
      .then(function () { save.disabled = false; msg.innerHTML = ""; msg.appendChild(alertBox("ok", "Хадгаллаа.")); })
      .catch(function (e) { save.disabled = false; msg.innerHTML = ""; msg.appendChild(alertBox("err", e.message)); });
  } });

  var card = h("div", { class: "card" }, [
    h("div", { class: "row" }, [h("h2", { class: "grow", text: "Гэрээний бичвэр" }), save])
  ]);
  if (!editable) {
    card.appendChild(alertBox("info",
      "Гэрээ илгээгдсэн. Бичвэрийг засах нь ШИНЭ хүлээн авагчид л нөлөөлнө — аль хэдийн хүргэгдсэн хувь ХӨЛДӨӨТЭЙ, тэднийг дахин илгээж шинэчилнэ."));
  }
  card.appendChild(area);
  card.appendChild(msg);
  return card;
}

/* — Талууд */
function sectionParties(id, parties, state) {
  var card = h("div", { class: "card" }, [h("h2", { text: "Талууд" })]);
  if (!parties.length) {
    card.appendChild(h("p", { class: "muted", text: "Хараахан тал нэрлээгүй. Гэрээ гэдэг нь хамгийн багадаа хоёр тал." }));
  }
  parties.forEach(function (p) { card.appendChild(partyCard(id, p, state)); });
  card.appendChild(addPartyForm(id));
  return card;
}

function partyCard(id, p, state) {
  var box = h("div", { class: "card tight", style: "background:#fbfcfe" });
  box.appendChild(h("div", { class: "row" }, [
    h("div", { class: "grow" }, [
      h("div", {}, [
        h("strong", { text: p.display_name }),
        h("span", { class: "pill", style: "margin-left:8px", text: PARTY_ROLE[p.party_role] || p.party_role }),
        h("span", { class: "pill", text: PARTY_KIND[p.party_kind] || p.party_kind }),
        p.sign_order ? h("span", { class: "pill", text: p.sign_order + "-рт зурна" }) : null
      ]),
      h("div", { class: "muted", style: "font-size:13px" , text:
        [p.registration_number, p.contact_email, p.contact_phone].filter(Boolean).join(" · ") })
    ]),
    badge(p.state, PARTY_STATE)
  ]));

  if (p.decline_reason) box.appendChild(alertBox("err", "Татгалзсан шалтгаан: " + p.decline_reason));

  /* Гарын үсэг зурагчид. ӨӨР байгууллагын тал өөрсдөө нэрлэнэ — тэдний
     оронд нэрлэх нь гарын үсэг зурах эрхийг бидний шийдвэр болгоно. */
  var sigs = p.signatories || [];
  if (sigs.length) {
    box.appendChild(h("div", { class: "hint", style: "margin-top:8px", text:
      sigs.map(function (s) {
        return s.full_name + (s.position ? " (" + s.position + ")" : "") + (s.reg_number ? " · " + s.reg_number : "") +
               (s.signed_at ? " ✓ " + fmtWhen(s.signed_at) : "");
      }).join(" | ") }));
  }

  var actions = h("div", { class: "row", style: "margin-top:10px" });

  if (p.party_role !== "issuer" && !p.counterparty_tenant_id) {
    actions.appendChild(h("button", { class: "ghost small", text: "Гарын үсэг зурагч нэмэх", onclick: function () {
      addSignatory(id, p);
    } }));
  }
  if (p.has_copy) {
    actions.appendChild(h("a", { class: "btn ghost small", target: "_blank", rel: "noopener",
      href: API + "/" + id + "/parties/" + p.id + "/copy", text: "Илгээсэн хувь" }));
  }
  if (p.has_signed_copy) {
    actions.appendChild(h("a", { class: "btn ghost small", target: "_blank", rel: "noopener",
      href: API + "/" + id + "/parties/" + p.id + "/signed.pdf", text: "Гарын үсэгтэй хувь" }));
  }
  if (p.party_role !== "issuer" && (p.state === "invited" || p.state === "viewed")) {
    actions.appendChild(h("button", { class: "gold small", text: "Энэ талын өмнөөс зурах", onclick: function () {
      ceremony(API + "/" + id + "/parties/" + p.id + "/sign/start",
               API + "/" + id + "/parties/" + p.id + "/sign/poll",
               function () { location.hash = "#/c/" + id; viewContract(id); });
    } }));
    actions.appendChild(h("button", { class: "ghost small", text: "Холбоос үүсгэх", onclick: function () {
      makeInvite(id, p);
    } }));
  }
  if (p.state === "draft") {
    actions.appendChild(h("button", { class: "danger small", text: "Хасах", onclick: function () {
      api(API + "/" + id + "/parties/" + p.id, { method: "DELETE" })
        .then(function () { viewContract(id); }).catch(fail);
    } }));
  }
  box.appendChild(actions);
  return box;
}

function addSignatory(id, p) {
  var name = h("input", { placeholder: "Овог нэр" });
  var pos = h("input", { placeholder: "Албан тушаал" });
  var reg = h("input", { placeholder: "Регистр (УЗ00000000)" });
  var msg = h("div", {});
  var box = h("div", { class: "card" }, [
    h("h2", { text: p.display_name + " — гарын үсэг зурах хүн" }),
    h("p", { class: "muted", text: "PIN2 хүсэлт энэ регистрийн дугаараар очно. Дугааргүй бол ёслол эхлэхгүй." }),
    h("label", { text: "Овог нэр" }), name,
    h("label", { text: "Албан тушаал" }), pos,
    h("label", { text: "Регистрийн дугаар" }), reg,
    msg,
    h("div", { class: "row", style: "margin-top:12px" }, [
      h("button", { class: "primary", text: "Нэмэх", onclick: function () {
        api(API + "/" + id + "/parties/" + p.id + "/signatories", { method: "POST", body: JSON.stringify({
          full_name: name.value.trim(), position: pos.value.trim(), reg_number: reg.value.trim()
        }) }).then(function () { viewContract(id); })
          .catch(function (e) { msg.innerHTML = ""; msg.appendChild(alertBox("err", e.message)); });
      } }),
      h("button", { class: "ghost", text: "Болих", onclick: function () { viewContract(id); } })
    ])
  ]);
  show(box);
}

function makeInvite(id, p) {
  api(API + "/" + id + "/parties/" + p.id + "/invite", { method: "POST", body: JSON.stringify({ channel: "link" }) })
    .then(function (inv) {
      var url = location.origin + inv.path;
      show(h("div", {}, [
        h("h1", { text: "Урилгын холбоос" }),
        h("div", { class: "card" }, [
          h("p", { text: p.display_name + " — энэ холбоосыг илгээнэ үү." }),
          h("div", { class: "token", text: url }),
          alertBox("wait", "Энэ холбоос ЗӨВХӨН ОДОО харагдана. Хуудсыг хаасны дараа дахин харах зам байхгүй — санд зөвхөн түүний хэш хадгалагдана."),
          h("p", { class: "hint", text: "Хүчинтэй хугацаа: " + fmtWhen(inv.expires_at) }),
          h("div", { class: "row", style: "margin-top:12px" }, [
            h("button", { class: "primary", text: "Хуулах", onclick: function () {
              if (navigator.clipboard) navigator.clipboard.writeText(url);
            } }),
            h("button", { class: "ghost", text: "Буцах", onclick: function () { viewContract(id); } })
          ])
        ])
      ]));
    }).catch(fail);
}

function addPartyForm(id) {
  var name = h("input", { placeholder: "Байгууллага эсвэл хүний нэр" });
  var reg = h("input", { placeholder: "Регистр / улсын бүртгэлийн дугаар" });
  var email = h("input", { placeholder: "И-мэйл" });
  var phone = h("input", { placeholder: "Утас" });
  var addr = h("input", { placeholder: "Хаяг" });
  var kind = h("select", {}, [
    h("option", { value: "organisation", text: "Байгууллага (дансгүй)" }),
    h("option", { value: "person", text: "Иргэн (дансгүй)" }),
    h("option", { value: "tenant", text: "Энэ платформ дээрх байгууллага" }),
    h("option", { value: "member", text: "Дотоод хэрэглэгч" })
  ]);
  var role = h("select", {}, [
    h("option", { value: "counterparty", text: "Тал" }),
    h("option", { value: "issuer", text: "Гаргагч (бид)" }),
    h("option", { value: "witness", text: "Гэрч" }),
    h("option", { value: "guarantor", text: "Батлан даагч" })
  ]);
  var order = h("input", { type: "number", min: "1", placeholder: "Дараалал (заавал биш)" });
  var msg = h("div", {});

  return h("div", { style: "margin-top:14px" }, [
    h("h3", { text: "Тал нэмэх" }),
    h("div", { class: "grid2" }, [
      h("div", {}, [h("label", { text: "Нэр" }), name]),
      h("div", {}, [h("label", { text: "Регистр" }), reg]),
      h("div", {}, [h("label", { text: "Үүрэг" }), role]),
      h("div", {}, [h("label", { text: "Төрөл" }), kind]),
      h("div", {}, [h("label", { text: "И-мэйл" }), email]),
      h("div", {}, [h("label", { text: "Утас" }), phone]),
      h("div", {}, [h("label", { text: "Хаяг" }), addr]),
      h("div", {}, [h("label", { text: "Зурах дараалал" }), order])
    ]),
    h("p", { class: "hint", text: "Дараалал бичвэл өмнөх тал зурах хүртэл дараагийнх нь зурж чадахгүй." }),
    msg,
    h("div", { class: "row", style: "margin-top:10px" }, [
      h("button", { class: "primary", text: "Нэмэх", onclick: function () {
        var payload = {
          party_role: role.value, party_kind: kind.value,
          display_name: name.value.trim(), registration_number: reg.value.trim(),
          contact_email: email.value.trim(), contact_phone: phone.value.trim(),
          address_line: addr.value.trim()
        };
        if (order.value) payload.sign_order = Number(order.value);
        api(API + "/" + id + "/parties", { method: "POST", body: JSON.stringify(payload) })
          .then(function () { viewContract(id); })
          .catch(function (e) { msg.innerHTML = ""; msg.appendChild(alertBox("err", e.message)); });
      } })
    ])
  ]);
}

/* — Илгээх */
function sectionSend(id, parties, state, mode) {
  var pending = parties.filter(function (p) { return p.party_role !== "issuer"; });
  if (!pending.length) return h("div", {});

  var modeSel = h("select", {}, [
    h("option", { value: "counterpart", text: "Зэрэг (хэн ч эхэлж болно)" }),
    h("option", { value: "joint", text: "Дараалалтай" })
  ]);
  if (mode) modeSel.value = mode === "internal" ? "counterpart" : mode;
  var msg = h("div", {});

  var send = h("button", { class: "primary", text: state === "DRAFT" ? "Илгээх" : "Дахин илгээх", onclick: function () {
    send.disabled = true;
    api(API + "/" + id + "/send", { method: "POST", body: JSON.stringify({ mode: modeSel.value }) })
      .then(function (res) {
        send.disabled = false;
        msg.innerHTML = "";
        msg.appendChild(alertBox("ok", res.sent + " талд хүргэгдлээ."));
        (res.skipped || []).forEach(function (s) {
          msg.appendChild(alertBox("wait", s.display_name + ": " + s.reason));
        });
        setTimeout(function () { viewContract(id); }, 1200);
      })
      .catch(function (e) { send.disabled = false; msg.innerHTML = ""; msg.appendChild(alertBox("err", e.message)); });
  } });

  return h("div", { class: "card" }, [
    h("h2", { text: "Илгээх" }),
    h("p", { class: "muted", text: "Илгээх агшинд тал бүрийн PDF зурагдаж ХӨЛДӨНӨ. Тэд яг тэр байтад гарын үсэг зурна." }),
    h("label", { text: "Гарын үсгийн горим" }), modeSel,
    msg,
    h("div", { class: "row", style: "margin-top:12px" }, [send])
  ]);
}

/* ═══════════════════════════════════════════ 3. ИРСЭН ГЭРЭЭ */

function viewInbox() {
  api(API + "/inbox?state=all").then(function (data) {
    var items = data.items || [];
    var wrap = h("div", {}, [h("h1", { text: "Надад ирсэн гэрээ" })]);
    if (!items.length) {
      wrap.appendChild(h("div", { class: "card" }, [h("div", { class: "empty", text: "Танд ирсэн гэрээ алга." })]));
      return show(wrap);
    }
    var rows = items.map(function (v) {
      return h("tr", { class: "click", onclick: function () { location.hash = "#/inbox/" + v.party_id; } }, [
        h("td", { text: v.title }),
        h("td", { text: v.issuer_name || "—" }),
        h("td", {}, [badge(v.state, PARTY_STATE)]),
        h("td", { class: "muted", text: fmtDate(v.invited_at) }),
        h("td", { class: "muted", text: v.due_at ? fmtDate(v.due_at) : "" })
      ]);
    });
    wrap.appendChild(h("div", { class: "card" }, [
      h("table", {}, [
        h("thead", {}, [h("tr", {}, [
          h("th", { text: "Гэрээ" }), h("th", { text: "Илгээгч" }), h("th", { text: "Төлөв" }),
          h("th", { text: "Ирсэн" }), h("th", { text: "Эцсийн хугацаа" })
        ])]),
        h("tbody", {}, rows)
      ])
    ]));
    show(wrap);
  }).catch(fail);
}

function viewInboxOne(pid) {
  api(API + "/inbox/" + pid).then(function (v) {
    var base = API + "/inbox/" + pid;
    var wrap = h("div", {}, [
      h("a", { class: "plain", href: "#/inbox", text: "← Ирсэн гэрээ" }),
      h("div", { class: "row", style: "margin-top:6px" }, [
        h("h1", { class: "grow", text: v.title }), badge(v.state, PARTY_STATE)
      ])
    ]);

    if (v.parties && v.parties.length) {
      wrap.appendChild(h("div", { class: "steps" }, v.parties.map(function (p) {
        return h("span", { class: "step" }, [
          h("span", { class: "dot " + p.state }),
          h("span", { text: p.display_name + (p.mine ? " (та)" : "") }),
          h("span", { class: "muted", text: PARTY_STATE[p.state] || p.state })
        ]);
      })));
    }

    if (v.has_copy) {
      wrap.appendChild(h("div", { class: "card" }, [
        h("div", { class: "row" }, [
          h("h2", { class: "grow", text: "Гэрээний бичвэр" }),
          h("a", { class: "btn ghost small", target: "_blank", rel: "noopener", href: base + "/copy", text: "PDF харах" })
        ]),
        h("div", { class: "body-text", text: v.body_text || "" }),
        h("p", { class: "hint", text: "Баримтын SHA-256: " + (v.sha256 || "—") })
      ]));
    } else {
      wrap.appendChild(alertBox("wait", "Гэрээ хараахан хүргэгдээгүй байна."));
    }

    var mine = v.my_signatories || [];
    var sigCard = h("div", { class: "card" }, [h("h2", { text: "Гарын үсэг зурах хүн" })]);
    if (mine.length) {
      mine.forEach(function (s) {
        sigCard.appendChild(h("p", { text: s.full_name + (s.position ? " (" + s.position + ")" : "") +
          (s.reg_number ? " · " + s.reg_number : "") + (s.signed_at ? " ✓ " + fmtWhen(s.signed_at) : "") }));
      });
    } else {
      sigCard.appendChild(h("p", { class: "muted", text: "Танай байгууллагаас хэн гарын үсэг зурахыг нэрлээгүй байна." }));
    }
    if (v.state === "invited" || v.state === "viewed") {
      var name = h("input", { placeholder: "Овог нэр" });
      var pos = h("input", { placeholder: "Албан тушаал" });
      var reg = h("input", { placeholder: "Регистр" });
      var msg = h("div", {});
      sigCard.appendChild(h("div", { class: "grid2", style: "margin-top:8px" }, [
        h("div", {}, [h("label", { text: "Овог нэр" }), name]),
        h("div", {}, [h("label", { text: "Регистр" }), reg]),
        h("div", {}, [h("label", { text: "Албан тушаал" }), pos])
      ]));
      sigCard.appendChild(msg);
      sigCard.appendChild(h("div", { class: "row", style: "margin-top:10px" }, [
        h("button", { class: "ghost small", text: "Нэрлэх", onclick: function () {
          api(base + "/signatories", { method: "POST", body: JSON.stringify({
            full_name: name.value.trim(), position: pos.value.trim(), reg_number: reg.value.trim()
          }) }).then(function () { viewInboxOne(pid); })
            .catch(function (e) { msg.innerHTML = ""; msg.appendChild(alertBox("err", e.message)); });
        } })
      ]));
    }
    wrap.appendChild(sigCard);

    if ((v.state === "invited" || v.state === "viewed") && v.has_copy) {
      wrap.appendChild(h("div", { class: "card" }, [
        h("h2", { text: "Шийдвэр" }),
        h("div", { class: "row" }, [
          h("button", { class: "gold", text: "Гарын үсэг зурах (PIN2)", onclick: function () {
            ceremony(base + "/sign/start", base + "/sign/poll", function () { viewInboxOne(pid); });
          } }),
          h("button", { class: "danger", text: "Татгалзах", onclick: function () { declineInbox(pid); } })
        ])
      ]));
    }
    if (v.state === "signed") {
      wrap.appendChild(h("div", { class: "card" }, [
        alertBox("ok", "Та энэ гэрээнд гарын үсэг зурсан."),
        h("a", { class: "btn ghost small", target: "_blank", rel: "noopener", href: base + "/signed.pdf", text: "Гарын үсэгтэй хувь" })
      ]));
    }
    show(wrap);
  }).catch(fail);
}

function declineInbox(pid) {
  var reason = h("textarea", { style: "min-height:100px", placeholder: "Юуг нь засах ёстойг бичнэ үү" });
  var msg = h("div", {});
  show(h("div", {}, [
    h("h1", { text: "Татгалзах" }),
    h("div", { class: "card" }, [
      h("p", { class: "muted", text: "Шалтгаангүй татгалзал нь илгээгчид юу засахыг нь хэлэхгүй." }),
      reason, msg,
      h("div", { class: "row", style: "margin-top:12px" }, [
        h("button", { class: "danger", text: "Татгалзах", onclick: function () {
          api(API + "/inbox/" + pid + "/decline", { method: "POST", body: JSON.stringify({ reason: reason.value }) })
            .then(function () { viewInboxOne(pid); })
            .catch(function (e) { msg.innerHTML = ""; msg.appendChild(alertBox("err", e.message)); });
        } }),
        h("button", { class: "ghost", text: "Болих", onclick: function () { viewInboxOne(pid); } })
      ])
    ])
  ]));
}

/* ═══════════════════════════════════════════ зам */

function route() {
  stopPolling();
  var hash = location.hash || "#/";
  document.getElementById("tab-out").className = hash.indexOf("#/inbox") === 0 ? "" : "on";
  document.getElementById("tab-in").className = hash.indexOf("#/inbox") === 0 ? "on" : "";

  if (hash.indexOf("#/c/") === 0) return viewContract(hash.slice(4));
  if (hash.indexOf("#/inbox/") === 0) return viewInboxOne(hash.slice(8));
  if (hash === "#/inbox") return viewInbox();
  if (hash === "#/new") return viewNew();
  return viewList();
}

window.addEventListener("hashchange", route);
route();
