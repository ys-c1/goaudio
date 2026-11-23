// Command mixbench 混音总线基准与能量守恒报告。
//
// 两档通道数（2 路 / 4 路）× 五场景（同相、反相、满幅、静音、三路叠加），
// 输出两张表：
//  1. 峰值与耗时对照表（限幅前浮点峰值、硬削顶峰值、软限幅峰值、限幅是否介入）
//  2. 能量表（限幅前 E_in/E_pre、限幅后 E_post、差值、守恒判定）
//
// 不变量：限幅器未介入的行，E_pre - E_post 必须严格为 0，否则程序以非零码退出。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goaudio/audio"
)

const (
	rate      = 8000 // Hz
	frames    = 8000 // 1 秒
	threshold = 24576
)

type scenario struct {
	name   string
	amps   []int // 每路基础幅度（生成后再施加相位）
	phase  []int // 每路相位：+1 同相，-1 反相
	weight float64
}

// buildClips 按通道数 N 展开场景：N 大于场景所需路数时补零幅静音路。
func buildClips(s scenario, n int) ([]*audio.Clip, []float64) {
	clips := make([]*audio.Clip, n)
	weights := make([]float64, n)
	for i := 0; i < n; i++ {
		c, err := audio.NewClip(rate, frames)
		if err != nil {
			panic(err)
		}
		var amp int
		ph := 1
		if i < len(s.amps) {
			amp = s.amps[i]
			if i < len(s.phase) {
				ph = s.phase[i]
			}
		}
		if amp > 0 {
			if err := audio.Sine(c, 100, amp); err != nil {
				panic(err)
			}
			if ph < 0 {
				for k := range c.Frames {
					c.Frames[k] = -c.Frames[k]
				}
			}
			weights[i] = s.weight
		}
		clips[i] = c
	}
	return clips, weights
}

type row struct {
	n         int
	scenario  string
	peakPre   float64 // 限幅前加权和浮点峰值
	peakHard  int     // 旧行为：直接量化硬削顶后的峰值
	peakSoft  int     // 软限幅输出峰值
	engaged   bool
	cost      time.Duration
	eIn       float64
	ePre      float64
	ePost     float64
	delta     float64
	conserved bool
}

func runOne(n int, s scenario) row {
	clips, weights := buildClips(s, n)

	// 限幅前检查点 1：加权和连续域峰值
	peakPre, err := audio.PreLimitPeak(clips, weights)
	if err != nil {
		panic(err)
	}
	// 限幅前检查点 2：加权求和直接量化（旧的硬削顶行为）
	pre, err := audio.NaiveMix(clips, weights)
	if err != nil {
		panic(err)
	}
	// 软限幅混音总线，计时为一次完整混音调用
	start := time.Now()
	post, err := audio.MixBus(clips, audio.MixConfig{Weights: weights, Threshold: threshold})
	cost := time.Since(start)
	if err != nil {
		panic(err)
	}

	peakHard, peakSoft := audio.Peak(pre), audio.Peak(post)
	eIn := audio.WeightedInputEnergy(clips, weights)
	ePre, ePost := audio.Energy(pre), audio.Energy(post)
	engaged := peakPre > float64(threshold)
	delta := ePre - ePost

	return row{
		n:         n,
		scenario:  s.name,
		peakPre:   peakPre,
		peakHard:  peakHard,
		peakSoft:  peakSoft,
		engaged:   engaged,
		cost:      cost,
		eIn:       eIn,
		ePre:      ePre,
		ePost:     ePost,
		delta:     delta,
		conserved: !engaged && delta == 0,
	}
}

