package main

import (
	"html/template"
	"os"
	"strings"
)

// sharedCSS is the one visual theme for every page, injected into templates
// via the css template func so the dashboard and player pages stay in sync.
const sharedCSS = `
:root{
  --bg:#0e100f;
  --bg-2:#141714;
  --panel:#181b18;
  --panel-2:#20251f;
  --panel-3:#2a3029;
  --line:#343b33;
  --line-strong:#4d594b;
  --text:#f3f0e8;
  --muted:#a3aca0;
  --accent:#d7a33b;
  --accent-2:#66b58a;
  --info:#6fa8dc;
  --danger:#d05e55;
  --ink:#12110d;
  --radius:6px;
  --shadow:0 18px 42px rgba(0,0,0,.24);
}
*{box-sizing:border-box}
html{scroll-behavior:smooth}
body{margin:0;min-height:100vh;display:flex;flex-direction:column;background:linear-gradient(180deg,#111410 0,#0e100f 52%,#0a0b0a 100%);color:var(--text);font:14px/1.5 Inter,"Segoe UI",system-ui,-apple-system,sans-serif}
a{color:var(--accent);text-decoration:none}
a:hover{text-decoration:underline}
a.link{font-weight:600}
header.site{position:sticky;top:0;z-index:10;display:grid;grid-template-columns:auto minmax(0,1fr) auto;align-items:center;gap:18px;padding:12px 22px;background:rgba(14,16,15,.93);backdrop-filter:blur(14px);border-bottom:1px solid rgba(77,89,75,.8);box-shadow:0 10px 26px rgba(0,0,0,.22)}
header.site h1{margin:0;font-size:16px;text-transform:uppercase;letter-spacing:1.6px;font-weight:850;white-space:nowrap}
header.site h1 a{color:var(--text)}
header.site h1 a:hover{text-decoration:none}
header.site h1 span{color:var(--accent)}
header.site .meta{color:var(--muted);font-size:12px}
header.site nav{min-width:0;display:flex;gap:5px;align-items:center;flex-wrap:wrap;justify-content:center}
header.site nav a{color:var(--muted);text-transform:uppercase;font-size:10px;letter-spacing:.6px;padding:7px 7px;border-radius:var(--radius);border:1px solid transparent;white-space:nowrap}
header.site nav a:hover{color:var(--text);text-decoration:none;background:rgba(255,255,255,.045);border-color:var(--line)}
header.site nav a.active{color:var(--ink);background:var(--accent);border-color:var(--accent);font-weight:800}
header.site .hdr-search{justify-self:end}
header.site .hdr-search input{min-height:32px;width:178px;border:1px solid var(--line);background:rgba(255,255,255,.045);color:var(--text);padding:5px 10px;font:inherit;font-size:12px;border-radius:var(--radius)}
header.site .hdr-search input:focus{outline:1px solid var(--accent);border-color:var(--accent);width:230px;transition:width .15s}
main{max-width:1360px;width:100%;flex:1;margin:0 auto;padding:28px 20px 68px}
h2{margin:34px 0 12px;font-size:12px;text-transform:uppercase;letter-spacing:1.8px;color:#c0c8cf;border-bottom:1px solid var(--line);padding-bottom:7px}
.page-title{display:flex;align-items:baseline;gap:12px;flex-wrap:wrap;margin:4px 0 14px;padding:18px 20px;border:1px solid var(--line);border-radius:var(--radius);background:linear-gradient(135deg,rgba(215,163,59,.13),rgba(102,181,138,.09) 44%,rgba(24,27,24,.96));box-shadow:var(--shadow)}
.page-title strong{font-size:30px;line-height:1.1;letter-spacing:.4px}
.overview-shell{display:grid;grid-template-columns:minmax(0,1.2fr) minmax(300px,.8fr);gap:18px;align-items:stretch;margin:4px 0 18px}
.overview-hero{position:relative;overflow:hidden;border:1px solid var(--line);border-radius:var(--radius);background:linear-gradient(135deg,rgba(215,163,59,.16),rgba(102,181,138,.08) 36%,rgba(24,27,24,.95));box-shadow:var(--shadow);padding:24px;min-height:250px}
.overview-hero:after{content:"";position:absolute;right:-80px;bottom:-110px;width:340px;height:240px;border:1px solid rgba(215,163,59,.28);transform:rotate(-18deg);background:repeating-linear-gradient(90deg,rgba(255,255,255,.035) 0 2px,transparent 2px 14px)}
.overview-hero > *{position:relative;z-index:1}
.eyebrow{display:block;color:var(--accent);font-size:11px;font-weight:800;text-transform:uppercase;letter-spacing:1.6px;margin-bottom:10px}
.overview-hero h2{margin:0;border:0;padding:0;color:var(--text);font-size:40px;line-height:1.02;text-transform:none;letter-spacing:0;max-width:760px}
.overview-hero p{max-width:720px;color:#cbd2c7;font-size:15px;margin:14px 0 0}
.overview-actions{display:flex;gap:10px;flex-wrap:wrap;margin-top:22px}
.action-link{display:inline-flex;align-items:center;min-height:36px;border:1px solid var(--line-strong);background:rgba(255,255,255,.055);color:var(--text);padding:8px 12px;border-radius:var(--radius);text-transform:uppercase;font-size:11px;font-weight:800;letter-spacing:.9px}
.action-link:hover{border-color:var(--accent);text-decoration:none;background:rgba(215,163,59,.12)}
.action-link.primary{background:var(--accent);border-color:var(--accent);color:var(--ink)}
.overview-rail{display:grid;gap:10px}
.feature-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}
.feature-card{display:grid;gap:4px;min-height:92px;padding:13px;border:1px solid var(--line);border-radius:var(--radius);background:rgba(255,255,255,.035);color:var(--text)}
.feature-card:hover{text-decoration:none;border-color:var(--line-strong);background:rgba(255,255,255,.06);transform:translateY(-1px)}
.feature-card strong{font-size:13px;text-transform:uppercase;letter-spacing:.9px}
.feature-card span{color:var(--muted);font-size:12px;line-height:1.35}
.section-heading{display:flex;align-items:baseline;justify-content:space-between;gap:12px;flex-wrap:wrap;margin:30px 0 12px;border-bottom:1px solid var(--line);padding-bottom:7px}
.section-heading h2,.section-heading h3{margin:0;border:0;padding:0}
.panel .section-heading{margin:0 0 10px}
.section-heading a{font-size:11px;text-transform:uppercase;letter-spacing:.9px;color:var(--muted)}
.data-section{margin-top:26px}
.data-section > h2:first-child{margin-top:0}
.cards,.kpi-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:10px;margin:16px 0}
.overview-rail .kpi-grid{grid-template-columns:repeat(2,1fr)}
.card,.kpi-card{background:linear-gradient(180deg,rgba(32,37,31,.98),rgba(24,27,24,.98));border:1px solid var(--line);border-radius:var(--radius);padding:12px 14px;min-width:0;box-shadow:0 8px 18px rgba(0,0,0,.13);position:relative;overflow:hidden}
.card:before,.kpi-card:before{content:"";position:absolute;left:0;top:0;bottom:0;width:3px;background:#46515e}
.card.gold:before,.kpi-card.gold:before{background:var(--accent)}
.card.green:before,.kpi-card.green:before{background:var(--accent-2)}
.card.red:before,.kpi-card.red:before{background:var(--danger)}
.card span,.kpi-card span{display:block;font-size:10px;text-transform:uppercase;letter-spacing:1.1px;color:var(--muted)}
.card strong,.kpi-card strong{display:block;font-size:24px;line-height:1.15;font-variant-numeric:tabular-nums;margin-top:4px;overflow-wrap:anywhere}
.kpi-card small{display:block;color:var(--muted);font-size:12px;margin-top:4px;white-space:normal}
.card-link{display:block;color:inherit}
.card-link:hover{text-decoration:none;border-color:#596574;transform:translateY(-1px)}
.card.gold strong{color:var(--accent)}
.card.green strong{color:var(--accent-2)}
.card.red strong{color:var(--danger)}
.section-tabs{display:flex;gap:8px;flex-wrap:wrap;margin:18px 0}
.section-tabs a{border:1px solid var(--line);background:var(--panel);color:var(--muted);padding:8px 12px;border-radius:var(--radius);text-transform:uppercase;font-size:11px;letter-spacing:1px}
.section-tabs a:hover,.section-tabs a.active{color:var(--ink);background:var(--accent);border-color:var(--accent);text-decoration:none;font-weight:800}
.stat-note{display:flex;gap:12px;flex-wrap:wrap;color:var(--muted);font-size:12px;margin:8px 0 0}
table{width:100%;border-collapse:separate;border-spacing:0;background:var(--panel);border:1px solid var(--line);border-radius:var(--radius);overflow:hidden;box-shadow:0 12px 26px rgba(0,0,0,.12)}
th,td{padding:9px 12px;border-bottom:1px solid var(--line);text-align:left;vertical-align:top;white-space:nowrap;font-variant-numeric:tabular-nums}
th{font-size:10px;text-transform:uppercase;letter-spacing:1px;color:#c3cbc0;background:linear-gradient(180deg,var(--panel-3),var(--panel-2));font-weight:700}
tbody tr:nth-child(even){background:rgba(255,255,255,.018)}
tbody tr:hover{background:#242b23}
tr:last-child td{border-bottom:0}
.rank-col{width:1%}
th.lb-active{color:var(--accent)}
td.lb-active{color:var(--accent);font-weight:800}
.mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:12px}
.muted{color:var(--muted)}
.wrap{white-space:normal;min-width:240px;max-width:520px;overflow-wrap:anywhere}
.pager{display:flex;gap:8px;align-items:center;justify-content:flex-end;margin:10px 0 0}
.pager span{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:1px}
.pager a{border:1px solid var(--line);background:var(--panel);color:var(--muted);padding:6px 12px;font-size:12px;text-transform:uppercase;letter-spacing:1px;border-radius:var(--radius)}
.pager a:hover{color:var(--text);text-decoration:none;border-color:var(--muted)}
.pager a.active{background:var(--accent);border-color:var(--accent);color:var(--ink);font-weight:700}
.filter-groups{display:grid;gap:2px;margin:14px 0;border:1px solid var(--line);border-radius:var(--radius);background:var(--panel);padding:10px 14px}
.filter-group{display:flex;gap:10px;align-items:center;flex-wrap:wrap;padding:4px 0;border-bottom:1px solid rgba(52,59,51,.5)}
.filter-group:last-child{border-bottom:0}
.filter-group>span{flex:none;width:88px;color:var(--muted);font-size:10px;text-transform:uppercase;letter-spacing:1px}
.filter-group .filters{margin:0;gap:6px}
.filter-group .filters a{padding:4px 10px;font-size:11px}
.filters{display:flex;gap:8px;flex-wrap:wrap;margin:12px 0}
.filters a{border:1px solid var(--line);background:var(--panel);color:var(--muted);padding:6px 12px;font-size:12px;text-transform:uppercase;letter-spacing:1px;border-radius:var(--radius)}
.filters a:hover{color:var(--text);text-decoration:none;border-color:var(--muted)}
.filters a.active{background:var(--accent);border-color:var(--accent);color:var(--ink);font-weight:700}
.search{display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:8px;margin:14px 0;align-items:end}
input[type="search"]{min-height:36px;border:1px solid var(--line);background:var(--panel);color:var(--text);padding:8px 10px;font:inherit;border-radius:var(--radius)}
input[type="search"]:focus{outline:1px solid var(--accent);border-color:var(--accent)}
button{min-height:36px;border:1px solid var(--accent);background:var(--accent);color:var(--ink);font-weight:700;padding:8px 16px;font:inherit;cursor:pointer;border-radius:var(--radius);text-transform:uppercase;letter-spacing:1px;font-size:12px}
button:hover{filter:brightness(1.1)}
.map-tiles{display:grid;grid-template-columns:repeat(auto-fit,minmax(230px,1fr));gap:14px;margin:16px 0}
.map-tile{position:relative;display:flex;flex-direction:column;gap:10px;padding:16px 16px 14px;border:1px solid var(--line);border-radius:var(--radius);background:linear-gradient(180deg,rgba(32,37,31,.98),rgba(24,27,24,.98));box-shadow:0 8px 18px rgba(0,0,0,.13);color:inherit;overflow:hidden}
.map-tile:before{content:"";position:absolute;left:0;top:0;bottom:0;width:3px;background:var(--accent)}
.map-tile:hover{text-decoration:none;border-color:#596574;transform:translateY(-1px)}
.map-tile-top{display:flex;align-items:baseline;gap:8px}
.map-rank{flex:none;font:700 11px/1 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;color:var(--muted)}
.map-name{font-size:15px;font-weight:800;letter-spacing:.2px;overflow-wrap:anywhere}
.map-rounds{font-size:26px;line-height:1.1;font-variant-numeric:tabular-nums}
.map-rounds small{margin-left:6px;font-size:10px;text-transform:uppercase;letter-spacing:1px;color:var(--muted);font-weight:600}
.map-tile-meta{color:var(--muted);font-size:12px}
.map-split{display:flex;flex-direction:column;gap:6px;margin-top:auto}
.map-split-bar{display:flex;height:6px;border-radius:4px;overflow:hidden;background:var(--panel-2)}
.map-split-bar span{height:100%}
.map-split-bar span.axis{background:#bc861f}
.map-split-bar span.allies{margin-left:2px;background:#4a8fd4}
.map-split-labels{display:flex;justify-content:space-between;font-size:11px;color:var(--muted)}
.map-split-labels .sw{display:inline-block;width:8px;height:8px;border-radius:2px;vertical-align:middle}
.map-split-labels .sw.axis{margin-right:5px;background:#bc861f}
.map-split-labels .sw.allies{margin-left:5px;background:#4a8fd4}
.grid{display:grid;grid-template-columns:minmax(0,1fr) minmax(300px,380px);gap:18px;align-items:stretch;margin:16px 0}
.grid2{display:grid;grid-template-columns:repeat(auto-fit,minmax(310px,1fr));gap:18px;align-items:stretch;margin:16px 0}
.panel{display:flex;flex-direction:column;background:linear-gradient(180deg,rgba(32,37,31,.95),rgba(24,27,24,.98));border:1px solid var(--line);border-radius:var(--radius);padding:16px;min-width:0;box-shadow:var(--shadow);overflow-x:auto}
.panel h3{margin:0 0 12px;font-size:11px;text-transform:uppercase;letter-spacing:1.7px;color:#c6ced6}
.round-overview{display:grid;grid-template-columns:minmax(0,1.12fr) minmax(340px,.88fr);gap:16px;align-items:stretch;margin:16px 0}
.round-summary{overflow:visible}
.round-kpis{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:10px;min-width:0}
.round-stat-card{position:relative;display:flex;flex-direction:column;justify-content:center;min-height:82px;padding:11px 12px 11px 15px;border:1px solid var(--line);border-radius:var(--radius);background:rgba(255,255,255,.035);box-shadow:0 8px 18px rgba(0,0,0,.11);overflow:hidden}
.round-stat-card:before{content:"";position:absolute;left:0;top:0;bottom:0;width:3px;background:#46515e}
.round-stat-card.gold:before{background:var(--accent)}
.round-stat-card.green:before{background:var(--accent-2)}
.round-stat-card span{display:block;color:var(--muted);font-size:10px;text-transform:uppercase;letter-spacing:1px}
.round-stat-card strong{display:block;margin-top:5px;font-size:23px;line-height:1.12;font-variant-numeric:tabular-nums;overflow-wrap:anywhere}
.round-stat-card.gold strong{color:var(--accent)}
.round-stat-card.green strong{color:var(--accent-2)}
.round-stat-card.text strong{font-size:17px;line-height:1.25}
.round-standouts{justify-content:flex-start}
.round-highlights{display:grid;gap:8px}
.round-highlight{display:grid;grid-template-columns:minmax(88px,.55fr) minmax(0,1fr);gap:4px 12px;align-items:baseline;padding:9px 0;border-bottom:1px solid rgba(52,59,51,.72)}
.round-highlight:last-child{border-bottom:0}
.round-highlight span{color:var(--muted);font-size:10px;text-transform:uppercase;letter-spacing:.9px}
.round-highlight strong{min-width:0;font-size:15px;line-height:1.2;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.round-highlight small{grid-column:2;color:var(--muted);font-size:12px;white-space:normal}
.round-kill-pace{margin:16px 0}
.round-kill-pace .chart{min-height:230px}
.round-chart-grid{display:grid;grid-template-columns:repeat(6,minmax(0,1fr));gap:16px;align-items:stretch;margin:16px 0}
.round-chart-grid .panel{grid-column:span 2;min-height:240px}
.round-chart-grid .panel:nth-child(1),.round-chart-grid .panel:nth-child(2){grid-column:span 3}
.round-detail-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px;align-items:start;margin:16px 0}
.round-detail-grid .panel{min-height:0}
.round-wide{grid-column:1/-1}
.round-table-panel{margin-top:18px}
.panel table{border:0}
.panel td:last-child{white-space:normal;overflow-wrap:anywhere}
.badge{display:inline-block;padding:2px 9px;font-size:11px;border-radius:10px;border:1px solid var(--line);color:var(--muted);text-transform:uppercase;letter-spacing:1px}
.badge.bot{color:var(--accent);border-color:var(--accent)}
.badge.human{color:var(--accent-2);border-color:var(--accent-2)}
.error{border-left:4px solid var(--danger);background:#2a1b18;color:#e8b7ae;padding:10px 12px;margin:14px 0;border-radius:var(--radius)}
body.auth-page header.site nav,body.auth-page header.site .hdr-search{display:none}
body.auth-page footer.site nav{display:none}
body.auth-page{background:radial-gradient(ellipse 900px 500px at 50% -10%,rgba(215,163,59,.10),transparent 60%),linear-gradient(180deg,#111410 0,#0e100f 52%,#0a0b0a 100%)}
body.auth-page main{display:flex;align-items:center;justify-content:center;flex:1;padding:48px 20px}
.auth-shell{width:100%;max-width:400px;margin:0 auto;display:grid;gap:18px}
.auth-mark{display:flex;justify-content:center}
.auth-mark svg{width:46px;height:46px;filter:drop-shadow(0 4px 14px rgba(215,163,59,.35))}
.auth-shell .page-title{flex-direction:column;align-items:center;text-align:center;gap:5px;padding:0;border:0;background:none;box-shadow:none}
.auth-shell .page-title strong{font-size:23px;letter-spacing:.3px}
.auth-shell .page-title .muted{font-size:11px;text-transform:uppercase;letter-spacing:1.8px}
.auth-card{display:grid;gap:13px;position:relative;padding:26px 24px;overflow:visible}
.auth-card:before{content:"";position:absolute;left:0;right:0;top:0;height:3px;border-radius:var(--radius) var(--radius) 0 0;background:linear-gradient(90deg,var(--accent),var(--accent-2))}
.input-group{position:relative}
.input-group svg{position:absolute;left:13px;top:50%;transform:translateY(-50%);pointer-events:none}
.auth-card input[type="text"],.auth-card input[type="password"]{min-height:44px;border:1px solid var(--line);background:var(--panel-2);color:var(--text);padding:10px 14px 10px 40px;font:inherit;border-radius:var(--radius);width:100%;transition:border-color .15s,background-color .15s}
.auth-card input[type="text"]:focus,.auth-card input[type="password"]:focus{outline:none;border-color:var(--accent);background:var(--panel-3)}
.auth-card button{width:100%;margin-top:3px}
.auth-token{margin-top:0;text-align:center}
.auth-token summary{cursor:pointer;list-style:none;color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:1px;text-align:center;padding:10px 0}
.auth-token summary::-webkit-details-marker{display:none}
.auth-token summary:hover{color:var(--text)}
.auth-token[open] summary{color:var(--accent)}
.auth-token .panel{margin-top:10px;text-align:left}
.bars{display:grid;gap:8px}
.bar-row{display:grid;grid-template-columns:150px 1fr 52px;gap:10px;align-items:center;font-size:13px}
.bar-label{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.bar-track{background:var(--panel-2);border:1px solid var(--line);border-radius:4px;height:14px;overflow:hidden}
.bar-fill{height:100%;border-radius:0 4px 4px 0}
.bar-value{text-align:right;font-variant-numeric:tabular-nums;color:var(--muted)}
.chart svg{width:100%;height:auto;display:block}
.chart-donut{display:flex;flex:1 1 auto;align-items:center;justify-content:center;padding:6px 0}
.chart-donut svg{max-width:190px}
.legend.center{justify-content:center}
.legend{display:flex;gap:16px;margin:10px 0 0;font-size:12px;color:var(--muted)}
.legend i{display:inline-block;width:10px;height:10px;border-radius:2px;margin-right:6px;vertical-align:-1px}
.analysis-grid{display:grid;grid-template-columns:1.2fr .8fr;gap:18px;align-items:start;margin:16px 0}
.statline{display:grid;gap:10px}
.statline-row{display:grid;grid-template-columns:minmax(130px,1fr) minmax(160px,2fr) 64px;gap:10px;align-items:center}
.statline-label{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.meter{height:12px;border:1px solid var(--line);background:#11161c;border-radius:99px;overflow:hidden}
.meter-fill{height:100%;background:#bc861f;border-radius:99px}
.metric-pair{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}
.callout{border-left:3px solid var(--accent);background:rgba(224,173,69,.08);padding:10px 12px;border-radius:var(--radius);color:#d7dde2}
.dashboard-hero{display:grid;grid-template-columns:minmax(0,1fr) minmax(340px,420px);gap:18px;align-items:stretch;margin:18px 0}
.chart-panel{min-height:310px}
.chart-panel .chart{margin-top:8px;max-width:760px}
.combat-trend{display:grid;grid-template-columns:repeat(auto-fit,minmax(58px,1fr));gap:10px;align-items:end;min-height:210px;margin-top:14px}
.combat-round{display:grid;grid-template-rows:auto 138px auto;gap:7px;min-width:0}
.combat-label{font-size:10px;color:var(--muted);white-space:nowrap;overflow:hidden;text-overflow:ellipsis;text-align:center}
.combat-bars{display:flex;align-items:end;justify-content:center;gap:5px;height:138px;border:1px solid var(--line);background:rgba(255,255,255,.025);border-radius:var(--radius);padding:8px 7px}
.combat-bars i{display:block;width:16px;min-height:2px;border-radius:4px 4px 0 0}
.combat-bars i.kill{background:#bc861f}
.combat-bars i.death{background:#d05e55}
.combat-values{display:flex;justify-content:center;gap:7px;color:var(--muted);font-size:10px;font-variant-numeric:tabular-nums}
.heatmap{display:grid;gap:10px}
.heat-row{display:grid;grid-template-columns:minmax(150px,1.1fr) repeat(4,minmax(92px,1fr));gap:8px;align-items:stretch;padding:10px;border:1px solid var(--line);border-radius:var(--radius);background:rgba(255,255,255,.025);color:var(--text)}
.heat-row:hover{text-decoration:none;background:rgba(255,255,255,.045);border-color:#596574}
.heat-title{display:flex;flex-direction:column;justify-content:center;min-width:0}
.heat-title strong{font-size:15px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.heat-title span{font-size:11px;color:var(--muted);text-transform:uppercase;letter-spacing:1px}
.heat-cell{position:relative;overflow:hidden;border:1px solid var(--line);border-radius:var(--radius);padding:9px 10px;background:rgba(255,255,255,.03)}
.heat-cell:before{content:"";position:absolute;inset:0;width:var(--heat);opacity:.42}
.heat-cell.hot:before{background:linear-gradient(90deg,var(--danger),var(--accent))}
.heat-cell.cool:before{background:linear-gradient(90deg,var(--info),var(--accent-2))}
.heat-cell.warn:before{background:linear-gradient(90deg,var(--accent),var(--danger))}
.heat-cell.time:before{background:linear-gradient(90deg,var(--panel-3),var(--info))}
.heat-cell span,.heat-cell strong{position:relative;z-index:1;display:block}
.heat-cell span{font-size:9px;color:var(--muted);text-transform:uppercase;letter-spacing:.9px}
.heat-cell strong{font-size:17px;line-height:1.1;margin-top:3px}
.metric-list{display:grid;gap:8px}
.metric-scroll{max-height:390px;overflow:auto;padding-right:4px;scrollbar-color:var(--line) transparent}
.metric-scroll .metric-list{padding-bottom:2px}
.metric-row{display:grid;grid-template-columns:70px 76px minmax(0,1fr);gap:10px;align-items:center;padding:10px;border:1px solid var(--line);border-radius:var(--radius);background:rgba(255,255,255,.025);color:var(--text)}
.metric-row:hover{text-decoration:none;background:rgba(255,255,255,.05);border-color:#596574}
.metric-row span{font-size:10px;color:var(--muted);text-transform:uppercase;letter-spacing:1px}
.metric-row strong{font-size:20px;color:var(--accent);font-variant-numeric:tabular-nums}
.metric-row em{font-style:normal;font-weight:700;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.metric-row small{grid-column:3;color:var(--muted);font-size:12px;white-space:normal}
.award-list{display:grid;gap:8px}
.award-row{display:flex;align-items:center;gap:10px;min-width:0}
.award-row .val{margin-left:auto;font-variant-numeric:tabular-nums;font-weight:700;color:var(--accent)}
.award-row .who{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.medal{width:22px;height:22px;border-radius:50%;display:inline-flex;align-items:center;justify-content:center;font-size:11px;font-weight:800;color:#181d23;flex:none;border:1px solid rgba(0,0,0,.35)}
.medal.g{background:linear-gradient(180deg,#e8c15a,#bc861f)}
.medal.s{background:linear-gradient(180deg,#d7dee6,#9aa7b4)}
.medal.b{background:linear-gradient(180deg,#d09a6a,#a06a3c)}
.panel.medal-case{margin:16px 0}
.medal-chips{display:flex;flex-wrap:wrap;gap:8px}
.medal-chip{display:inline-flex;align-items:center;gap:7px;padding:4px 10px;border:1px solid var(--line);border-radius:99px;background:rgba(255,255,255,.03);font-size:12px}
.medal-chip .medal{width:16px;height:16px;font-size:9px}
.medal-chip b{color:var(--accent);font-variant-numeric:tabular-nums}
.award-blurb{color:var(--muted);font-size:12px;margin:-6px 0 10px}
.player-summary{display:grid;grid-template-columns:minmax(0,1.1fr) minmax(320px,.9fr);gap:18px;align-items:stretch;margin:18px 0}
.player-scorecard{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:10px}
.hero-stat{padding:14px;border:1px solid var(--line);border-radius:var(--radius);background:rgba(255,255,255,.035)}
.hero-stat.primary{grid-column:span 2;background:linear-gradient(135deg,rgba(215,163,59,.14),rgba(255,255,255,.035))}
.hero-stat span{display:block;color:var(--muted);font-size:10px;text-transform:uppercase;letter-spacing:1.1px}
.hero-stat strong{display:block;font-size:30px;line-height:1.1;margin-top:5px;font-variant-numeric:tabular-nums;overflow-wrap:anywhere}
.hero-stat small{display:block;color:var(--muted);font-size:12px;margin-top:4px}
.profile-facts{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px}
.fact-row{display:flex;align-items:baseline;justify-content:space-between;gap:12px;padding:9px 0;border-bottom:1px solid var(--line)}
.fact-row span{color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.9px}
.fact-row strong{font-size:14px;text-align:right;font-variant-numeric:tabular-nums}
.round-chat{display:grid;gap:8px}
.chat-line{display:grid;grid-template-columns:156px minmax(130px,.35fr) minmax(0,1fr);gap:10px;align-items:start;padding:10px 12px;border:1px solid var(--line);border-radius:var(--radius);background:rgba(255,255,255,.025)}
.chat-line:hover{background:rgba(255,255,255,.045);border-color:var(--line-strong)}
.chat-line .speaker{font-weight:800;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.chat-line .message{white-space:normal;overflow-wrap:anywhere;color:#e5e8df}
.timeline{display:grid;gap:8px}
#combat-timeline{margin-top:24px}
.timeline-search{display:grid;grid-template-columns:minmax(220px,1fr) auto auto;gap:8px;margin:0 0 12px;align-items:center}
.timeline-search input[type="search"]{min-height:34px}
.timeline-search button,.timeline-search .action-link{min-height:34px}
.timeline-row{display:grid;grid-template-columns:154px 112px minmax(0,1fr) minmax(140px,.28fr);gap:10px;align-items:start;padding:10px 12px;border:1px solid var(--line);border-radius:var(--radius);background:rgba(255,255,255,.025)}
.timeline-row:hover{background:rgba(255,255,255,.045);border-color:var(--line-strong)}
.timeline-kind{display:inline-flex;align-items:center;width:max-content;max-width:100%;padding:2px 8px;border:1px solid var(--line-strong);border-radius:99px;color:var(--muted);font-size:10px;text-transform:uppercase;letter-spacing:.8px}
.timeline-summary{white-space:normal;overflow-wrap:anywhere;color:#e8ebe3}
.timeline-meta{color:var(--muted);font-size:12px;white-space:normal;overflow-wrap:anywhere}
footer.site{margin-top:auto;border-top:1px solid rgba(77,89,75,.8);background:rgba(11,13,11,.97)}
footer.site .foot-inner{max-width:1360px;margin:0 auto;padding:22px 20px;display:flex;justify-content:space-between;align-items:center;gap:14px;flex-wrap:wrap}
footer.site .brand{font-size:14px;text-transform:uppercase;letter-spacing:1.6px;font-weight:850;color:var(--text)}
footer.site .brand span{color:var(--accent)}
footer.site .foot-note{color:var(--muted);font-size:12px;margin-top:3px}
footer.site nav{display:flex;gap:6px}
footer.site nav a{color:var(--muted);text-transform:uppercase;font-size:10px;letter-spacing:.6px;padding:7px 10px;border-radius:var(--radius);border:1px solid transparent;white-space:nowrap}
footer.site nav a:hover{color:var(--text);text-decoration:none;background:rgba(255,255,255,.045);border-color:var(--line)}
@media (max-width:1100px){
  table{
    display:block;overflow-x:auto;
    background:
      linear-gradient(to right,var(--panel),var(--panel)) left/24px 100% no-repeat local,
      linear-gradient(to left,var(--panel),var(--panel)) right/24px 100% no-repeat local,
      linear-gradient(to right,rgba(0,0,0,.5),rgba(0,0,0,0) 24px) left/24px 100% no-repeat scroll,
      linear-gradient(to left,rgba(0,0,0,.5),rgba(0,0,0,0) 24px) right/24px 100% no-repeat scroll;
  }
}
@media (max-width:900px){
  header.site{grid-template-columns:1fr}
  header.site nav{justify-content:flex-start}
  header.site .hdr-search{justify-self:stretch}
  header.site .hdr-search input{width:100%}
  header.site .hdr-search input:focus{width:100%}
  .overview-shell{grid-template-columns:1fr}
  .overview-hero h2{font-size:32px}
  .feature-grid{grid-template-columns:1fr 1fr}
  .player-summary{grid-template-columns:1fr}
  .player-scorecard{grid-template-columns:repeat(2,minmax(0,1fr))}
  .round-overview{grid-template-columns:1fr}
  .round-chart-grid,.round-detail-grid{grid-template-columns:1fr}
  .round-chart-grid .panel,.round-chart-grid .panel:nth-child(1),.round-chart-grid .panel:nth-child(2),.round-wide{grid-column:auto}
  .grid{grid-template-columns:1fr}
  .analysis-grid{grid-template-columns:1fr}
  .dashboard-hero{grid-template-columns:1fr}
  .search{grid-template-columns:1fr}
  table{display:block;overflow-x:auto}
  .bar-row{grid-template-columns:110px 1fr 44px}
  .heat-row{grid-template-columns:1fr 1fr}
  .heat-title{grid-column:1/-1}
  .metric-row{grid-template-columns:64px 70px minmax(0,1fr)}
  .chat-line{grid-template-columns:1fr}
  .timeline-row{grid-template-columns:1fr}
  .timeline-search{grid-template-columns:1fr auto auto}
}
@media (max-width:560px){
  main{padding:18px 12px 48px}
  header.site{padding:10px 12px}
  .overview-hero{padding:18px}
  .overview-hero h2{font-size:27px}
  .feature-grid{grid-template-columns:1fr}
  .player-scorecard,.profile-facts{grid-template-columns:1fr}
  .round-kpis{grid-template-columns:repeat(2,minmax(0,1fr))}
  .round-stat-card{min-height:76px;padding:10px 11px 10px 14px}
  .round-stat-card strong{font-size:20px}
  .round-stat-card.text strong{font-size:15px}
  .round-highlight{grid-template-columns:1fr}
  .round-highlight small{grid-column:auto}
  .timeline-search{grid-template-columns:1fr}
  .hero-stat.primary{grid-column:auto}
  .page-title strong{font-size:24px}
}
`

