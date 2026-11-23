package audio

import (
	"errors"
	"math"
	"math/rand"
	"testing"
)

// 端点检测验收用例：单段、多段、渐弱、背景噪声、全静音。
// 每个有真实发声的用例都逐条核对：
//   1. 检测有声段覆盖全部真实有声样本（漏检率 <= 允许值）；
//   2. 任一检测段不跨越两次真实发声之间的静音。

type truthCase struct {
	name  string
	rate  int
	total float64 // ms
	truth []Segment
	make  func(c *Clip)
}

func msToN(rate int, ms float64) int { return int(math.Round(float64(rate) * ms / 1000.0)) }

// addTone 在 [a,b) 写入正弦，两端 3ms 余弦过渡避免爆音。
func addTone(c *Clip, a, b int, freq, ampStart, ampEnd float64) {
	edge := msToN(c.Rate, 3)
	for i := a; i < b; i++ {
		t := float64(i) / float64(c.Rate)
		frac := float64(i-a) / float64(b-a)
		amp := ampStart + (ampEnd-ampStart)*frac
		g := 1.0
		if i-a < edge {
			g = math.Min(g, 0.5-0.5*math.Cos(math.Pi*float64(i-a)/float64(edge)))
		}
		if b-i-1 < edge {
			g = math.Min(g, 0.5-0.5*math.Cos(math.Pi*float64(b-i-1)/float64(edge)))
		}
		c.Frames[i] += int16(amp * g * math.Sin(2*math.Pi*freq*t))
	}
}

func addWhiteNoise(c *Clip, amp float64, seed int64) {
	r := rand.New(rand.NewSource(seed))
	for i := range c.Frames {
		c.Frames[i] += int16((r.Float64()*2 - 1) * amp)
	}
}

func buildcase(tc truthCase) *Clip {
	c, err := NewClip(tc.rate, msToN(tc.rate, tc.total))
	if err != nil {
		panic(err)
	}
	tc.make(c)
	return c
}

func cases() []truthCase {
	const rate = 8000
	seg := func(a, b float64) Segment {
		return Segment{Start: msToN(rate, a), End: msToN(rate, b)}
	}
	return []truthCase{
		{
			name: "单段", rate: rate, total: 1500,
			truth: []Segment{seg(400, 1000)},
			make:  func(c *Clip) { addTone(c, seg(400, 1000).Start, seg(400, 1000).End, 300, 9000, 9000) },
		},
		{
			name: "多段", rate: rate, total: 3000,
			truth: []Segment{seg(300, 700), seg(1200, 1600), seg(2100, 2500)},
			make: func(c *Clip) {
				for _, s := range []struct {
					a, b, f float64
				}{{300, 700, 300}, {1200, 1600, 420}, {2100, 2500, 260}} {
					addTone(c, seg(s.a, s.b).Start, seg(s.a, s.b).End, s.f, 8000, 8000)
				}
			},
		},
		{
			name: "渐弱", rate: rate, total: 1600,
			truth: []Segment{seg(300, 1300)},
			make:  func(c *Clip) { addTone(c, seg(300, 1300).Start, seg(300, 1300).End, 400, 12000, 2500) },
		},
		{
			name: "背景噪声", rate: rate, total: 2200,
			truth: []Segment{seg(500, 1200)},
			make: func(c *Clip) {
				addWhiteNoise(c, 700, 7)
				addTone(c, seg(500, 1200).Start, seg(500, 1200).End, 250, 9000, 9000)
			},
		},
		{
			name: "全静音", rate: rate, total: 1200,
			truth: nil,
			make:  func(c *Clip) {},
		},
	}
}

