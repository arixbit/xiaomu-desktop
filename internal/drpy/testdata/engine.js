import cheerio from "assets://js/lib/cheerio.min.js";
import "assets://js/lib/crypto-js.js";
import "assets://js/lib/jsencrypt.js";
import { gbkTool } from "assets://js/lib/gbk.js";
import muban from "assets://js/lib/模板.js";

let rule = {};

function applyRuleSource(src) {
  const fn = new Function(`${src}\nreturn typeof rule === "undefined" ? null : rule;`);
  const out = fn();
  if (!out || typeof out !== "object") {
    throw new Error("规则里没有 var rule");
  }
  rule = out;
}

function loadRule(ext) {
  if (ext && typeof ext === "object") {
    rule = ext;
    return;
  }
  const text = String(ext || "");
  if (text.startsWith("http://") || text.startsWith("https://")) {
    const res = req(text, { method: "GET" });
    applyRuleSource(res.content || "");
    return;
  }
  applyRuleSource(text);
}

function abs(u) {
  return joinUrl(rule.host || "", u || "");
}

export default {
  init(ext) {
    if (!cheerio || typeof cheerio.jinja2 !== "function") throw new Error("cheerio");
    if (typeof CryptoJS === "undefined") throw new Error("crypto");
    if (typeof gbkTool !== "function") throw new Error("gbk");
    if (!muban || typeof muban.getMubans !== "function") throw new Error("muban");
    loadRule(ext);
    if (rule.host) rule.host = cheerio.jinja2(rule.host);
    local.set("demo", "ready", "1");
  },
  home() {
    const names = String(rule.class_name || "").split("&");
    const ids = String(rule.class_url || "").split("&");
    const cls = [];
    for (let i = 0; i < names.length; i++) {
      if (!names[i]) continue;
      cls.push({ type_id: ids[i] || String(i), type_name: names[i] });
    }
    return JSON.stringify({ class: cls });
  },
  category(tid, pg) {
    let u = String(rule.url || "").split("fyclass").join(String(tid)).split("fypage").join(String(pg || 1));
    u = abs(u);
    const html = req(u, { headers: { Referer: rule.host || "" } }).content || "";
    const items = pdfa(html, rule["一级"] || ".item");
    const list = items.map((item) => ({
      vod_id: pd(item, "a&&href", u),
      vod_name: pdfh(item, "a&&Text"),
      vod_pic: pdfh(item, "img&&data-src")
    }));
    return JSON.stringify({ list, page: Number(pg) || 1 });
  },
  detail(id) {
    const u = abs(String(id));
    const html = req(u, {}).content || "";
    const title = pdfh(html, ".missing&&Text||h1&&Text");
    const eps = pdfa(html, ".eps&&a");
    const play = eps.map((ep) => pdfh(ep, "a&&Text") + "$" + pd(ep, "a&&href", u)).join("#");
    return JSON.stringify({
      list: [{
        vod_id: id,
        vod_name: title,
        vod_play_from: "默认",
        vod_play_url: play
      }]
    });
  },
  search(wd, quick, pg) {
    let u = String(rule.searchUrl || "").split("**").join(encodeURIComponent(wd)).split("fypage").join(String(pg || 1));
    u = abs(u);
    const html = req(u, {}).content || "";
    const items = pdfa(html, rule["一级"] || ".item");
    const list = items.map((item) => ({
      vod_id: pd(item, "a&&href", u),
      vod_name: pdfh(item, "a&&Text")
    }));
    return JSON.stringify({ list });
  },
  play(flag, id) {
    return JSON.stringify({
      parse: 0,
      url: id,
      flag: flag,
      header: { "X-Local": local.get("demo", "ready") }
    });
  }
};
