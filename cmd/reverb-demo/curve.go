package main

import (
	"fmt"
	"math"
	"os"
	"strings"
)

// curvePoint 曲线上的一个采样点。
type curvePoint struct {
	tau float64
	L   int
	a60 float64
	a40 float64
}

func curveData(points int) []curvePoint {
	out := make([]curvePoint, 0, points)
	for k := 1; k <= points; k++ {
		tau := 0.005 * math.Pow(2.0/0.005, float64(k)/float64(points)) // 5ms..2s
		L := int(math.Round(tau * rate))
		if L < 2 {
			L = 2
		}
		out = append(out, curvePoint{
			tau: tau,
			L:   L,
			a60: math.Pow(0.001, 1.0/float64(L-1)),
			a40: math.Pow(0.01, 1.0/float64(L-1)),
		})
	}
	return out
}

// writeCurveHTML 生成离线自包含的曲线图页面（无外部依赖，数据内嵌为 JS 数组）。
// X 轴为尾音长度 L（对数刻度，标注同步给出物理时长），Y 轴为衰减系数 α。
func writeCurveHTML(path string) error {
	data := curveData(120)

	var js strings.Builder
	js.WriteString("const DATA=[")
	for i, p := range data {
		if i > 0 {
			js.WriteString(",")
		}
		fmt.Fprintf(&js, "[%d,%.10f,%.10f,%.6f]", p.L, p.a60, p.a40, p.tau)
	}
	js.WriteString("];")

	// 两条参考线：演示所用两档 τ=0.30s / 1.50s
	refL1 := int(math.Round(0.30 * rate))
	refL2 := int(math.Round(1.50 * rate))

	html := `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>尾音长度 L 与衰减系数 α 对应曲线</title>
<style>
  .viz-root {
    color-scheme: light;
    --surface-1: #fcfcfb; --plane: #f9f9f7;
    --text-primary: #0b0b0b; --text-secondary: #52514e; --text-muted: #898781;
    --grid: #e1e0d9; --axis: #c3c2b7; --border: rgba(11,11,11,0.10);
    --series-1: #2a78d6; --series-2: #eb6834;
    font-family: system-ui, -apple-system, "Segoe UI", sans-serif;
  }
  @media (prefers-color-scheme: dark) {
    :root:where(:not([data-theme="light"])) .viz-root {
      color-scheme: dark;
      --surface-1: #1a1a19; --plane: #0d0d0d;
      --text-primary: #f0efec; --text-secondary: #c3c2b7; --text-muted: #898781;
      --grid: #2c2c2a; --axis: #383835; --border: rgba(255,255,255,0.10);
      --series-1: #3987e5; --series-2: #d95926;
    }
  }
  body { margin: 0; background: var(--plane); color: var(--text-primary); }
  .viz-root { max-width: 980px; margin: 24px auto; padding: 0 20px 32px; }
  h1 { font-size: 20px; font-weight: 650; margin: 12px 0 4px; }
  .sub { color: var(--text-secondary); font-size: 13px; margin-bottom: 18px; }
  .card { background: var(--surface-1); border: 1px solid var(--border);
          border-radius: 12px; padding: 18px 18px 8px; }
  svg { width: 100%; height: auto; display: block; }
  .grid { stroke: var(--grid); stroke-width: 1; }
  .axisline { stroke: var(--axis); stroke-width: 1; }
  .tick { fill: var(--text-muted); font-size: 11px; }
  .axis-title { fill: var(--text-secondary); font-size: 13px; font-weight: 600; }
  .line { fill: none; stroke-width: 2; stroke-linejoin: round; stroke-linecap: round; }
  .end-dot { stroke: var(--surface-1); stroke-width: 2; }
  .refline { stroke: var(--text-muted); stroke-width: 1; stroke-dasharray: 4 4; opacity: .75; }
  .reflabel { fill: var(--text-muted); font-size: 10.5px; }
  .legend { display: flex; gap: 22px; margin: 6px 4px 14px; font-size: 13px; color: var(--text-secondary); }
  .legend .key { display: inline-flex; align-items: center; gap: 8px; }
  .legend .sw { width: 18px; height: 2px; border-radius: 1px; }
  #crosshair { stroke: var(--text-muted); stroke-width: 1; visibility: hidden; }
  #tip { position: fixed; pointer-events: none; visibility: hidden; background: var(--surface-1);
         border: 1px solid var(--border); border-radius: 8px; padding: 8px 10px; font-size: 12px;
         color: var(--text-secondary); box-shadow: 0 4px 14px rgba(0,0,0,.14); min-width: 168px; z-index: 5; }
  #tip .v { color: var(--text-primary); font-weight: 650; font-variant-numeric: tabular-nums; }
  #tip .row { display: flex; align-items: center; gap: 7px; margin: 2px 0; }
  #tip .lk { width: 12px; height: 2px; border-radius: 1px; }
  details { margin-top: 14px; }
  summary { cursor: pointer; color: var(--text-secondary); font-size: 13px; }
  table { border-collapse: collapse; width: 100%; margin-top: 10px; font-size: 12px; }
  th, td { text-align: right; padding: 5px 10px; border-bottom: 1px solid var(--grid);
           font-variant-numeric: tabular-nums; color: var(--text-secondary); }
  th { color: var(--text-primary); font-weight: 600; }
  td:first-child, th:first-child { text-align: left; }
</style>
</head>
<body>
<div class="viz-root">
  <h1>尾音长度 L 与衰减系数 α 的对应关系</h1>
  <div class="sub">反解关系 α = level<sup>1/(L−1)</sup>，采样率 44100 Hz；X 轴为尾音长度（对数刻度，括号内为物理时长），竖直虚线标出演示两档 τ = 0.30 s / 1.50 s</div>
  <div class="card">
    <div class="legend">
      <span class="key"><span class="sw" style="background:var(--series-1)"></span>尾电平 −60 dB（level = 0.001）</span>
      <span class="key"><span class="sw" style="background:var(--series-2)"></span>尾电平 −40 dB（level = 0.01）</span>
    </div>
    <svg id="chart" viewBox="0 0 920 520" role="img" aria-label="尾音长度与衰减系数对应曲线">
      <line id="crosshair" x1="0" y1="0" x2="0" y2="0"></line>
      <g id="grid"></g><g id="axes"></g><g id="refs"></g>
      <g id="series"></g><g id="dots"></g>
    </svg>
    <div id="tip"></div>
  </div>
  <details>
    <summary>数据表视图（全部 120 个采样点）</summary>
    <table id="tbl"><thead><tr>
      <th>尾音长度 L（帧）</th><th>物理时长 τ（s）</th>
      <th>α（−60 dB）</th><th>α（−40 dB）</th>
    </tr></thead><tbody></tbody></table>
  </details>
</div>
<script>
` + js.String() + fmt.Sprintf(`
const REFS = [{L:%d, label:"τ=0.30s"}, {L:%d, label:"τ=1.50s"}];
const svg = document.getElementById('chart');
const W = 920, H = 520, M = {l: 84, r: 70, t: 24, b: 64};
const pw = W - M.l - M.r, ph = H - M.t - M.b;
const css = n => getComputedStyle(document.querySelector('.viz-root')).getPropertyValue(n).trim();
const LMIN = DATA[0][0], LMAX = DATA[DATA.length-1][0];
const xScale = L => M.l + (Math.log(L)-Math.log(LMIN))/(Math.log(LMAX)-Math.log(LMIN))*pw;
const AMIN = 0.999, AMAX = 1.0;
const yScale = a => M.t + (AMAX-a)/(AMAX-AMIN)*ph;
const NS = 'http://www.w3.org/2000/svg';
function el(name, attrs) { const e = document.createElementNS(NS, name); for (const k in attrs) e.setAttribute(k, attrs[k]); return e; }
function txt(x, y, s, cls, anchor) { const t = el('text', {x, y, class: cls, 'text-anchor': anchor || 'middle'}); t.textContent = s; return t; }

// 网格 + 轴刻度：X 对数刻度
const xTicks = [];
for (let base = 100; base <= 100000; base *= 10) {
  for (let m = 1; m < 10; m++) { const v = base * m; if (v >= LMIN && v <= LMAX) xTicks.push(v); }
}
[LMIN, LMAX].forEach(v => { if (!xTicks.includes(v)) xTicks.push(v); });
xTicks.sort((a,b)=>a-b).forEach(v => {
  const x = xScale(v);
  document.getElementById('grid').appendChild(el('line', {x1:x, y1:M.t, x2:x, y2:M.t+ph, class:'grid'}));
  const tau = (v/44100);
  document.getElementById('axes').appendChild(txt(x, M.t+ph+18, v.toLocaleString(), 'tick'));
  document.getElementById('axes').appendChild(txt(x, M.t+ph+34, tau>=1? tau.toFixed(2)+'s' : (tau*1000).toFixed(0)+'ms', 'tick'));
});
const yTicks = [0.999, 0.9992, 0.9994, 0.9996, 0.9998, 1.0];
yTicks.forEach(v => {
  const y = yScale(v);
  document.getElementById('grid').appendChild(el('line', {x1:M.l, y1:y, x2:M.l+pw, y2:y, class:'grid'}));
  document.getElementById('axes').appendChild(txt(M.l-10, y+4, v.toFixed(4), 'tick', 'end'));
});
document.getElementById('axes').appendChild(el('line', {x1:M.l, y1:M.t, x2:M.l, y2:M.t+ph, class:'axisline'}));
document.getElementById('axes').appendChild(el('line', {x1:M.l, y1:M.t+ph, x2:M.l+pw, y2:M.t+ph, class:'axisline'}));
document.getElementById('axes').appendChild(txt(M.l+pw/2, H-12, '尾音长度 L（帧，对数刻度；下方数字为对应物理时长）', 'axis-title'));
const yt = txt(22, M.t+ph/2, '衰减系数 α', 'axis-title'); yt.setAttribute('transform', 'rotate(-90 22 '+(M.t+ph/2)+')');

// 参考竖线
REFS.forEach(r => {
  const x = xScale(r.L);
  document.getElementById('refs').appendChild(el('line', {x1:x, y1:M.t, x2:x, y2:M.t+ph, class:'refline'}));
  document.getElementById('refs').appendChild(txt(x+5, M.t+12, r.label + '  L=' + r.L.toLocaleString(), 'reflabel', 'start'));
});

// 两条曲线 + 端点
function pathFor(col) {
  return DATA.map((d, i) => (i ? 'L' : 'M') + xScale(d[0]).toFixed(2) + ' ' + yScale(d[col]).toFixed(2)).join(' ');
}
const s1 = el('path', {d: pathFor(1), class:'line', stroke: 'var(--series-1)'});
const s2 = el('path', {d: pathFor(2), class:'line', stroke: 'var(--series-2)'});
document.getElementById('series').append(s1, s2);
const last = DATA[DATA.length-1];
[[1,'--series-1'],[2,'--series-2']].forEach(([col, cvar]) => {
  document.getElementById('dots').appendChild(el('circle',
    {cx: xScale(last[0]), cy: yScale(last[col]), r: 4.5, class:'end-dot', fill: 'var('+cvar+')'}));
});

// 十字线 + tooltip（一个 tooltip 同时列出两条曲线）
const cross = document.getElementById('crosshair'), tip = document.getElementById('tip');
cross.setAttribute('y1', M.t); cross.setAttribute('y2', M.t+ph);
svg.addEventListener('pointermove', ev => {
  const rect = svg.getBoundingClientRect();
  const mx = (ev.clientX - rect.left) / rect.width * W;
  if (mx < M.l || mx > M.l+pw) { cross.style.visibility = 'hidden'; tip.style.visibility = 'hidden'; return; }
  // 最近数据点（X 为对数刻度，按 log 距离找）
  const logX = Math.log(LMIN) + (mx-M.l)/pw*(Math.log(LMAX)-Math.log(LMIN));
  let best = 0, bd = Infinity;
  DATA.forEach((d, i) => { const dd = Math.abs(Math.log(d[0])-logX); if (dd < bd) { bd = dd; best = i; }});
  const d = DATA[best], x = xScale(d[0]);
  cross.setAttribute('x1', x); cross.setAttribute('x2', x); cross.style.visibility = 'visible';
  tip.replaceChildren();
  const head = document.createElement('div');
  head.style.marginBottom = '4px';
  head.textContent = 'L=' + d[0].toLocaleString() + ' 帧 · τ=' + (d[3]>=1? d[3].toFixed(3)+'s' : (d[3]*1000).toFixed(1)+'ms');
  tip.appendChild(head);
  [['--series-1','−60 dB', d[1]], ['--series-2','−40 dB', d[2]]].forEach(([c, name, v]) => {
    const row = document.createElement('div'); row.className = 'row';
    const lk = document.createElement('span'); lk.className = 'lk'; lk.style.background = 'var('+c+')';
    const lab = document.createElement('span'); lab.textContent = name;
    const val = document.createElement('span'); val.className = 'v'; val.textContent = v.toFixed(7);
    row.append(lk, lab, val); tip.appendChild(row);
  });
  tip.style.visibility = 'visible';
  const px = ev.clientX + 16, py = ev.clientY + 12;
  tip.style.left = Math.min(px, window.innerWidth - 210) + 'px';
  tip.style.top = Math.min(py, window.innerHeight - 110) + 'px';
});
svg.addEventListener('pointerleave', () => { cross.style.visibility = 'hidden'; tip.style.visibility = 'hidden'; });

// 数据表
const tbody = document.querySelector('#tbl tbody');
DATA.forEach(d => {
  const tr = document.createElement('tr');
  [d[0].toLocaleString(), d[3].toFixed(4), d[1].toFixed(8), d[2].toFixed(8)].forEach((v, i) => {
    const td = document.createElement('td'); if (i === 0) td.style.textAlign = 'left';
    td.textContent = v; tr.appendChild(td);
  });
  tbody.appendChild(tr);
});
</script>
</body>
</html>`, refL1, refL2)

	return os.WriteFile(path, []byte(html), 0o644)
}