func TestEndpointsAcceptance(t *testing.T) {
	for _, tc := range cases() {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			c := buildcase(tc)
			cfg := DefaultEndpointConfig()
			res, err := DetectEndpoints(c, cfg)
			if err != nil {
				t.Fatal(err)
			}

			// 实际窗长不得超过反解上限。
			if res.WindowSamples > res.WindowBound {
				t.Fatalf("窗长 %d 超过反解上限 %d", res.WindowSamples, res.WindowBound)
			}

			chk := CheckCoverage(res.Speech, tc.truth)

			// 核对 1：覆盖全部真实有声，漏检率不超过允许值。
			if chk.MissRate > res.AllowedMissRate {
				t.Fatalf("漏检率 %.4f 超过允许值 %.4f（每段 %+v）",
					chk.MissRate, res.AllowedMissRate, chk.PerSpan)
			}
			for _, ps := range chk.PerSpan {
				if !ps.FullyCovered {
					t.Fatalf("真实发声 %v 未被完整覆盖，漏 %d 样本", ps.Truth, ps.Missed)
				}
			}
			// 核对 2：检测段不跨越两次发声之间的静音。
			if chk.Crossings != 0 {
				t.Fatalf("有 %d 个检测段跨越了发声之间的静音: %v", chk.Crossings, res.Speech)
			}

			// 全静音不得造出有声段；有发声用例不应凭空多段。
			if len(tc.truth) == 0 && len(res.Speech) != 0 {
				t.Fatalf("全静音应无有声段，得到 %v", res.Speech)
			}
			if len(tc.truth) > 0 && len(res.Speech) != len(tc.truth) {
				t.Fatalf("段数 %d 与真实 %d 不符: %v", len(res.Speech), len(tc.truth), res.Speech)
			}
			if chk.ExtraSegments != 0 {
				t.Fatalf("存在不覆盖任何真实发声的检测段 %d", chk.ExtraSegments)
			}

			// 静音段 = 有声段补集，两者应无缝覆盖整段素材。
			if got, want := res.SpeechSamples()+res.SilenceSamples(), len(c.Frames); got != want {
				t.Fatalf("有声+静音 = %d，应为 %d", got, want)
			}
			if len(res.Silence) == 0 {
				t.Fatal("应至少有一个静音段")
			}
		})
	}
}

func TestEndpointSilenceSegmentsListed(t *testing.T) {
	// 多段用例：有声 3 段 => 静音 4 段（首、两个间隔、尾）。
	rate := 8000
	c, _ := NewClip(rate, msToN(rate, 3000))
	for _, s := range [][2]float64{{300, 700}, {1200, 1600}, {2100, 2500}} {
		addTone(c, msToN(rate, s[0]), msToN(rate, s[1]), 350, 8000, 8000)
	}
	res, err := DetectEndpoints(c, DefaultEndpointConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Speech) != 3 || len(res.Silence) != 4 {
		t.Fatalf("应有声 3 段、静音 4 段，得到有声 %d 静音 %d", len(res.Speech), len(res.Silence))
	}
	for _, s := range res.Silence {
		if s.Duration() <= 0 {
			t.Fatalf("静音段长度非正: %v", s)
		}
	}
}

func TestEndpointParamsRejected(t *testing.T) {
	rate := 8000
	c, _ := NewClip(rate, msToN(rate, 1000))
	addTone(c, 0, len(c.Frames), 300, 8000, 8000)

	cases := []struct {
		name   string
		cfg    func() EndpointConfig
		clip   *Clip
		param  string
		target error
	}{
		{
			name: "窗长为零", clip: c, param: "windowLen", target: ErrWindow,
			cfg: func() EndpointConfig { g := DefaultEndpointConfig(); g.WindowMs = 0; return g },
		},
		{
			name: "窗长为负", clip: c, param: "windowLen", target: ErrWindow,
			cfg: func() EndpointConfig { g := DefaultEndpointConfig(); g.WindowMs = -8; return g },
		},
		{
			name: "采样率为零", clip: &Clip{Rate: 0, Frames: make([]int16, 800)}, param: "sampleRate", target: ErrSampleRate,
			cfg: func() EndpointConfig { return DefaultEndpointConfig() },
		},
		{
			name: "时长为零", clip: &Clip{Rate: rate, Frames: nil}, param: "duration", target: ErrDuration,
			cfg: func() EndpointConfig { return DefaultEndpointConfig() },
		},
		{
			name: "最短发声时长为零", clip: c, param: "minSpeechMs", target: ErrDuration,
			cfg: func() EndpointConfig { g := DefaultEndpointConfig(); g.MinSpeechMs = 0; return g },
		},
		{
			name: "静音合并时长为零", clip: c, param: "minSilenceMs", target: ErrDuration,
			cfg: func() EndpointConfig { g := DefaultEndpointConfig(); g.MinSilenceMs = 0; return g },
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := DetectEndpoints(tc.clip, tc.cfg())
			if err == nil {
				t.Fatal("应当报错")
			}
			var pe *ParamError
			if !errors.As(err, &pe) {
				t.Fatalf("应为 ParamError，得到 %T: %v", err, err)
			}
			if pe.Param != tc.param {
				t.Fatalf("参数名应为 %q，得到 %q：%v", tc.param, pe.Param, err)
			}
			if !errors.Is(err, tc.target) {
				t.Fatalf("错误应可归为 %v：%v", tc.target, err)
			}
		})
	}
}

