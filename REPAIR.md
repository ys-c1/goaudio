# 削波修复（Declipping）说明

## 1. 问题模型

录音中超过 ADC 量程的样本被压成同一个削波电平，形成连续两个及以上完全
相等的“削平段”。修复把每个削平段整体当作**缺失样本**，只用段外的动态
样本（绝对值低于削波电平的样本）重建段内波形。

## 2. 修复流水线（`audio/declip.go` 的 `Declip`）

1. **检测**：`DetectFlatRuns` 找出所有连续相等且绝对值达到 `ClipLevel`
   的平台，记录起止帧。
2. **收集动态样本**：`dynamicIndices` 取削平段两侧各至多 `Window` 个
   动态样本，贴削波电平的样本一律排除；`dynamicRuns` 再切成连续段，
   保证后续统计不跨越缺口或平台。
3. **定阶**（`estimateOrder` + `levinsonCoefs`）：见第 3 节。
4. **插值**（`solveGapCentered`）：在零均值空间 y = x − μ 上，把削平段
   当未知变量，前向、后向线性预测各列一行，联合成超定最小二乘
   （AᵀA·v = Aᵀb，带 1e-9 岭项），由带选主元的高斯消元求解。前向
   `y[k]=Σaⱼy[k-j]`、后向 `y[k]=Σaⱼy[k+j]`（平稳 AR 系数对称），
   两侧已知样本把缺口夹在中间，系统满秩。
5. **动态范围投影**（`projectActiveSetCentered`）：活动集投影把解约束到
   可行区间——正向 `[ClipLevel, FullScale]`、负向 `[-FullScale, -ClipLevel]`
   （已平移到零均值空间）。越界点固定到边界后从变量集移除、重新求解，
   迭代到全部可行，最后加回 μ 并量化为 int16。
6. **两个独立验证器**（第 4 节），分别检查满幅峰值与两侧一阶差分连续性。

## 3. 预测阶数如何由残差自相关确定，为什么取这个阶

候选阶 p 从 1 到 `MaxOrder`，对每个 p 用 **Burg 格型法**估计 LP 系数，
再计算前向预测残差 e[k] = y[k] − Σaⱼy[k-j]，依据残差自相关定阶：

- **首选——残差白化检验的最小阶**：真实 AR(p) 过程在正确阶数下残差白化，
  其各滞后自相关落入白噪声带 ±1.96/√N（`residualIsWhite`）。取通过检验
  的**最小** p：更小的 p 残差仍含可预测结构（欠拟合），更大的 p 只是在
  拟合噪声（过拟合、矩阵病态）。
- **兜底——AIC 最小阶**：周期信号在有限窗内残差可能始终带弱结构、无一
  通过白化检验，此时取 AIC(p) = N·ln(σ²ₚ) + 2p 最小的阶。σ²ₚ 是残差方差
  （由残差自相关给出），随 p 增大单调下降，2p 惩罚过拟合。
- **阶数为零即拒绝**：当动态样本恒定（无任何动态、能量≈0）或有效残差
  不足时，没有可预测结构，阶数只能为零，`Declip` 返回 `ErrZeroOrder`
  并携带该削平段位置。

### 为什么用 Burg 而不是直接对自相关解 Yule-Walker

削波把信号切成多个短动态段，且动态样本系统性排除了波形顶部/底部，
普通自相关估计存在选择性偏差（实测低频正弦的一阶相关从理论 0.999 偏到
0.975），导致谐波阶系数失真、插值塌回削波电平。Burg 法直接在可用样本上
最小化前向+后向预测误差之和、逐阶求反射系数，不经有偏自相关，对短数据/
缺失数据稳健，并由构造保证反射系数 |k|≤1（系统最小相位）。多段的前/后向
误差能量跨段池化后再求反射系数，等价于在同一平稳过程的所有可用观测上
联合拟合。

## 4. 两条独立验证（分开实现、分开报错）

- **验证器 A — 满幅峰值** `VerifyPeakFloat`：先在量化前的 float 重建信号上
  逐点检查 `[-32768, 32767]`，再由 `Peak` 复核 int16 峰值；越界返回
  `ErrPeakOverflow` 并带帧位置。
