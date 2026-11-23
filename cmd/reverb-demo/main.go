package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"goaudio/audio"
)

const rate = 44100

// 两档衰减时长（秒），尾端电平统一取 -60dB = 0.001
var presets = []struct {
	name  string
	tau   float64
	level float64
}{
	{"短尾音 0.30s", 0.30, 0.001},
	{"长尾音 1.50s", 1.50, 0.001},
}

type testCase struct {
	name   string
	frames []int16
}

func buildCases() []testCase {
	n := rate // 1 秒
	sine := make([]int16, n)
	for i := range sine {
		t := float64(i) / float64(rate)
		sine[i] = int16(20000 * math.Sin(2*math.Pi*440*t))
	}
	silence := make([]int16, n)
	full := make([]int16, n)
	for i := range full {
		full[i] = 32767
	}
	return []testCase{
		{"脉冲", []int16{10000}},
		{"正弦440Hz", sine},
		{"静音", silence},
		{"满幅", full},
		{"极短片段(2帧)", []int16{32767, -32768}},
	}
}

func main() {
	outDir := "artifacts/reverb"
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}
	cases := buildCases()

	fmt.Println("================ 1. 衰减时间反解（两档时长） ================")
	fmt.Printf("%-14s %10s %10s %12s %14s\n", "档位", "τ(s)", "尾音L(帧)", "尾音时长", "衰减系数α")
	rev := make([]*audio.Reverb, len(presets))
	for i, p := range presets {
		r, err := audio.NewReverb(rate, p.tau, p.level, true)
		if err != nil {
			panic(err)
		}
		rev[i] = r
		fmt.Printf("%-14s %10.3f %10d %12s %14.8f  核能量=%.10f\n",
			p.name, p.tau, r.TailSamples, r.TailDuration().Round(time.Millisecond),
			r.Alpha, r.KernelEnergy())
	}

	fmt.Println()
	fmt.Println("================ 2. 五类用例：卷积前后能量比（浮点域，未量化） ================")
	for i, p := range presets {
		r := rev[i]
		fmt.Printf("--- %s (L=%d, α=%.8f) ---\n", p.name, r.TailSamples, r.Alpha)
		fmt.Printf("%-16s %8s %12s %14s %14s %14s %16s\n",
			"用例", "输入帧", "输出帧(含尾)", "前能量", "后能量(浮点)", "能量比(浮点)", "能量比(int16)")
		for _, tc := range cases {
			c := &audio.Clip{Rate: rate, Frames: tc.frames}
			inF := audio.FramesToFloat(tc.frames)
			yF, err := r.ProcessFloat(c)
			if err != nil {
				panic(err)
			}
			out, err := r.Process(c)
			if err != nil {
				panic(err)
			}
			eIn, eOutF := audio.Energy(inF), audio.Energy(yF)
			eOutQ := audio.Energy(audio.FramesToFloat(out.Frames)) // int16 钳位、量化后的实际输出能量
			ratioF, ratioQ := math.NaN(), math.NaN()
			if eIn > 0 {
				ratioF = eOutF / eIn
				ratioQ = eOutQ / eIn
			}
			fmt.Printf("%-16s %8d %12d %14.3f %14.3f %14s %16s\n",
				tc.name, len(tc.frames), len(yF), eIn, eOutF,
				nanOrFmt(ratioF, "0/0(静音)"), nanOrFmt(ratioQ, "0/0(静音)"))
		}
		fmt.Println()
	}

	fmt.Println("================ 3. 两档时长耗时（同一段 1 秒正弦，各 50 次） ================")
	bench := cases[1] // 正弦
	for i, p := range presets {
		r := rev[i]
		c := &audio.Clip{Rate: rate, Frames: bench.frames}
		// 预热
		if _, err := r.ProcessFloat(c); err != nil {
			panic(err)
		}
		start := time.Now()
		const iters = 50
		for k := 0; k < iters; k++ {
			if _, err := r.ProcessFloat(c); err != nil {
				panic(err)
			}
		}
		avg := time.Since(start) / iters
		fmt.Printf("%-14s 尾音长度=%6d 帧 (%5.3fs)  卷积平均耗时=%v\n",
			p.name, r.TailSamples, r.TailDuration().Seconds(), avg)
	}

	fmt.Println()
	fmt.Println("================ 4. 非法参数（必须报出参数名） ================")
	check := func(label string, err error) {
		if err == nil {
			fmt.Printf("%-22s 未报错（缺陷！）\n", label)
		} else {
			fmt.Printf("%-22s -> %s\n", label, err)
		}
	}
	_, err := audio.NewReverb(rate, 0, 0.001, true)
	check("时长为零 decayTime=0", err)
	_, err = audio.NewReverb(rate, 0.5, 0, true)
	check("衰减为零 decayLevel=0", err)
	badRate, _ := audio.NewClip(8000, 16)
	_, err = rev[0].Process(badRate)
	check("采样率不一致 8000≠44100", err)

	fmt.Println()
	fmt.Println("================ 5. 未启用：逐样本输出不变性 ================")
	off, err := audio.NewReverb(rate, 0.5, 0.001, false)
	if err != nil {
		panic(err)
	}
	src := &audio.Clip{Rate: rate, Frames: cases[1].frames[:1000]}
	dry, err := off.Process(src)
	if err != nil {
		panic(err)
	}
	identical := len(dry.Frames) == len(src.Frames)
	if identical {
		for k := range src.Frames {
			if dry.Frames[k] != src.Frames[k] {
				identical = false
				break
			}
		}
	}
	fmt.Printf("Enabled=false: 长度 %d->%d, 逐样本一致=%v\n",
		len(src.Frames), len(dry.Frames), identical)

	// 尾音长度-衰减系数曲线数据
	csvPath := filepath.Join(outDir, "curve.csv")
	if err := writeCurveCSV(csvPath); err != nil {
		panic(err)
	}
	htmlPath := filepath.Join(outDir, "curve.html")
	if err := writeCurveHTML(htmlPath); err != nil {
		panic(err)
	}
	fmt.Println()
	fmt.Printf("曲线数据: %s\n", csvPath)
	fmt.Printf("曲线图:  %s\n", htmlPath)
}

func nanOrFmt(v float64, fallback string) string {
	if math.IsNaN(v) {
		return fallback
	}
	return fmt.Sprintf("%10.6f", v)
}

// writeCurveCSV 输出两档尾电平下 尾音长度L ↔ 衰减系数α 的对应曲线。
func writeCurveCSV(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintln(f, "tau_s,tail_samples_L,alpha_level_-60dB,alpha_level_-40dB")
	const points = 120
	for k := 1; k <= points; k++ {
		tau := 0.005 * math.Pow(2.0/0.005, float64(k)/float64(points)) // 5ms..2s 对数取点
		L := int(math.Round(tau * rate))
		if L < 2 {
			L = 2
		}
		a60 := math.Pow(0.001, 1.0/float64(L-1))
		a40 := math.Pow(0.01, 1.0/float64(L-1))
		fmt.Fprintf(f, "%.6f,%d,%.10f,%.10f\n", tau, L, a60, a40)
	}
	return nil
}
