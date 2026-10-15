package convo

import (
	"os"
	"testing"
)

// readFileForTest 读本包里一个源文件 (给"接线扫源码"那类用例用)。
//
// ⚠️ 只在**跨文件的接线**上用它: 那种事在运行期没有可观测的差别
// (两处内联与一处共享, 行为逐字节相同), 而分叉的代价要过很久才显形。
func readFileForTest(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读不到 %s: %v", name, err)
	}
	return string(b)
}
