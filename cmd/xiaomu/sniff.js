(() => {
  const records = [], seen = new Set();
  const isMedia = value => {
    try { return /\.(m3u8|mp4|m4v|mov|flv|mp3|m4a|aac)$/i.test(new URL(value, location.href).pathname); }
    catch (_) { return false; }
  };
  const save = item => {
    if (!item || typeof item.url !== "string" || records.length >= 128) return;
    try {
      const u = new URL(item.url, location.href);
      if (!/^https?:$/.test(u.protocol) || seen.has(u.href)) return;
      seen.add(u.href);
      const record = {url:u.href, referer:item.referer || location.href, headers:item.headers || {}};
      records.push(record);
      if (window !== window.top) window.top.postMessage({xiaomuMedia:record}, "*");
    } catch (_) {}
  };
  const record = (url, headers, force) => {
    if (typeof url === "string" && (force || isMedia(url))) save({url,headers,referer:location.href});
  };
  window.addEventListener("message", e => { if (e.data && e.data.xiaomuMedia) save(e.data.xiaomuMedia); });
  window.__xiaomuMedia = records;
  const fetchOriginal = window.fetch;
  window.fetch = function(input, init) {
    const url = typeof input === "string" ? input : input.url;
    const headers = Object.fromEntries(new Headers(init?.headers || input?.headers || {}));
    record(url, headers);
    return fetchOriginal.apply(this, arguments).then(response => {
      if (/mpegurl|^video\/|^audio\//i.test(response.headers.get("content-type") || "")) record(response.url,headers,true);
      return response;
    });
  };
  const open = XMLHttpRequest.prototype.open, setHeader = XMLHttpRequest.prototype.setRequestHeader, send = XMLHttpRequest.prototype.send;
  XMLHttpRequest.prototype.open = function(method,url) { this.__xiaomuURL = String(url); this.__xiaomuHeaders={}; return open.apply(this,arguments); };
  XMLHttpRequest.prototype.setRequestHeader = function(k,v) { (this.__xiaomuHeaders ||= {})[k]=v; return setHeader.apply(this,arguments); };
  XMLHttpRequest.prototype.send = function() { record(this.__xiaomuURL,this.__xiaomuHeaders); return send.apply(this,arguments); };
  try { new PerformanceObserver(list => list.getEntries().forEach(e => record(e.name))).observe({type:"resource",buffered:true}); } catch (_) {}
  const scan = () => {
    document.querySelectorAll("video,audio,source").forEach(v => {
      record(v.currentSrc || v.src);
      if (v.play) { v.muted=true; const p=v.play(); if (p) p.catch(()=>{}); }
    });
    for (const key of Object.keys(window)) {
      if (/^player_[a-z0-9_]+$/i.test(key)) {
        const p=window[key]; if (!p || typeof p.url!=="string") continue;
        let u=p.url; try {if (String(p.encrypt)==="2") u=atob(u); if (p.encrypt) u=decodeURIComponent(u);} catch (_) {}
        record(u);
      }
    }
  };
  setInterval(scan,300);
})();
