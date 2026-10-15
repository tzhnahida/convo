import io
p = "internal/convo/nightwrite_test.go"
s = io.open(p, encoding="utf8").read()
old = "	e, _, at := nightEngine(t)"
new = (	"// ⚠️ **用真实时钟当批的时刻** —— `sleepEffect()` 读的是 `clock.Now()`，" + chr(10) +
       	"// 而两边的归属日 (NightKeyOf) 必须落在同一天，否则这条用例测的是夹具。" + chr(10) +
       	"// (第一版用 nightEngine 的 20:00，而跑测试时是凌晨 ⇒ 归属日差一天 ⇒ 假红一次。)" + chr(10) +
       	"e, _ := groupSpeakEngine(t, &sitCaptureChatter{})" + chr(10) +
       	"at := clock.Now()")
assert old in s, "MISS"
s = s.replace(old, new)
if "internal/clock" not in s:
    s = s.replace('import "testing"', 'import (' + chr(10) + T + '"testing"' + chr(10) + chr(10) + T + '"github.com/tzhnahida/VocabLock/internal/clock"' + chr(10) + ')', 1)
io.open(p, "w", encoding="utf8").write(s)
print("ok")