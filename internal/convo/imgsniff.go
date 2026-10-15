package convo

import "github.com/tzhnahida/VocabLock/internal/ai"

// ⚠️ 魔数表**只有一份**, 在 ai 包里 (internal/ai/magic.go) ——
// convo 这里只是转发。两份表迟早会分叉, 而分叉的表现是
// "下载时认得出、发送时不认", 极难查。

// SniffImage 按**文件头**判格式。
func SniffImage(head []byte) (mime, ext string, ok bool) {
	return ai.SniffImage(head)
}

// SniffImageFile 读文件头判格式。ok=false = 这不是一张能用的图。
func SniffImageFile(path string) (mime, ext string, ok bool) {
	m := ai.MimeOfFile(path)
	if m == "" {
		return "", "", false
	}
	return m, "", true
}
