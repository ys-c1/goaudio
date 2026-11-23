// Command declipdemo 生成五类样本、执行削波修复，并把原始/削波/修复波形、
// 逐类报告（阶数、残差、修复样本数）与 SVG 对照图写入 artifacts/ 目录。
//
// 运行（在仓库根目录）：
//
//	go run ./cmd/declipdemo
package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"goaudio/audio"
)

const outDir = "artifacts"

type scenario struct {
	name      string
	rate      int
	frames    int
	freq      float64
	amp       int
	clipLevel int
}

func hardClip(c *audio.Clip, level int) *audio.Clip {
	out := &audio.Clip{Rate: c.Rate, Frames: append([]int16(nil), c.Frames...)}
	for i, v := range out.Frames {
		if int(v) > level {
			out.Frames[i] = int16(level)
		} else if int(v) < -level {
			out.Frames[i] = -int16(level)
		}
	}
	return out
}

func rmse(a, b []int16) float64 {
	var sq float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		sq += d * d
	}
	return math.Sqrt(sq / float64(len(a)))
}

func main() {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}

	scenarios := []scenario{
		{"class1_clean", 8000, 1600, 220, 20000, 0}, // 不削波
		{"class2_short", 8000, 2400, 50, 34000, 30000},
		{"class3_long", 8000, 4000, 60, 34000, 22000},
		{"class4_multi", 8000, 4800, 100, 33000, 25000},
	}

	var report strings.Builder
	report.WriteString("# 削波修复逐类报告\n\n")
	report.WriteString("| 类别 | 削平段数 | 阶数(最小/最大) | 残差RMSE(首段) | 修复样本数 | 峰值(修复后) | RMSE 削波→修复 |\n")
	report.WriteString("|---|---|---|---|---|---|---|\n")

	for _, sc := range scenarios {
		orig, err := audio.NewClip(sc.rate, sc.frames)
		if err != nil {
			panic(err)
		}
		if err := audio.Sine(orig, sc.freq, sc.amp); err != nil {
			panic(err)
		}

		var clipped *audio.Clip
		cfg := audio.DefaultRepairConfig()
		if sc.clipLevel > 0 {
			clipped = hardClip(orig, sc.clipLevel)
			cfg.ClipLevel = sc.clipLevel
		} else {
			clipped = &audio.Clip{Rate: orig.Rate, Frames: append([]int16(nil), orig.Frames...)}
		}

		repaired, rep, derr := audio.Declip(clipped, cfg)
		if derr != nil {
			panic(derr)
		}

		// WAV 三件套对照。
		base := filepath.Join(outDir, sc.name)
		must(orig.SaveWAV(base + "_original.wav"))
		must(clipped.SaveWAV(base + "_clipped.wav"))
		must(repaired.SaveWAV(base + "_repaired.wav"))

		minOrder, maxOrder := 0, 0
		var firstRMSE float64
		if len(rep.Segments) > 0 {
			minOrder = rep.Segments[0].Order
			firstRMSE = rep.Segments[0].ResidualRMSE
			for _, sg := range rep.Segments {
				if sg.Order < minOrder {
					minOrder = sg.Order
				}
				if sg.Order > maxOrder {
					maxOrder = sg.Order
				}
			}
		}
		orderStr := "N/A（无削波）"
		if minOrder > 0 {
			orderStr = fmt.Sprintf("%d/%d", minOrder, maxOrder)
		}
		rmseClip := rmse(orig.Frames, clipped.Frames)
		rmseRep := rmse(orig.Frames, repaired.Frames)
		report.WriteString(fmt.Sprintf("| %s | %d | %s | %.3f | %d | %d | %.1f → %.2f |\n",
			sc.name, len(rep.Segments), orderStr, firstRMSE, rep.RepairedCount,
			rep.PeakAfter, rmseClip, rmseRep))

		// SVG 对照图（取前 400 帧，清晰展示平台与修复）。
		view := 400
		if view > sc.frames {
			view = sc.frames
		}
		svg := buildSVG(orig.Frames[:view], clipped.Frames[:view], repaired.Frames[:view], sc.name)
		must(os.WriteFile(base+"_compare.svg", []byte(svg), 0o644))
		fmt.Printf("已生成 %s: 段=%d 修复样本=%d RMSE %.1f→%.2f\n",
			sc.name, len(rep.Segments), rep.RepairedCount, rmseClip, rmseRep)
	}

	// 第五类：拒绝路径（不产出波形，只记录拒绝原因与位置）。
	report.WriteString("\n## 第五类：拒绝路径\n\n")
	report.WriteString("| 子项 | 触发条件 | 返回错误（含位置） | 匹配哨兵 |\n|---|---|---|---|\n")
	longCfg := audio.DefaultRepairConfig()
	longCfg.ClipLevel = 30000
	reportRejection(&report, "5a 超长削平段", buildLongRunClip(), longCfg, audio.ErrFlatLimit)
	zeroCfg := audio.DefaultRepairConfig()
	zeroCfg.ClipLevel = 30000
	zeroCfg.Window = 8
	reportRejection(&report, "5b 预测阶数为零", buildZeroOrderClip(), zeroCfg, audio.ErrZeroOrder)
	reportRejection(&report, "5c 采样率为零", &audio.Clip{Rate: 0, Frames: []int16{1, 2, 3, 3, 2, 1}}, audio.DefaultRepairConfig(), audio.ErrSampleRate)

	must(os.WriteFile(filepath.Join(outDir, "repair_report.md"), []byte(report.String()), 0o644))
	fmt.Println("报告已写入", filepath.Join(outDir, "repair_report.md"))
}

