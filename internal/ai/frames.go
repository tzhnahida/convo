package ai

import (
	"image"
	"image/draw"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

// ─────────────────────────────────────────────────────────────
// 动图: **抽多帧, 分别作为多个 image_url 传入**
//
// ⚠️ 这是被官方最佳实践纠正过的一版。
//
// 来源: karminski《DeepSeek V4 Flash Vision 视频输入最佳实践》(2026-08-22)
//   「虽然模型支持 gif 输入, 但是**它只识别 gif 的第一帧**(我写代码验证了)。
//     所以把视频转换为 gif 是行不通的。
//     抽帧的最佳实践: 把视频抽成 JPEG/PNG, **按时间顺序作为多个 image_url 传入**,
//     并在 prompt 里写明采样率和时间轴。这样做模型识别很准确。」
//
// ⚠️ 我第一版做的是「首帧+末帧**横向拼成一张图**」—— 那是错的:
//   · 模型会把拼图当成**左右对比梗图** (实测: 它就是这么读的);
//   · 拼起来等于把分辨率摊薄一半;
//   · "左右" 这个空间关系会盖过"先后"这个时间关系。
//   **多帧必须分开传** —— 时间轴由顺序表达, 不由像素布局表达。
//
// ⚠️ 另一条来自同一篇: **不要匀速抽全片**, 用
//   「宏观低帧率全图 + 事件窗口高帧率中心放大」两级采样。
//   表情包只有几秒、画面也小, 不适用两级; 但"别匀速"这条适用 ——
//   所以端点帧必取 (变化往往发生在头尾)。
// ─────────────────────────────────────────────────────────────

// FrameCount 动图帧数。1 = 不是动图 (或解码失败)。
func FrameCount(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil {
		return 0
	}
	return len(g.Image)
}

// MaxAnimFrames 一张动图最多抽几帧。
//
// ⚠️ 4 帧: 表情包的变化通常只有一个动作 (点头/摇头/变大),
// 4 帧足够看出"从什么变成什么"。再多是重复付费 —— 每一帧都是一张图的 token。
const MaxAnimFrames = 4

// ExtractFrames 从动图抽帧, 按**时间顺序**写成 PNG, 返回路径列表。
//
// 抽法: 端点必取 (变化常在头尾), 中间均匀。帧数不足时按实际数量返回。
// 单帧图 / 非动图 → (nil, false), 调用方照原样走单图路径。
func ExtractFrames(src string, max int) ([]string, bool) {
	if max <= 0 {
		max = MaxAnimFrames
	}
	f, err := os.Open(src)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	g, err := gif.DecodeAll(f)
	if err != nil || len(g.Image) < 2 {
		return nil, false
	}
	n := len(g.Image)
	// ⚠️ 首尾必取。中间按等分补齐。
	idx := []int{0}
	if max > 2 {
		for k := 1; k < max-1; k++ {
			idx = append(idx, k*(n-1)/(max-1))
		}
	}
	if max > 1 && n > 1 {
		idx = append(idx, n-1)
	}
	// 去重并保持顺序 (n 很小时等分会撞车)。
	seen := map[int]bool{}
	var uniq []int
	for _, i := range idx {
		if i < 0 || i >= n || seen[i] {
			continue
		}
		seen[i] = true
		uniq = append(uniq, i)
	}
	// ⚠️ **抽出来的帧不许写在源文件旁边** ——
	// 源往往是 data/convo/stickers/<lang>/ 里的表情包,
	// 在它旁边生成 x.gif.f0.png 会让表情库多出一堆看起来像表情的 PNG
	// (而且会被下一轮的"库里有几张"数进去)。
	// 单独一个临时目录, 用完即删 (见 CleanupFrames)。
	dir := filepath.Join(os.TempDir(), "convo-frames")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false
	}
	base := filepath.Join(dir, filepath.Base(strings.TrimSuffix(src, filepathExt(src))))
	out := make([]string, 0, len(uniq))
	for _, i := range uniq {
		im := g.Image[i]
		r := im.Bounds()
		dst := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		draw.Draw(dst, dst.Bounds(), im, r.Min, draw.Src)
		p := base + ".f" + itoa(i) + ".png"
		of, err := os.Create(p)
		if err != nil {
			continue
		}
		if err := png.Encode(of, dst); err != nil {
			of.Close()
			continue
		}
		of.Close()
		out = append(out, p)
	}
	if len(out) < 2 {
		return nil, false
	}
	return out, true
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// CleanupFrames 删掉 ExtractFrames 生成的帧。
//
// ⚠️ 必须显式调用: 帧是**中间产物**, 留着只会堆满临时目录。
// 调用方 (visionChat 的使用者) 在拿到结果后立刻清。
func CleanupFrames(paths []string) {
	for _, p := range paths {
		// 只删我们自己生成的 —— 别把用户的原图删了。
		if strings.Contains(p, "convo-frames") && strings.HasSuffix(p, ".png") {
			_ = os.Remove(p)
		}
	}
}
