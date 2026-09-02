/*
 * Gerege Client — урилгын холбоосоор ирсэн хүний дэлгэц.
 *
 * ТУСДАА ФАЙЛ, санаатайгаар. Энэ хуудсыг НЭВТРЭЭГҮЙ хүн нээдэг тул консолын
 * кодыг дуудвал 401 дээр нэвтрэх дэлгэц рүү шидэх логик нь тэднийг данс
 * байхгүй газар руу явуулна. Хоёр өөр үзэгч, хоёр өөр зан төлөв.
 *
 * Токен нь ХАЯГИЙН МӨРӨӨС гарна: холбоос нь /contract/<токен>.
 */
"use strict";

var app = document.getElementById("app");
var token = decodeURIComponent((location.pathname.split("/contract/")[1] || "").replace(/\/+$/, ""));
var API = "/api/v1/contract/" + encodeURIComponent(token);
var poll = null;

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

function api(path, opts) {
  opts = opts || {};
  opts.headers = Object.assign({ "Content-Type": "application/json" }, opts.headers || {});
  return fetch(path, opts).then(function (res) {
    if (res.status === 204) return {};
    return res.json().catch(function () { return {}; }).then(function (body) {
      if (!res.ok) throw new Error(body.error || body.message || ("алдаа " + res.status));
      return body;
    });
  });
}

function alertBox(kind, text) { return h("div", { class: "alert " + kind, text: text }); }
function show(node) { app.innerHTML = ""; app.appendChild(node); }
function fmtWhen(s) { if (!s) return ""; var d = new Date(s); return isNaN(d) ? s : d.toLocaleString("mn-MN"); }
var PARTY_STATE = {
  invited: "Хүлээгдэж буй", viewed: "Уншсан", signed: "Гарын үсэг зурсан",
  declined: "Татгалзсан", withdrawn: "Эргүүлж татсан", expired: "Хугацаа дууссан", draft: "Ноорог"
};

function fail(err) {
  show(h("div", { class: "card" }, [
    alertBox("err", err.message || String(err)),
    h("p", { class: "muted", text: "Холбоос хүчингүй, хугацаа нь дууссан, эсвэл хаагдсан байж болно. Илгээсэн байгууллагатайгаа холбогдоно уу." })
  ]));
}

/* Ёслол: асуусаар байх — тайлбарыг app.js дээр үз. */
function ceremony(onDone) {
  var box = h("div", { class: "card" }, [h("p", { class: "muted", text: "Утас руу хүсэлт илгээж байна…" })]);
  show(box);
  api(API + "/sign/start", { method: "POST", body: "{}" }).then(function (s) {
    box.innerHTML = "";
    box.appendChild(h("h2", { text: "Утсаа шалгана уу" }));
    box.appendChild(h("p", { class: "muted", text: "eID апп дээрх баталгаажуулах код доорхтой таарч байвал PIN2 кодоо оруулна уу." }));
    if (s.verification_code) box.appendChild(h("div", { class: "vcode", text: s.verification_code }));
    var status = alertBox("wait", "Хүлээгдэж байна…");
    box.appendChild(status);
    var tick = function () {
      api(API + "/sign/poll", { method: "POST", body: "{}" }).then(function (p) {
        if (p.state === "COMPLETE") { clearTimeout(poll); onDone(); return; }
        if (p.state === "PENDING") { poll = setTimeout(tick, 3000); return; }
        clearTimeout(poll);
        status.className = "alert err";
        status.textContent = p.state === "REFUSED" ? "Та татгалзлаа." : "Ёслол дуусав: " + p.state;
      }).catch(function (err) {
        clearTimeout(poll);
        status.className = "alert err";
        status.textContent = err.message;
      });
    };
    poll = setTimeout(tick, 3000);
  }).catch(fail);
}

