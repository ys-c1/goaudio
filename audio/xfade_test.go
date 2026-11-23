package audio

import (
	"errors"
	"math"
	"testing"
)

// 构造报告场景：8kHz，A=440Hz，B=660Hz 且初相位错开 π/2，硬切处有咔哒声。
func makePair(t *testing.T, frames int) (a, b *Clip) {
	t.Helper()
	a, err := NewClip(8000, frames)
	if err != nil {
		t.Fatal(err)
	}
	b, err = NewClip(8000, frames)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sine(a, 440, 10000); err != nil {
		t.Fatal(err)
	}
	if err := SinePhase(b, 660, 10000, math.Pi/2); err != nil {
		t.Fatal(err)
	}
	return a, b
}

// 逐样本核验三件事：端点钉死、权重恒等式、混合公式 Err=0。
func assertFadeInvariants(t *testing.T, a, b, out *Clip, fade int) {
	t.Helper()
	nF := len(a.Frames)
	base := nF - fade
	for i := 0; i < fade; i++ {
		wa, wb := arcWeights(i, fade)
		// 权重恒等式：非端点 wa²+wb²=1；端点精确 (1,0)/(0,1)。
		if i == 0 {
			if wa != 1 || wb != 0 {
				t.Fatalf("淡化入口权重未钉死: i=%d wa=%v wb=%v", i, wa, wb)
			}
		} else if i == fade-1 {
			if wa != 0 || wb != 1 {
				t.Fatalf("淡化出口权重未钉死: i=%d wa=%v wb=%v", i, wa, wb)
			}
		} else if d := math.Abs(wa*wa + wb*wb - 1); d > 1e-15 {
			t.Fatalf("权重恒等式失效: i=%d wa²+wb²−1=%v", i, d)
		}
		want := blend(a.Frames[base+i], b.Frames[i], wa, wb)
		if got := out.Frames[base+i]; got != want {
			t.Fatalf("混合公式逐样本不一致: i=%d got=%d want=%d (Err=%d)", i, got, want, int(got)-int(want))
		}
	}
}

func assertSeamDiffsZero(t *testing.T, a, b, out *Clip, fade int) {
	t.Helper()
	for _, s := range SeamSamples(a, b, out, fade) {
		// 单样本淡化时入口/出口退化为同一个混合点，允许跳过；其余点必须 Diff=0。
		if fade == 1 && (s.Name == "entry.right" || s.Name == "exit.left") {
			continue
		}
		if s.Diff != 0 {
			t.Fatalf("接缝不变量失效: %s 下标=%d out=%d src=%d diff=%d",
				s.Name, s.OutIndex, s.Out, s.Src, s.Diff)
		}
	}
}

func TestXFadeShortFade(t *testing.T) {
	a, b := makePair(t, 8000) // 1 s 素材
	const fade = 80           // 10 ms 短淡
	out, err := XFade(a, b, fade)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != 16000-fade {
		t.Fatalf("输出长度错误: %d", len(out.Frames))
	}
	assertFadeInvariants(t, a, b, out, fade)
	assertSeamDiffsZero(t, a, b, out, fade)

	jumps, err := SeamJumps(a, out, fade)
	if err != nil {
		t.Fatal(err)
	}
	if len(jumps) != fade+1 { // 入口对 + 区内 fade−1 对 + 出口对
		t.Fatalf("接缝跳变对数错误: %d", len(jumps))
	}
	maxJump, at := MaxJump(jumps)
	if maxJump <= 0 || at < 0 {
		t.Fatalf("最大跳变统计异常: max=%d at=%d", maxJump, at)
	}
	if err := CheckMaxJump(jumps, 7000); err != nil {
		t.Fatalf("短淡应通过阈值: %v", err)
	}
}

func TestXFadeLongFade(t *testing.T) {
	a, b := makePair(t, 8000)
	const fade = 800 // 100 ms 长淡
	out, err := XFade(a, b, fade)
	if err != nil {
		t.Fatal(err)
	}
	assertFadeInvariants(t, a, b, out, fade)
	assertSeamDiffsZero(t, a, b, out, fade)
	jumps, err := SeamJumps(a, out, fade)
	if err != nil {
		t.Fatal(err)
	}
	if len(jumps) != fade+1 {
		t.Fatalf("接缝跳变对数错误: %d", len(jumps))
	}
	if err := CheckMaxJump(jumps, 7000); err != nil {
		t.Fatalf("长淡应通过阈值: %v", err)
	}
}