func cssFunc() template.CSS {
	return template.CSS(`@import url("/assets/site.css");`)
}

// faviconSVG is served at /favicon.ico and embedded as a data URI in every
// page head: a gold crosshair on the site's dark panel color.
const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><rect width="32" height="32" rx="6" fill="#0e100f"/><circle cx="16" cy="16" r="9" fill="none" stroke="#d7a33b" stroke-width="2.5"/><path d="M16 3v6M16 23v6M3 16h6M23 16h6" stroke="#d7a33b" stroke-width="2.5" stroke-linecap="round"/><circle cx="16" cy="16" r="2.5" fill="#d7a33b"/></svg>`

// siteDescription is the sitewide fallback description used for both the
// <meta name="description"> tag and OpenGraph/Twitter previews. Individual
// pages don't currently override it — it's a generic "what is this site"
// blurb so links shared in Discord/forums preview reasonably instead of not
// at all.
const siteDescription = "Community-run stats for a Battlefield 1942 dedicated server: leaderboards, awards, maps, weapons, and match history rebuilt from server event logs."

// faviconFunc is registered as the `favicon` template func and injected into
// every page's <head>. Besides the icon link, it also emits the sitewide
// description/OpenGraph/Twitter meta tags — bundled here rather than added
// to every template's head block individually, since this func is already
// the one shared insertion point every page uses.
func faviconFunc() template.HTML {
	uri := "data:image/svg+xml," + strings.NewReplacer(
		`"`, "%22",
		"#", "%23",
		"<", "%3C",
		">", "%3E",
		" ", "%20",
	).Replace(faviconSVG)
	var b strings.Builder
	b.WriteString(`<link rel="icon" type="image/svg+xml" href="` + uri + `">`)
	b.WriteString(`<meta name="description" content="`)
	b.WriteString(template.HTMLEscapeString(siteDescription))
	b.WriteString(`">`)
	b.WriteString(`<meta property="og:type" content="website">`)
	b.WriteString(`<meta property="og:site_name" content="Spawnpoint">`)
	b.WriteString(`<meta property="og:title" content="Spawnpoint">`)
	b.WriteString(`<meta property="og:description" content="`)
	b.WriteString(template.HTMLEscapeString(siteDescription))
	b.WriteString(`">`)
	// OG/Twitter crawlers need an absolute image URL, so this only renders
	// when the operator has told us what that is; a relative path here
	// would just silently fail to preview on most platforms.
	if base := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"); base != "" {
		imageURL := template.HTMLEscapeString(base + "/assets/og-image.png")
		b.WriteString(`<meta property="og:image" content="` + imageURL + `">`)
		b.WriteString(`<meta name="twitter:image" content="` + imageURL + `">`)
		b.WriteString(`<meta name="twitter:card" content="summary_large_image">`)
	} else {
		b.WriteString(`<meta name="twitter:card" content="summary">`)
	}
	return template.HTML(b.String())
}

