package audio

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// sineClip 生成指定相位与幅度的正弦单声道 Clip，相位参数用于构造反相素材。
func sineClip(rate, frames int, freq float64, amp float64, phase float64) *Clip {
	c := &Clip{Rate: rate, Frames: make([]int16, frames)}
	for i := range c.Frames {
		t := float64(i) / float64(rate)
		c.Frames[i] = int16(math.Round(amp * math.Sin(2*math.Pi*freq*t+phase)))
	}
	return c
}

func multi(t *testing.T, clips ...*Clip) *MultiClip {
	t.Helper()
	m, err := NewMultiClip(clips...)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestDownmixCenter 居中：两个等能但独立（不同频率）的声道对称排布，
// 下混后左右能量均衡、总能量守恒、相关系数落在默认区间。
func TestDownmixCenter(t *testing.T) {
	m := multi(t,
		sineClip(8000, 1024, 300, 4000, 0),
		sineClip(8000, 1024, 700, 4000, 0),
	)
	mat, rep, err := m.SolveEnergyMatrix(0.9)
	if err != nil {
		t.Fatalf("居中独立素材应可解: %v", err)
	}
	if math.Abs(rep.EnergyRatio-1) > 1e-6 {
		t.Fatalf("总能量不守恒: 比值 %.8f", rep.EnergyRatio)
	}
	// 两声道能量相同、频率不同，有限帧下统计能量略有起伏，按 2% 容差核验均衡。
	if math.Abs(rep.OutputLeft-rep.OutputRight)/rep.OutputLeft > 0.02 {
		t.Fatalf("居中素材左右能量应相等: %.2f vs %.2f", rep.OutputLeft, rep.OutputRight)
	}
	if math.Abs(rep.Correlation) > 0.9+rhoTolerance {
		t.Fatalf("相关系数越界: %.4f", rep.Correlation)
	}
	for j := range mat.Left {
		if mat.Left[j] <= 0 || mat.Right[j] <= 0 {
			t.Fatalf("第 %d 声道系数必须严格为正: %v %v", j+1, mat.Left[j], mat.Right[j])
		}
	}
	if _, _, err := m.DownmixMatrix(mat, 0.9); err != nil {
		t.Fatalf("矩阵下混核验失败: %v", err)
	}
	t.Log("\n" + rep.String())
}

// TestDownmixIdenticalCorrelated 两路完全同相的同一信号：
// 数学上 L、R 必为该信号的标量倍数，|ρ|≡1，任何矩阵都无法降低。
// 严格区间必须如实触发相关系数核验错误；放宽到 [-1,1] 后仍应能量守恒、声像均衡。
func TestDownmixIdenticalCorrelated(t *testing.T) {
	m := multi(t,
		sineClip(8000, 1024, 440, 4000, 0),
		sineClip(8000, 1024, 440, 4000, 0),
	)
	if _, _, err := m.SolveEnergyMatrix(0.9); !errors.Is(err, ErrCorrelation) {
		t.Fatalf("完全相关素材在严格区间下应报 ErrCorrelation，得到 %v", err)
	}
	mat, rep, err := m.SolveEnergyMatrix(1.0)
	if err != nil {
		t.Fatalf("放宽区间到 [-1,1] 后应可解: %v", err)
	}
	if math.Abs(rep.EnergyRatio-1) > 1e-6 {
		t.Fatalf("总能量不守恒: %.8f", rep.EnergyRatio)
	}
	if math.Abs(rep.OutputLeft-rep.OutputRight)/rep.OutputLeft > 1e-9 {
		t.Fatalf("同相双声道应左右均衡: 左 %.2f 右 %.2f", rep.OutputLeft, rep.OutputRight)
	}
	if _, _, err := m.DownmixMatrix(mat, 1.0); err != nil {
		t.Fatal(err)
	}
}

// TestDownmixPanLeft 偏左：左声道能量远高于右声道，下混后左通道能量必须更大。
func TestDownmixPanLeft(t *testing.T) {
	m := multi(t,
		sineClip(8000, 1024, 300, 6000, 0), // 声道1：大声，偏左
		sineClip(8000, 1024, 700, 1000, 0), // 声道2：小声，偏右
	)
	mat, rep, err := m.SolveEnergyMatrix(DefaultRhoLimit)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OutputLeft <= rep.OutputRight {
		t.Fatalf("偏左素材下混后左通道应更响: 左 %.2f 右 %.2f", rep.OutputLeft, rep.OutputRight)
	}
	if math.Abs(rep.EnergyRatio-1) > 1e-6 {
		t.Fatalf("总能量不守恒: %.8f", rep.EnergyRatio)
	}
	out, _, err := m.DownmixMatrix(mat, DefaultRhoLimit)
	if err != nil {
		t.Fatal(err)
	}
	if eL, eR := channelEnergy(out.Left), channelEnergy(out.Right); eL <= eR {
		t.Fatalf("整型输出偏左失败: 左 %.2f 右 %.2f", eL, eR)
	}
}

// TestDownmixPanRight 偏右：右声道能量远高于左声道，下混后右通道能量必须更大。
func TestDownmixPanRight(t *testing.T) {
	m := multi(t,
		sineClip(8000, 1024, 300, 1000, 0), // 声道1：小声，偏左
		sineClip(8000, 1024, 700, 6000, 0), // 声道2：大声，偏右
	)
	mat, rep, err := m.SolveEnergyMatrix(DefaultRhoLimit)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OutputRight <= rep.OutputLeft {
		t.Fatalf("偏右素材下混后右通道应更响: 左 %.2f 右 %.2f", rep.OutputLeft, rep.OutputRight)
	}
	if math.Abs(rep.EnergyRatio-1) > 1e-6 {
		t.Fatalf("总能量不守恒: %.8f", rep.EnergyRatio)
	}
	if _, _, err := m.DownmixMatrix(mat, DefaultRhoLimit); err != nil {
		t.Fatal(err)
	}
}

// TestDownmixAntiphase 反相：两声道能量相同、相位相反。
// 直接相加会完全抵消；矩阵下混必须保住两个声道的能量并通过相关系数核验。
func TestDownmixAntiphase(t *testing.T) {
	m := multi(t,
		sineClip(8000, 1024, 440, 4000, 0),
		sineClip(8000, 1024, 440, 4000, math.Pi),
	)

	// 直接相加接口保持朴素语义：反相素材相加近似静音。
	sum, err := m.DownmixSum()
	if err != nil {
		t.Fatal(err)
	}
	if got := channelEnergy(sum.Left); got > 1 {
		t.Fatalf("直接相加接口语义被改变，反相应近静音，能量 %.4f", got)
	}

	// 纯反相的同一信号同样满足 |ρ|≡1，严格区间须如实报错，且与抵消核验互不吞并。
	if _, _, err := m.SolveEnergyMatrix(0.9); !errors.Is(err, ErrCorrelation) {
		t.Fatalf("纯反相素材在严格区间下应报 ErrCorrelation，得到 %v", err)
	}

	// 放宽到 [-1,1]：能量守恒，且没有声道被抵消。
	mat, rep, err := m.SolveEnergyMatrix(1.0)
	if err != nil {
		t.Fatalf("反相素材应能解出合规矩阵: %v", err)
	}
	if math.Abs(rep.EnergyRatio-1) > 1e-6 {
		t.Fatalf("反相素材总能量不守恒: %.8f", rep.EnergyRatio)
	}
	if err := VerifyNoCancellation(mat, m); err != nil {
		t.Fatalf("单独发声声道被抵消: %v", err)
	}
	out, _, err := m.DownmixMatrix(mat, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	if got := channelEnergy(out.Left) + channelEnergy(out.Right); got < 1 {
		t.Fatal("反相素材经矩阵下混后被整体抵消")
	}
	t.Log("\n" + rep.String())
}

// TestDownmixMono 单声道：单声道下混必须逐样本保持能量（系数平方和为 1）。
func TestDownmixMono(t *testing.T) {
	src := sineClip(44100, 2048, 1000, 5000, 0)
	m := multi(t, src)
	// 单声源下 |ρ|≡1，严格区间应触发相关系数核验错误。
	if _, _, err := m.SolveEnergyMatrix(0.9); !errors.Is(err, ErrCorrelation) {
		t.Fatalf("单声道在严格区间下应报 ErrCorrelation，得到 %v", err)
	}
	mat, rep, err := m.SolveEnergyMatrix(1.0)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(mat.Left); n != 1 {
		t.Fatalf("单声道矩阵列数异常: %d", n)
	}
	colNorm := mat.Left[0]*mat.Left[0] + mat.Right[0]*mat.Right[0]
	if math.Abs(colNorm-1) > 1e-9 {
		t.Fatalf("单声道列向量必须单位化，平方和 %.8f", colNorm)
	}
	if math.Abs(rep.EnergyRatio-1) > 1e-9 {
		t.Fatalf("单声道能量必须严格守恒: %.8f", rep.EnergyRatio)
	}
	out, _, err := m.DownmixMatrix(mat, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	// float 层已严格守恒；整型输出只受 int16 舍入量化影响，容差 1e-4。
	if eIn, eOut := channelEnergy(src.Frames), channelEnergy(out.Left)+channelEnergy(out.Right); math.Abs(eOut-eIn)/eIn > 1e-4 {
		t.Fatalf("整型输出能量偏差过大: 入 %.2f 出 %.2f", eIn, eOut)
	}
}

// TestDownmixFiveChannels 五声道常见布局：能量守恒与左右偏置同时成立。
func TestDownmixFiveChannels(t *testing.T) {
	m := multi(t,
		sineClip(48000, 2048, 200, 5000, 0),
		sineClip(48000, 2048, 300, 5000, 0),
		sineClip(48000, 2048, 400, 5000, 0),
		sineClip(48000, 2048, 500, 5000, 0),
		sineClip(48000, 2048, 600, 5000, 0),
	)
	mat, rep, err := m.SolveEnergyMatrix(DefaultRhoLimit)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(rep.EnergyRatio-1) > 1e-6 {
		t.Fatalf("五声道能量不守恒: %.8f", rep.EnergyRatio)
	}
	if _, _, err := m.DownmixMatrix(mat, DefaultRhoLimit); err != nil {
		t.Fatal(err)
	}
	for j := range mat.Left {
		if mat.Left[j] <= 0 || mat.Right[j] <= 0 {
			t.Fatalf("第 %d 声道出现非正系数", j+1)
		}
	}
	t.Log("\n" + rep.String())
}

// TestDownmixErrors 三类输入报错，并指名对应声道。
func TestDownmixErrors(t *testing.T) {
	// 1. 声道数为零
	if _, err := NewMultiClip(); !errors.Is(err, ErrNoChannels) {
		t.Fatalf("声道数为零应报 ErrNoChannels，得到 %v", err)
	}
	var empty MultiClip
	if _, err := empty.DownmixSum(); !errors.Is(err, ErrNoChannels) {
		t.Fatalf("直接相加接口声道数为零应报错，得到 %v", err)
	}
	if _, _, err := empty.SolveEnergyMatrix(0.9); !errors.Is(err, ErrNoChannels) {
		t.Fatalf("求解接口声道数为零应报错，得到 %v", err)
	}

	// 2. 采样率不一致：错误必须指名冲突声道
	m := multi(t,
		sineClip(8000, 256, 440, 1000, 0),
		sineClip(44100, 256, 440, 1000, 0),
		sineClip(8000, 256, 440, 1000, 0),
	)
	_, err := m.DownmixSum()
	if !errors.Is(err, ErrRateMismatch) || !strings.Contains(err.Error(), "第 1 声道") || !strings.Contains(err.Error(), "第 2 声道") {
		t.Fatalf("采样率不一致须指名声道，得到 %v", err)
	}
	if _, _, err := m.SolveEnergyMatrix(0.9); !errors.Is(err, ErrRateMismatch) {
		t.Fatalf("求解接口应报采样率不一致，得到 %v", err)
	}
	good := multi(t, sineClip(8000, 256, 440, 1000, 0))
	if _, _, err := good.DownmixMatrix(Matrix{Left: []float64{0.5}, Right: []float64{0.5}}, 1.0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.DownmixMatrix(Matrix{Left: []float64{0.5, 0.5}, Right: []float64{0.5, 0.5}}, 0.9); !errors.Is(err, ErrRateMismatch) {
		t.Fatalf("矩阵接口应报采样率不一致，得到 %v", err)
	}

	// 3. 系数全负：错误必须指名声道范围
	neg := Matrix{Left: []float64{-0.6, -0.5}, Right: []float64{-0.8, -0.7}}
	err = ValidateMatrix(neg, 2)
	if !errors.Is(err, ErrAllNegative) || !strings.Contains(err.Error(), "第 1 至 2 声道") {
		t.Fatalf("系数全负须指名声道，得到 %v", err)
	}
	if _, _, err := good.DownmixMatrix(Matrix{Left: []float64{-0.1}, Right: []float64{-0.2}}, 0.9); !errors.Is(err, ErrAllNegative) {
		t.Fatalf("单声道全负系数应报错，得到 %v", err)
	}
	// 混合符号不允许误报
	if err := ValidateMatrix(Matrix{Left: []float64{-0.6, 0.5}, Right: []float64{0.8, -0.7}}, 2); err != nil {
		t.Fatalf("混合符号不应判为全负: %v", err)
	}
}

// TestVerifyCorrelationIndependent 相关系数核验独立工作：
// 构造左右完全同相的输出，用严格区间令其单独失败。
func TestVerifyCorrelationIndependent(t *testing.T) {
	n := 256
	l, r := make([]float64, n), make([]float64, n)
	for i := 0; i < n; i++ {
		l[i] = math.Sin(2 * math.Pi * float64(i) / 32)
		r[i] = 0.7 * l[i]
	}
	if err := VerifyCorrelation(l, r, -1.0, 1.0); err != nil {
		t.Fatalf("相关系数 ≈1 但区间含 1 时应通过: %v", err)
	}
	err := VerifyCorrelation(l, r, -0.5, 0.5)
	if !errors.Is(err, ErrCorrelation) || !strings.Contains(err.Error(), "相关系数") {
		t.Fatalf("窄区间应独立触发相关系数核验错误，得到 %v", err)
	}
}

// TestVerifyNoCancellationIndependent 抵消核验独立工作：
// 手工构造把某声道清零的矩阵，必须单独失败并指名声道。
func TestVerifyNoCancellationIndependent(t *testing.T) {
	m := multi(t,
		sineClip(8000, 256, 440, 4000, 0),
		sineClip(8000, 256, 660, 4000, 0),
	)
	// 第 2 声道整列为零 → 单独发声时被完全抵消
	kill := Matrix{Left: []float64{0.7, 0}, Right: []float64{0.7, 0}}
	err := VerifyNoCancellation(kill, m)
	if !errors.Is(err, ErrCancellation) || !strings.Contains(err.Error(), "第 2 声道") {
		t.Fatalf("抵消核验须指名第 2 声道，得到 %v", err)
	}
	if _, _, err := m.DownmixMatrix(kill, DefaultRhoLimit); !errors.Is(err, ErrCancellation) {
		t.Fatalf("矩阵下混入口必须执行抵消核验，得到 %v", err)
	}
	// 正常单位列通过
	ok := Matrix{Left: []float64{0.7071, 0.7071}, Right: []float64{0.7071, 0.7071}}
	if err := VerifyNoCancellation(ok, m); err != nil {
		t.Fatal(err)
	}
}

// TestDownmixMatrixShapeMismatch 尺寸不匹配的矩阵应被拒绝。
func TestDownmixMatrixShapeMismatch(t *testing.T) {
	m := multi(t,
		sineClip(8000, 128, 440, 1000, 0),
		sineClip(8000, 128, 660, 1000, 0),
	)
	if _, _, err := m.DownmixMatrix(Matrix{Left: []float64{0.5}, Right: []float64{0.5, 0.5}}, 0.9); err == nil {
		t.Fatal("矩阵尺寸不匹配应报错")
	}
}
