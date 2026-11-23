package main

import (
	"fmt"
	"strings"

	"goaudio/audio"
)

// 图表几何与配色角色（dataviz 参考色板，已通过 validate_palette 校验）。
const (
	svgW, svgH   = 760, 400
	margin       = 60
	plotW, plotH = svgW - 2*margin, svgH - 2*margin
	xMin, xMax   = -48.0, 0.0
	yMin, yMax   = -20.0, 2.0
	css          = `
.viz-root { color-scheme: light;
  --surface-1:#fcfcfb; --text-primary:#0b0b0b; --text-secondary:#52514e; --grid:#e3e2de;
  --series-1:#2a78d6; --series-2:#eb6834;
  font-family: -apple-system, "Segoe UI", Roboto, "PingFang SC", "Microsoft YaHei", sans-serif;
  background: var(--surface-1); color: var(--text-primary);
}
@media (prefers-color-scheme: dark) {
  :root:where(:not([data-theme="light"])) .viz-root { color-scheme: dark;
    --surface-1:#1a1a19; --text-primary:#f0efec; --text-secondary:#c3c2b7; --grid:#33332f;
    --series-1:#3987e5; --series-2:#d95926; }
}
.viz-root { max-width: 820px; margin: 24px auto; padding: 0 16px; }
h1 { font-size: 20px; } h2 { font-size: 16px; margin-top: 28px; }
p, td, th, li, figcaption { color: var(--text-secondary); font-size: 13px; }
table { border-collapse: collapse; margin: 12px 0; }
th, td { border: 1px solid var(--grid); padding: 4px 10px; text-align: right; }
th { color: var(--text-primary); }
figure { margin: 20px 0; }
.tip { position: fixed; pointer-events: none; display: none; background: var(--surface-1);
  border: 1px solid var(--grid); border-radius: 6px; padding: 6px 10px; font-size: 12px;
  color: var(--text-primary); box-shadow: 0 2px 8px rgba(0,0,0,.18); z-index: 10; }
.axis-label { fill: var(--text-secondary); font-size: 12px; }
.grid-line { stroke: var(--grid); stroke-width: 1; }
.axis-line { stroke: var(--text-secondary); stroke-width: 1; }
.series { fill: none; stroke-width: 2; }
.cross { stroke: var(--text-secondary); stroke-width: 1; stroke-dasharray: 3 3; display: none; }
.hover-dot { display: none; }
`
)

func sx(x float64) float64 { return margin + (x-xMin)/(xMax-xMin)*plotW }
func sy(y float64) float64 { return margin + plotH - (y-yMin)/(yMax-yMin)*plotH }

type chartSpec struct {
	id, title, caption, slot string
	data                     [][2]float64
}

func renderHTML(before, after [][2]float64, cfg audio.CompressorConfig) string {
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">`)
	b.WriteString("<title>软拐点压缩器增益曲线</title><style>" + css + "</style></head>")
	b.WriteString(`<body class="viz-root" data-theme="auto">`)
	b.WriteString("<h1>软拐点压缩器 · 静态增益曲线（压缩前后各一张）</h1>")
	b.WriteString(fmt.Sprintf(
		"<p>门限 %d（%.2f dBFS）· 比率 %.0f:1 · 膝宽 %.1f dB · 攻击 %.0f ms · 释放 %.0f ms</p>",
		cfg.Threshold, ampDB(cfg.Threshold), cfg.Ratio, cfg.KneeDB, cfg.AttackMs, cfg.ReleaseMs))

	charts := []chartSpec{
		{
			id: "before", slot: "series-1",
			title:   "图 1 · 压缩前（未启用，直通）",
			caption: "Enabled=false：增益恒为 0 dB，输出逐样本等于输入。",
			data:    before,
		},
		{
			id: "after", slot: "series-2",
			title: "图 2 · 压缩后（软拐点压缩）",
			caption: fmt.Sprintf("门限以下不压缩；门限 ±%.1f dB 为软拐点；其上按 %.0f:1 全比率衰减。",
				cfg.KneeDB/2, cfg.Ratio),
			data: after,
		},
	}
	for _, ch := range charts {
		b.WriteString("<figure>")
		b.WriteString("<h2>" + ch.title + "</h2>")
		b.WriteString(svgChart(ch))
		b.WriteString("<figcaption>" + ch.caption + "</figcaption>")
		b.WriteString("</figure>")
	}
	b.WriteString(`<div class="tip"></div>`)

	// 数据表（表格视图：可访问性回退）。
	b.WriteString("<h2>增益曲线数据表</h2><table><tr><th>输入 dBFS</th><th>图1 压缩前增益 dB</th><th>图2 压缩后增益 dB</th></tr>")
	for i := range before {
		b.WriteString(fmt.Sprintf("<tr><td>%.1f</td><td>%.3f</td><td>%.3f</td></tr>",
			before[i][0], before[i][1], after[i][1]))
	}
	b.WriteString("</table>")
	b.WriteString(hoverScript(charts))
	b.WriteString("</body></html>")
	return b.String()
}