func TestWindowBoundSolved(t *testing.T) {
	// 反解式 2W ≤ p·T_min：p=5%、T_min=400ms、8kHz => 上限 80 样本（10ms）。
	// 默认实际窗长 8ms = 64 样本，合规。
	bound, err := SolveWindowBound(8000, 400, 0.05)
	if err != nil {
		t.Fatal(err)
	}
	if bound != 80 {
		t.Fatalf("反解上限应为 80，得到 %d", bound)
	}
	c, _ := NewClip(8000, 16000)
	cfg := DefaultEndpointConfig()
	if msToN(8000, cfg.WindowMs) > bound {
		t.Fatalf("默认窗长 %v ms 超过反解上限 %d 样本", cfg.WindowMs, bound)
	}
	cfg.WindowMs = 10.5 // 84 样本 > 80
	_, err = DetectEndpoints(c, cfg)
	if !errors.Is(err, ErrWindowBound) {
		t.Fatalf("超过反解上限应报 ErrWindowBound，得到 %v", err)
	}
	var pe *ParamError
	if !errors.As(err, &pe) || pe.Param != "windowLen" {
		t.Fatalf("应指明 windowLen：%v", err)
	}
	// 恰好等于上限应通过（10ms = 80 样本）。
	cfg.WindowMs = 10
	if _, err := DetectEndpoints(c, cfg); err != nil {
		t.Fatalf("窗长等于反解上限应通过：%v", err)
	}

	// 反解值随 p 与 T_min 变化：p 减半，上限减半；T_min 加倍，上限加倍。
	if b, _ := SolveWindowBound(8000, 400, 0.025); b != 40 {
		t.Fatalf("p=2.5%% 时上限应为 40，得到 %d", b)
	}
	if b, _ := SolveWindowBound(8000, 800, 0.05); b != 160 {
		t.Fatalf("T_min=800ms 时上限应为 160，得到 %d", b)
	}
	// 反解值不足以容纳 1 个样本时必须报错，而不是返回 0 窗长。
	if _, err := SolveWindowBound(8000, 10, 0.01); err == nil {
		t.Fatal("反解上限不足 1 样本应报错")
	}
}

func TestShortSegmentsMerged(t *testing.T) {
	// 两段发声之间只留 60ms 空隙（< MinSilenceMs=200ms），应合并为一段。
	rate := 8000
	c, _ := NewClip(rate, msToN(rate, 1600))
	addTone(c, msToN(rate, 300), msToN(rate, 700), 300, 8000, 8000)
	addTone(c, msToN(rate, 760), msToN(rate, 1100), 300, 8000, 8000)
	res, err := DetectEndpoints(c, DefaultEndpointConfig())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Speech) != 1 {
		t.Fatalf("60ms 间隙应合并为 1 段，得到 %d 段：%v", len(res.Speech), res.Speech)
	}
	// 合并段必须覆盖两段真实发声。
	chk := CheckCoverage(res.Speech, []Segment{
		{Start: msToN(rate, 300), End: msToN(rate, 700)},
		{Start: msToN(rate, 760), End: msToN(rate, 1100)},
	})
	if chk.MissRate > 0.05 {
		t.Fatalf("合并后漏检率 %.4f 过高", chk.MissRate)
	}
}

func TestCheckCoverageDetectsCrossing(t *testing.T) {
	truth := []Segment{{Start: 100, End: 200}, {Start: 400, End: 500}}
	// 一个连通的检测段同时盖住两段真实发声 => 必须计为跨越。
	chk := CheckCoverage([]Segment{{Start: 90, End: 510}}, truth)
	if chk.Crossings != 1 {
		t.Fatalf("应检出 1 次跨越，得到 %d", chk.Crossings)
	}
	// 分开的检测段不跨越，且不漏检。
	chk = CheckCoverage([]Segment{{Start: 90, End: 210}, {Start: 390, End: 510}}, truth)
	if chk.Crossings != 0 || chk.MissedSamples != 0 {
		t.Fatalf("不应跨越或漏检：%+v", chk)
	}
	// 漏掉第二段的一半。
	chk = CheckCoverage([]Segment{{Start: 90, End: 210}, {Start: 390, End: 450}}, truth)
	if chk.MissedSamples != 50 {
		t.Fatalf("应漏检 50 样本，得到 %d", chk.MissedSamples)
	}
}