func siteHeader(active string) template.HTML {
	items := []struct {
		Key   string
		Label string
		Href  string
	}{
		{"overview", "Overview", "/"},
		{"live", "Live", "/live"},
		{"players", "Players", "/players"},
		{"leaderboard", "Leaderboard", "/leaderboard"},
		{"awards", "Awards", "/awards"},
		{"streaks", "Streaks", "/streaks"},
		{"rounds", "Rounds", "/rounds"},
		{"maps", "Maps", "/maps"},
		{"trends", "Trends", "/trends"},
		{"weapons", "Weapons", "/weapons"},
		{"vehicles", "Vehicles", "/vehicles"},
		{"kits", "Kits", "/kits"},
		{"clans", "Clans", "/clans"},
		{"compare", "Compare", "/compare"},
	}
	var b strings.Builder
	b.WriteString(`<header class="site"><h1><a href="/">Spawn<span>point</span></a></h1><nav>`)
	for _, item := range items {
		class := ""
		if item.Key == active {
			class = ` class="active"`
		}
		b.WriteString(`<a href="`)
		b.WriteString(template.HTMLEscapeString(item.Href))
		b.WriteString(`"`)
		b.WriteString(class)
		b.WriteString(`>`)
		b.WriteString(template.HTMLEscapeString(item.Label))
		b.WriteString(`</a>`)
	}
	b.WriteString(`</nav><form class="hdr-search" method="get" action="/search" role="search"><input type="search" name="q" placeholder="Search site&hellip;" aria-label="Search site"></form></header>`)
	return template.HTML(b.String())
}

// siteFooter closes every page: quiet links that don't belong in the main
// navigation, like the about page and the admin area.
func siteFooter() template.HTML {
	return template.HTML(`<footer class="site"><div class="foot-inner"><div class="brand">Spawn<span>point</span></div><nav><a href="/about">About</a><a href="/privacy">Privacy</a><a href="/terms">Terms</a><a href="/admin">Admin</a></nav></div></footer>`)
}