func main() {
	scenarios := []scenario{
		{name: "同相", amps: []int{20000, 20000, 20000, 20000}, phase: []int{1, 1, 1, 1}, weight: 1},
		{name: "反相", amps: []int{20000, 20000, 20000, 20000}, phase: []int{1, -1, 1, -1}, weight: 1},
		{name: "满幅", amps: []int{32767, 32767, 32767, 32767}, phase: []int{1, 1, 1, 1}, weight: 1},
		{name: "静音", amps: []int{0, 0, 0, 0}, phase: []int{1, 1, 1, 1}, weight: 1},
		{name: "三路叠加", amps: []int{15000, 15000, 15000, 0}, phase: []int{1, 1, 1, 1}, weight: 1},
	}
	tiers := []int{2, 4}

	var rows []row
	for _, n := range tiers {
		for _, s := range scenarios {
			// 预热一次后正式测量，降低首次调度噪声
			runOne(n, s)
			rows = append(rows, runOne(n, s))
		}
	}

	// 不变量断言：所有未触发限幅的行，限幅前后能量差必须严格为零。
	failed := false
	for _, r := range rows {
		if !r.engaged && r.delta != 0 {
			fmt.Fprintf(os.Stderr, "不变量破坏: N=%d %s 未触发限幅但 delta=%v\n", r.n, r.scenario, r.delta)
			failed = true
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# 混音总线基准报告\n\n")
	fmt.Fprintf(&b, "- 采样率 %d Hz，每路 %d 帧（1 s），门限 threshold=%d，权重全部为 1.0\n", rate, frames, threshold)
	fmt.Fprintf(&b, "- 软限幅：门限内恒等线性；超门限 y=T+(C−T)(1−e^{−(|s|−T)/(C−T)})，C=32767，边界 C1 连续，输出恒严格小于 C，不削顶\n")
	fmt.Fprintf(&b, "- 限幅判定：限幅前加权和浮点峰值 > 门限即视为限幅介入\n")
	fmt.Fprintf(&b, "- 耗时为一次完整 `MixBus` 调用（预热一次后测量）\n\n")

	// 表一：峰值与耗时
	b.WriteString("## 表一：峰值与耗时对照\n\n")
	b.WriteString("| 通道数 | 场景 | 限幅前浮点峰值 | 硬削顶峰值 | 软限幅峰值 | 限幅介入 | 耗时 |\n")
	b.WriteString("|---:|:---|---:|---:|---:|:---:|---:|\n")
	for _, r := range rows {
		eng := "否"
		if r.engaged {
			eng = "是"
		}
		fmt.Fprintf(&b, "| %d | %s | %.1f | %d | %d | %s | %v |\n",
			r.n, r.scenario, r.peakPre, r.peakHard, r.peakSoft, eng, r.cost)
	}

	// 表二：能量表
	b.WriteString("\n## 表二：能量表（限幅前后各算一次）\n\n")
	b.WriteString("- E_in = Σᵢ wᵢ²·E(xᵢ)：混音前各加权分量能量合计（不含交叉项，仅供对照）\n")
	b.WriteString("- E_pre：加权求和后、限幅前的信号能量（守恒不变量的基准检查点）\n")
	b.WriteString("- E_post：软限幅并量化后的输出能量；Δ = E_pre − E_post\n")
	b.WriteString("- 判定规则：限幅未介入时 Δ 必须严格为 0 才通过；限幅介入时 Δ>0 为预期（限幅器主动削减的能量）\n\n")
	b.WriteString("| 通道数 | 场景 | E_in | E_pre（限幅前） | E_post（限幅后） | Δ | 限幅介入 | 判定 |\n")
	b.WriteString("|---:|:---|---:|---:|---:|---:|:---:|:---:|\n")
	for _, r := range rows {
		eng := "否"
		if r.engaged {
			eng = "是"
		}
		verdict := "通过"
		if r.engaged {
			verdict = "不适用（限幅削减）"
		} else if !r.conserved {
			verdict = "失败"
		}
		fmt.Fprintf(&b, "| %d | %s | %.1f | %.1f | %.1f | %.1f | %s | %s |\n",
			r.n, r.scenario, r.eIn, r.ePre, r.ePost, r.delta, eng, verdict)
	}

	report := b.String()
	fmt.Print(report)

	out := filepath.Join("artifacts", "mix-report.md")
	if err := os.MkdirAll("artifacts", 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(out, []byte(report), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("\n报告已写入 %s\n", out)

	if failed {
		os.Exit(1)
	}
}
