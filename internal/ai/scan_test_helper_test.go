package ai

import "fmt"

func fmtSscan(s string, n *int) (int, error) { return fmt.Sscanf(string([]byte(s)), "%d", n) }