func TestXFadeEqualLength(t *testing.T) {
	// 等长素材且淡化区覆盖整条：输出长度等于单条素材长度。
	a, b := makePair(t, 64)
	out, err := XFade(a, b, 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != 64 {
		t.Fatalf("等长全覆盖输出长度错误: %d", len(out.Frames))
	}
	// 入口纯 A、出口纯 B。
	if out.Frames[0] != a.Frames[0] {
		t.Fatalf("入口未保持 A: %d ≠ %d", out.Frames[0], a.Frames[0])
	}
	if out.Frames[63] != b.Frames[63] {
		t.Fatalf("出口未保持 B: %d ≠ %d", out.Frames[63], b.Frames[63])
	}
	assertFadeInvariants(t, a, b, out, 64)
}

func TestXFadeSilence(t *testing.T) {
	a, _ := NewClip(8000, 100)
	b, _ := NewClip(8000, 100)
	out, err := XFade(a, b, 40)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range out.Frames {
		if v != 0 {
			t.Fatalf("静音素材输出第 %d 帧非零: %d", i, v)
		}
	}
	jumps, err := SeamJumps(a, out, 40)
	if err != nil {
		t.Fatal(err)
	}
	maxJump, _ := MaxJump(jumps)
	if maxJump != 0 {
		t.Fatalf("静音接缝跳变应为 0，实际 %d", maxJump)
	}
	if err := CheckMaxJump(jumps, 0); err != nil {
		t.Fatalf("静音在零阈值下也应通过: %v", err)
	}
}

func TestXFadeSingleSample(t *testing.T) {
	// 淡化时长只有 1 个样本：相位取 π/4，两侧等权 √2/2。
	a, b := makePair(t, 100)
	aSnap, bSnap := append([]int16(nil), a.Frames...), append([]int16(nil), b.Frames...)
	out, err := XFade(a, b, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != 199 {
		t.Fatalf("单样本淡化输出长度错误: %d", len(out.Frames))
	}
	wa, wb := arcWeights(0, 1)
	if math.Abs(wa-math.Sqrt(0.5)) > 1e-15 || math.Abs(wb-math.Sqrt(0.5)) > 1e-15 {
		t.Fatalf("单样本权重应为 √2/2: wa=%v wb=%v", wa, wb)
	}
	if math.Abs(wa*wa+wb*wb-1) > 1e-15 {
		t.Fatalf("单样本权重恒等式失效")
	}
	// 混合点逐样本核验：out = round((A+B)/√2)。
	idx := len(a.Frames) - 1
	want := blend(aSnap[idx], bSnap[0], wa, wb)
	if out.Frames[idx] != want {
		t.Fatalf("单样本混合值错误: got=%d want=%d", out.Frames[idx], want)
	}
	// 头部与尾部仍与源素材逐位相等。
	if out.Frames[idx-1] != aSnap[idx-1] || out.Frames[idx+1] != bSnap[1] {
		t.Fatalf("单样本淡化两侧未保持源样本")
	}
	// 接缝不变量：存在的纯源样本点 Diff 必须为 0。
	assertSeamDiffsZero(t, a, b, out, 1)

	// 两条素材本身就只有 1 帧的退化情形。
	a1, _ := NewClip(8000, 1)
	b1, _ := NewClip(8000, 1)
	a1.Frames[0], b1.Frames[0] = 1000, -2000
	out1, err := XFade(a1, b1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(out1.Frames) != 1 || out1.Frames[0] != blend(1000, -2000, wa, wb) {
		t.Fatalf("单帧素材交叉淡化错误: %v", out1.Frames)
	}
}

func TestXFadeErrors(t *testing.T) {
	a, b := makePair(t, 100)
	other, _ := NewClip(44100, 100)

	cases := []struct {
		name      string
		got       error
		wantSenti error
		wantField string
	}{
		{"淡化时长为零", func() error { _, e := XFade(a, b, 0); return e }(), ErrFadeZero, "fadeFrames"},
		{"淡化时长为负", func() error { _, e := XFade(a, b, -3); return e }(), ErrFadeZero, "fadeFrames"},
		{"采样率不一致", func() error { _, e := XFade(a, other, 10); return e }(), ErrRateMismatch, "sampleRate"},
		{"重叠超长(A)", func() error { _, e := XFade(a, b, 101); return e }(), ErrFadeTooLong, "fadeFrames"},
		{"硬切采样率不一致", func() error { _, e := HardCut(a, other); return e }(), ErrRateMismatch, "sampleRate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got == nil {
				t.Fatal("应当返回错误")
			}
			if !errors.Is(tc.got, tc.wantSenti) {
				t.Fatalf("哨兵错误不匹配: got=%v want=%v", tc.got, tc.wantSenti)
			}
			var fe *FieldError
			if !errors.As(tc.got, &fe) {
				t.Fatalf("错误未携带字段名: %T", tc.got)
			}
			if fe.Field != tc.wantField {
				t.Fatalf("字段名错误: got=%s want=%s", fe.Field, tc.wantField)
			}
		})
	}

	// B 侧更短时重叠超长同样报错。
	short, _ := NewClip(8000, 10)
	if _, err := XFade(a, short, 50); !errors.Is(err, ErrFadeTooLong) {
		t.Fatalf("短侧重叠超长应报 ErrFadeTooLong，实际 %v", err)
	}
}

func TestHardCutClickAndThreshold(t *testing.T) {
	a, b := makePair(t, 8000)
	out, err := HardCut(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Frames) != 16000 {
		t.Fatalf("硬切长度错误: %d", len(out.Frames))
	}
	// 硬切就是逐位拼接。
	for i := range a.Frames {
		if out.Frames[i] != a.Frames[i] {
			t.Fatalf("硬切 A 段第 %d 帧被修改", i)
		}
		if out.Frames[8000+i] != b.Frames[i] {
			t.Fatalf("硬切 B 段第 %d 帧被修改", i)
		}
	}
	jumps, err := SeamJumps(a, out, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jumps) != 1 {
		t.Fatalf("硬切只应有拼接点一对跳变，实际 %d", len(jumps))
	}
	maxJump, at := MaxJump(jumps)
	if at != 7999 || maxJump != 13387 {
		t.Fatalf("硬切跳变数据错误: max=%d @%d", maxJump, at)
	}
	if err := CheckMaxJump(jumps, 7000); !errors.Is(err, ErrJumpTooLarge) {
		t.Fatalf("硬切咔哒声应超阈值失败，实际 %v", err)
	}
	// 同样素材交叉淡化后通过阈值，形成对照。
	xf, err := XFade(a, b, 80)
	if err != nil {
		t.Fatal(err)
	}
	xj, err := SeamJumps(a, xf, 80)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckMaxJump(xj, 7000); err != nil {
		t.Fatalf("交叉淡化不应超阈值: %v", err)
	}
}

func TestXFadeDoesNotMutateInputs(t *testing.T) {
	a, b := makePair(t, 128)
	aSnap, bSnap := append([]int16(nil), a.Frames...), append([]int16(nil), b.Frames...)
	if _, err := XFade(a, b, 32); err != nil {
		t.Fatal(err)
	}
	for i := range aSnap {
		if a.Frames[i] != aSnap[i] || b.Frames[i] != bSnap[i] {
			t.Fatalf("第 %d 帧入参被修改", i)
		}
	}
}

func TestJumpsRangeError(t *testing.T) {
	a, _ := NewClip(8000, 10)
	if _, err := Jumps(a, 5, 4); !errors.Is(err, ErrRange) {
		t.Fatalf("非法区间应报 ErrRange，实际 %v", err)
	}
	if _, err := Jumps(a, 0, 11); !errors.Is(err, ErrRange) {
		t.Fatalf("越界区间应报 ErrRange，实际 %v", err)
	}
}

func TestBlendSaturation(t *testing.T) {
	// 混合后超出 int16 范围必须饱和而不是回绕。
	if got := blend(math.MaxInt16, math.MaxInt16, 0.9, 0.9); got != math.MaxInt16 {
		t.Fatalf("正向饱和失效: %d", got)
	}
	if got := blend(math.MinInt16, math.MinInt16, 0.9, 0.9); got != math.MinInt16 {
		t.Fatalf("负向饱和失效: %d", got)
	}
}