- **验证器 B — 两侧一阶差分连续性（C1）** `VerifyBoundaryContinuous`：
  比较边界差分与紧邻的**动态**参考差分（跳过其它平台）。容差为
  `JumpTol + 0.25·|参考差分|`，绝对项防零附近误报、相对项适配不同斜率。
  未修复平台的人工折角是局部差分的数倍、必然越界；正确重建的平滑衔接只在
  自然变化范围内。失败返回 `ErrBoundaryJump` 并带段位置。

两者是不同函数、不同错误哨兵、不同判据，`TestValidatorsIndependent`
显式构造各自独立触发的信号验证职责分离。

## 5. 拒绝路径（均经 `RepairError` 报出发生位置）

| 条件 | 哨兵错误 | 位置 |
|---|---|---|
| 削平段长度 > `MaxFlat` | `ErrFlatLimit` | 该段起止帧 |
| 预测阶数估计为零（邻域恒定/残差不足） | `ErrZeroOrder` | 该段起止帧 |
| 采样率 ≤ 0 | `ErrSampleRate` | 全局（Index=-1） |
| 削平段贴信号首尾，缺一侧已知样本 | `ErrEdgeClip` | 该段起止帧 |
| 配置非法（如 ClipLevel > FullScale） | `ErrConfig` | 全局 |

## 6. 五类样本与断言（`audio/declip_test.go`）

| 类别 | 样本 | 关键断言 |
|---|---|---|
| 1 干净信号 | 220Hz 无削波正弦 | 零修复段、零样本、波形原样返回 |
| 2 短削平段 | 50Hz，30000 电平硬削（约 21 样本/段） | 阶数为正、残差有限、两验证器通过、RMSE 1034.9→0.50 |
| 3 长削平段 | 60Hz，22000 深削（最长 36 样本/段） | 长段仍通过两验证器、RMSE 5682.9→1998 |
| 4 多削平段 | 100Hz，120 个平台 | 修复段数=检测段数、样本总数吻合、与原始相关系数≈1.00000 |
| 5 拒绝路径 | 超长/零阶/零采样率/贴边 | 分别匹配 `ErrFlatLimit`/`ErrZeroOrder`/`ErrSampleRate`/`ErrEdgeClip` 且带位置 |

## 7. 复现步骤

```bash
# 1. 编译与静态检查
go build ./...
go vet ./...

# 2. 跑全部单元测试（含五类样本断言，-v 看逐类阶数/残差/修复样本数日志）
go test ./... -v

# 只跑某一类（命令也写在各测试函数上方注释里）
go test ./audio/ -run TestClass1Clean -v
go test ./audio/ -run TestClass2ShortFlat -v
go test ./audio/ -run TestClass3LongFlat -v
go test ./audio/ -run TestClass4Multiple -v
go test ./audio/ -run TestClass5Rejections -v
go test ./audio/ -run TestValidatorsIndependent -v

# 3. 生成 artifacts/ 下的 WAV 三件套（原始/削波/修复）、SVG 波形对照图
#    与逐类报告 repair_report.md
go run ./cmd/declipdemo
```

## 8. 与原始波形对照

`go run ./cmd/declipdemo` 在 `artifacts/` 为每个场景产出：

- `*_original.wav` / `*_clipped.wav` / `*_repaired.wav`：原始、削波、修复
  三件套，可用任意 WAV 播放器或 Audacity 叠轨对照；
- `*_compare.svg`：灰=原始、红=削波、蓝=修复三条波形叠加（前 400 帧），
  浏览器直接打开；
- `repair_report.md`：逐类的阶数、残差 RMSE、修复样本数、修复后峰值与
  相对原始波形的 RMSE。

量化结果（见 `artifacts/repair_report.md`）：短削平段修复后 RMSE 从
1034.9 降到 0.50（int16 单位），多削平段与原始波形相关系数达 1.00000；
深削波长段因缺失信息多，RMSE 仅能从 5682.9 降到 1998——削波丢失的是
真实峰值，投影保证结果物理可行但无法凭空恢复超出满幅的原始幅值。