func svgChart(ch chartSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg id="%s" class="chart" width="%d" height="%d" viewBox="0 0 %d %d" role="img"
 aria-label="%s：横轴输入电平 dBFS，纵轴增益 dB">`,
		ch.id, svgW, svgH, svgW, svgH, ch.title)

	// 网格与 Y 刻度。
	for db := yMin; db <= yMax; db += 5 {
		y := sy(db)
		fmt.Fprintf(&b, `<line class="grid-line" x1="%v" y1="%v" x2="%v" y2="%v"/>`,
			margin, y, margin+plotW, y)
		fmt.Fprintf(&b, `<text class="axis-label" x="%v" y="%v" text-anchor="end">%.0f</text>`,
			margin-6, y+4, db)
	}
	// X 网格与刻度。
	for db := xMin; db <= xMax; db += 8 {
		x := sx(db)
		fmt.Fprintf(&b, `<line class="grid-line" x1="%v" y1="%v" x2="%v" y2="%v"/>`,
			x, margin, x, margin+plotH)
		fmt.Fprintf(&b, `<text class="axis-label" x="%v" y="%v" text-anchor="middle">%.0f</text>`,
			x, margin+plotH+18, db)
	}
	// 零增益参考线与坐标轴。
	fmt.Fprintf(&b, `<line class="axis-line" x1="%v" y1="%v" x2="%v" y2="%v"/>`,
		margin, sy(0), margin+plotW, sy(0))
	fmt.Fprintf(&b, `<line class="axis-line" x1="%v" y1="%v" x2="%v" y2="%v"/>`,
		margin, margin, margin, margin+plotH)
	fmt.Fprintf(&b, `<line class="axis-line" x1="%v" y1="%v" x2="%v" y2="%v"/>`,
		margin, margin+plotH, margin+plotW, margin+plotH)
	b.WriteString(fmt.Sprintf(
		`<text class="axis-label" x="%v" y="%v" text-anchor="middle">输入电平 (dBFS)</text>`,
		margin+plotW/2, svgH-12))
	b.WriteString(fmt.Sprintf(
		`<text class="axis-label" transform="translate(16 %v) rotate(-90)" text-anchor="middle">增益 (dB)</text>`,
		margin+plotH/2))

	// 单系列折线（点串写入 DOM 供 hover 解析）。
	var pts []string
	for _, p := range ch.data {
		pts = append(pts, fmt.Sprintf("%v,%v", sx(p[0]), sy(p[1])))
	}
	fmt.Fprintf(&b, `<polyline class="series" stroke="var(--%s)" points="%s"/>`,
		ch.slot, strings.Join(pts, " "))

	// 系列直接标注（单系列无需图例框，标题即名称）。
	last := ch.data[len(ch.data)-1]
	fmt.Fprintf(&b, `<text class="axis-label" x="%v" y="%v" fill="var(--%s)" text-anchor="end">增益曲线</text>`,
		sx(last[0])-4, sy(last[1])-8, ch.slot)

	// 十字线与悬停点。
	fmt.Fprintf(&b, `<line class="cross" x1="0" y1="%v" x2="0" y2="%v"/>`,
		margin, margin+plotH)
	fmt.Fprintf(&b, `<circle class="hover-dot" r="4" fill="var(--%s)"/>`, ch.slot)
	b.WriteString("</svg>")
	return b.String()
}

// hoverScript 为每张图绑定最近点十字线 + tooltip；数据从 DOM 点串解析，无第三方库。
func hoverScript(charts []chartSpec) string {
	var ids []string
	for _, ch := range charts {
		ids = append(ids, "'"+ch.id+"'")
	}
	return fmt.Sprintf(`<script>
(function(){
  var tip = document.querySelector('.tip');
  var margin = %d, plotW = %d, plotH = %d, xMin = %g, xMax = %g, yMin = %g, yMax = %g;
  var ids = [%s];
  function invX(px){ return xMin + (px - margin)/plotW * (xMax - xMin); }
  function invY(py){ return yMin + (margin + plotH - py)/plotH * (yMax - yMin); }
  ids.forEach(function(id){
    var svg = document.getElementById(id);
    var line = svg.querySelector('polyline');
    var pts = line.getAttribute('points').split(' ').map(function(p){
      var q = p.split(','); return {x: +q[0], y: +q[1]}; });
    var cross = svg.querySelector('.cross'), dot = svg.querySelector('.hover-dot');
    function hide(){ tip.style.display='none'; cross.style.display='none'; dot.style.display='none'; }
    svg.addEventListener('mousemove', function(ev){
      var r = svg.getBoundingClientRect();
      var mx = (ev.clientX - r.left) * (svg.viewBox.baseVal.width / r.width);
      if (mx < margin || mx > margin + plotW) { hide(); return; }
      var best = pts[0], bd = Infinity;
      pts.forEach(function(p){ var d = Math.abs(p.x - mx); if (d < bd){ bd = d; best = p; } });
      cross.setAttribute('x1', best.x); cross.setAttribute('x2', best.x);
      cross.style.display = 'block';
      dot.setAttribute('cx', best.x); dot.setAttribute('cy', best.y); dot.style.display = 'block';
      tip.innerHTML = '输入 ' + invX(best.x).toFixed(1) + ' dBFS<br>增益 ' + invY(best.y).toFixed(2) + ' dB';
      tip.style.display = 'block';
      tip.style.left = (ev.clientX + 14) + 'px'; tip.style.top = (ev.clientY + 12) + 'px';
    });
    svg.addEventListener('mouseleave', hide);
  });
})();
</script>`, margin, plotW, plotH, xMin, xMax, yMin, yMax, strings.Join(ids, ","))
}
