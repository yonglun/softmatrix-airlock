package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 未经 ldflags 注入时必须是 "dev"，而不是空字符串——
// 空字符串在日志里会显示成 version=，让人以为是个 bug。
func TestVersionDefaultsToDev(t *testing.T) {
	require.Equal(t, "dev", Version)
}
