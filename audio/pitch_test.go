package audio

import (
	"math"
	"strings"
	"testing"
)

func framesToFloat(c *Clip) []float64 {
	out := make([]float64, len(c.Frames))
	for i, v := range c.Frames {
		out[i] = float64(v)
	}
	return out
}

// 单音：主频必须移到目标值，时长必须不变，两条分开验。
func TestPitchShiftSingleTone(t *testing.T) {
	const rate = 44100
	const frames = rate // 1 秒
	c, err := NewClip(rate, frames)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sine(c, 440, 10000); err != nil {
		t.Fatal(err)
	}
	if err := PitchShift(c, 660, 2048); err != nil {
		t.Fatal(err)
	}

	// 验证一：时长不变
	durIn := float64(frames) / rate
	durOut := float64(len(c.Frames)) / rate
	durDev := math.Abs(durOut - durIn)
	if len(c.Frames) != frames {
		t.Fatalf("时长改变: %d -> %d 帧", frames, len(c.Frames))
	}

	// 验证二：主频移到目标值
	f := dominantFreq(framesToFloat(c), rate)
	freqDev := math.Abs(f - 660)
	if freqDev > 2 {
		t.Fatalf("主频应为 660Hz，实际 %.2fHz（偏差 %.2fHz）", f, freqDev)
	}
	if p := Peak(c); p < 5000 {
		t.Fatalf("变调后幅度异常塌陷: %d", p)
	}
	t.Logf("主频偏差 %.3f Hz（目标 660 Hz，实测 %.3f Hz）；时长偏差 %.6f s（%d 帧）",
		freqDev, f, durDev, len(c.Frames)-frames)
}

// 和弦：整体按比例搬移，最强分音移到目标值，时长不变。
func TestPitchShiftChord(t *testing.T) {
	const rate = 44100
	c, _ := NewClip(rate, rate)
	for i := range c.Frames {
		tm := float64(i) / rate
		v := 6000*math.Sin(2*math.Pi*220*tm) +
			8000*math.Sin(2*math.Pi*330*tm) +
			10000*math.Sin(2*math.Pi*440*tm)
		c.Frames[i] = int16(v)
	}
	before := len(c.Frames)
	if err := PitchShift(c, 880, 2048); err != nil { // 最强分音 440 -> 880
		t.Fatal(err)
	}
	if len(c.Frames) != before {
		t.Fatalf("时长改变: %d -> %d 帧", before, len(c.Frames))
	}
	f := dominantFreq(framesToFloat(c), rate)
	if dev := math.Abs(f - 880); dev > 5 {
		t.Fatalf("和弦主频应为 880Hz，实际 %.2fHz", f)
	}
	t.Logf("和弦主频 %.2f Hz（目标 880 Hz）", f)
}

// 噪声：能正常处理，时长不变，能量不塌陷。
func TestPitchShiftNoise(t *testing.T) {
	const rate = 44100
	c, _ := NewClip(rate, rate/2)
	seed := uint32(12345)
	for i := range c.Frames {
		seed = seed*1103515245 + 12345
		c.Frames[i] = int16(int32(seed>>16)%20000 - 10000)
	}
	f0 := dominantFreq(framesToFloat(c), rate)
	if f0 <= 0 {
		t.Fatal("噪声应能测到主频")
	}
	target := f0 * 1.3
	if target >= rate/2 {
		target = f0 * 0.7
	}
	before := len(c.Frames)
	if err := PitchShift(c, target, 2048); err != nil {
		t.Fatal(err)
	}
	if len(c.Frames) != before {
		t.Fatalf("时长改变: %d -> %d 帧", before, len(c.Frames))
	}
	if p := Peak(c); p < 1000 {
		t.Fatalf("噪声变调后能量异常: %d", p)
	}
	t.Logf("噪声主频 %.1f -> 目标 %.1f Hz，峰值 %d", f0, target, Peak(c))
}

// 扫频：能正常处理，时长不变。
func TestPitchShiftSweep(t *testing.T) {
	const rate = 44100
	c, _ := NewClip(rate, rate)
	for i := range c.Frames {
		tm := float64(i) / rate
		phase := 2 * math.Pi * (200*tm + 900*tm*tm) // 200 -> 2000 Hz 线性扫频
		c.Frames[i] = int16(10000 * math.Sin(phase))
	}
	before := len(c.Frames)
	if err := PitchShift(c, 1500, 2048); err != nil {
		t.Fatal(err)
	}
	if len(c.Frames) != before {
		t.Fatalf("时长改变: %d -> %d 帧", before, len(c.Frames))
	}
	if p := Peak(c); p < 1000 {
		t.Fatalf("扫频变调后能量异常: %d", p)
	}
}

// 静音：测不到主频，保持不变，时长不变，不报错。
func TestPitchShiftSilence(t *testing.T) {
	c, _ := NewClip(44100, 44100)
	if err := PitchShift(c, 660, 2048); err != nil {
		t.Fatal(err)
	}
	if len(c.Frames) != 44100 {
		t.Fatalf("时长改变: %d", len(c.Frames))
	}
	if p := Peak(c); p != 0 {
		t.Fatalf("静音变调后应仍为静音，峰值 %d", p)
	}
}

// 三类非法参数都要拦下，并点出是哪个参数。
func TestPitchShiftErrors(t *testing.T) {
	c, _ := NewClip(8000, 800)

	if err := PitchShift(c, 440, 0); err == nil || !strings.Contains(err.Error(), "帧长") {
		t.Fatalf("帧长为零应报出参数“帧长”: %v", err)
	}
	if err := PitchShift(c, 0, 1024); err == nil || !strings.Contains(err.Error(), "目标频率") {
		t.Fatalf("目标频率为零应报出参数“目标频率”: %v", err)
	}
	if err := PitchShift(c, 4000, 1024); err == nil || !strings.Contains(err.Error(), "目标频率") {
		t.Fatalf("目标频率达到奈奎斯特应报出参数“目标频率”: %v", err)
	}
	bad := &Clip{Rate: 0, Frames: make([]int16, 100)}
	if err := PitchShift(bad, 440, 1024); err == nil || !strings.Contains(err.Error(), "采样率") {
		t.Fatalf("采样率为零应报出参数“采样率”: %v", err)
	}
}