func buildLongRunClip() *audio.Clip {
	cfg := audio.DefaultRepairConfig()
	frames := make([]int16, 400)
	for i := range frames {
		frames[i] = 2000
	}
	for i := 100; i < 100+cfg.MaxFlat+10; i++ {
		frames[i] = 30000
	}
	return &audio.Clip{Rate: 8000, Frames: frames}
}

func buildZeroOrderClip() *audio.Clip {
	frames := make([]int16, 64)
	for i := range frames {
		frames[i] = 30000
	}
	frames[0], frames[63] = 1, 1
	return &audio.Clip{Rate: 8000, Frames: frames}
}

func reportRejection(b *strings.Builder, label string, c *audio.Clip, cfg audio.RepairConfig, want error) {
	_, _, err := audio.Declip(c, cfg)
	pos := "全局（无具体帧位置）"
	matched := false
	if err != nil {
		var re *audio.RepairError
		if asRepair(err, &re) && re.Index >= 0 {
			pos = fmt.Sprintf("第 %d-%d 帧（起于 %d）", re.Index, re.End, re.Index)
		}
		matched = errorIs(err, want)
	}
	errText := "nil（未拒绝！）"
	if err != nil {
		errText = err.Error()
	}
	b.WriteString(fmt.Sprintf("| %s | %s | %s | %v |\n", label, errText, pos, matched))
}

// errorIs 复刻 errors.Is 的本包最小实现，避免主流程额外导入。
func errorIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		if u, ok := err.(interface{ Unwrap() error }); ok {
			err = u.Unwrap()
			continue
		}
		return false
	}
	return false
}

// asRepair 包装 errors.As，避免在主流程再引一个 errors 导入。
func asRepair(err error, target **audio.RepairError) bool {
	for err != nil {
		if re, ok := err.(*audio.RepairError); ok {
			*target = re
			return true
		}
		if u, ok := err.(interface{ Unwrap() error }); ok {
			err = u.Unwrap()
			continue
		}
		return false
	}
	return false
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

// buildSVG 生成原始/削波/修复三条波形叠加的 SVG（无第三方依赖）。
func buildSVG(orig, clipped, repaired []int16, title string) string {
	const (
		w, h = 960, 360
		pad  = 40
	)
	n := len(orig)
	x := func(i int) float64 {
		return pad + float64(i)*(w-2*pad)/float64(n-1)
	}
	y := func(v int16) float64 {
		return h/2 - float64(v)*(h/2-pad)/32768.0
	}
	poly := func(frames []int16, color string, width float64) string {
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("<polyline fill=\"none\" stroke=\"%s\" stroke-width=\"%.2f\" points=\"", color, width))
		for i, v := range frames {
			sb.WriteString(fmt.Sprintf("%.1f,%.1f ", x(i), y(v)))
		}
		sb.WriteString("\"/>")
		return sb.String()
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"%d\" height=\"%d\" viewBox=\"0 0 %d %d\">\n", w, h, w, h))
	b.WriteString("<rect width=\"100%\" height=\"100%\" fill=\"white\"/>\n")
	b.WriteString(fmt.Sprintf("<text x=\"%d\" y=\"24\" font-family=\"monospace\" font-size=\"16\">%s（前 %d 帧）</text>\n", pad, title, n))
	// 零轴。
	b.WriteString(fmt.Sprintf("<line x1=\"%d\" y1=\"%d\" x2=\"%d\" y2=\"%d\" stroke=\"#ccc\" stroke-width=\"1\"/>\n",
		pad, h/2, w-pad, h/2))
	// 顺序：原始(灰) 、削波(红) 、修复(蓝)。
	b.WriteString(poly(orig, "#9aa0a6", 1.2) + "\n")
	b.WriteString(poly(clipped, "#d93025", 1.4) + "\n")
	b.WriteString(poly(repaired, "#1a73e8", 1.2) + "\n")
	b.WriteString("<text x=\"40\" y=\"352\" font-family=\"monospace\" font-size=\"13\" fill=\"#9aa0a6\">灰=原始</text>\n")
	b.WriteString("<text x=\"140\" y=\"352\" font-family=\"monospace\" font-size=\"13\" fill=\"#d93025\">红=削波</text>\n")
	b.WriteString("<text x=\"240\" y=\"352\" font-family=\"monospace\" font-size=\"13\" fill=\"#1a73e8\">蓝=修复</text>\n")
	b.WriteString("</svg>\n")
	return b.String()
}