function render(v) {
  var wrap = h("div", {}, [
    h("div", { class: "row" }, [
      h("h1", { class: "grow", text: v.title }),
      h("span", { class: "badge " + v.state, text: PARTY_STATE[v.state] || v.state })
    ]),
    h("p", { class: "muted", text: "Хүлээн авагч: " + v.display_name })
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
        h("a", { class: "btn ghost small", target: "_blank", rel: "noopener", href: API + "/copy", text: "PDF харах" })
      ]),
      h("div", { class: "body-text", text: v.body_text || "" }),
      h("p", { class: "hint", text: "Баримтын SHA-256: " + (v.sha256 || "—") })
    ]));
  } else {
    wrap.appendChild(alertBox("wait", "Гэрээний хувь хараахан бэлэн болоогүй байна."));
  }

  var sigs = v.my_signatories || [];
  var card = h("div", { class: "card" }, [h("h2", { text: "Гарын үсэг зурах хүн" })]);
  if (sigs.length) {
    sigs.forEach(function (s) {
      card.appendChild(h("p", { text: s.full_name + (s.position ? " (" + s.position + ")" : "") +
        (s.reg_number ? " · " + s.reg_number : "") + (s.signed_at ? " ✓ " + fmtWhen(s.signed_at) : "") }));
    });
  } else {
    card.appendChild(h("p", { class: "muted", text: "Хараахан нэрлээгүй." }));
  }

  if (v.may_nominate && (v.state === "invited" || v.state === "viewed")) {
    var name = h("input", { placeholder: "Овог нэр" });
    var pos = h("input", { placeholder: "Албан тушаал" });
    var reg = h("input", { placeholder: "Регистр (УЗ00000000)" });
    var msg = h("div", {});
    card.appendChild(h("p", { class: "hint", style: "margin-top:10px", text:
      "PIN2 хүсэлт энэ регистрийн дугаараар очно. НЭГ Л УДАА бүртгэгдэнэ — тиймээс сайн шалгаарай." }));
    card.appendChild(h("div", { class: "grid2" }, [
      h("div", {}, [h("label", { text: "Овог нэр" }), name]),
      h("div", {}, [h("label", { text: "Регистр" }), reg]),
      h("div", {}, [h("label", { text: "Албан тушаал" }), pos])
    ]));
    card.appendChild(msg);
    card.appendChild(h("div", { class: "row", style: "margin-top:10px" }, [
      h("button", { class: "primary", text: "Бүртгэх", onclick: function () {
        api(API + "/signatory", { method: "POST", body: JSON.stringify({
          full_name: name.value.trim(), position: pos.value.trim(), reg_number: reg.value.trim()
        }) }).then(load).catch(function (e) { msg.innerHTML = ""; msg.appendChild(alertBox("err", e.message)); });
      } })
    ]));
  }
  wrap.appendChild(card);

  if ((v.state === "invited" || v.state === "viewed") && v.has_copy && sigs.length) {
    wrap.appendChild(h("div", { class: "card" }, [
      h("h2", { text: "Шийдвэр" }),
      h("div", { class: "row" }, [
        h("button", { class: "gold", text: "Гарын үсэг зурах (PIN2)", onclick: function () { ceremony(load); } }),
        h("button", { class: "danger", text: "Татгалзах", onclick: decline })
      ])
    ]));
  }
  if (v.state === "signed") {
    wrap.appendChild(h("div", { class: "card" }, [
      alertBox("ok", "Та энэ гэрээнд гарын үсэг зурсан."),
      h("a", { class: "btn ghost small", target: "_blank", rel: "noopener", href: API + "/signed.pdf", text: "Гарын үсэгтэй хувиа татах" })
    ]));
  }
  if (v.state === "declined") wrap.appendChild(alertBox("err", "Та энэ гэрээнээс татгалзсан."));
  show(wrap);
}

function decline() {
  var reason = h("textarea", { style: "min-height:100px", placeholder: "Юуг нь засах ёстойг бичнэ үү" });
  var msg = h("div", {});
  show(h("div", { class: "card" }, [
    h("h2", { text: "Татгалзах" }),
    h("p", { class: "muted", text: "Шалтгаангүй татгалзал нь илгээгчид юу засахыг нь хэлэхгүй." }),
    reason, msg,
    h("div", { class: "row", style: "margin-top:12px" }, [
      h("button", { class: "danger", text: "Татгалзах", onclick: function () {
        api(API + "/decline", { method: "POST", body: JSON.stringify({ reason: reason.value }) })
          .then(load).catch(function (e) { msg.innerHTML = ""; msg.appendChild(alertBox("err", e.message)); });
      } }),
      h("button", { class: "ghost", text: "Болих", onclick: load })
    ])
  ]));
}

function load() { api(API).then(render).catch(fail); }

if (!token) fail(new Error("Холбоос бүрэн биш байна.")); else load();
