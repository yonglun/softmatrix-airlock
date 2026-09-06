package app

// Version 是构建时注入的版本号。
//
// 值由 cmd/airlock 在分发子命令前赋进来，最终来自
// go build -ldflags "-X main.version=...". 默认 "dev" 而不是空串：
// 空串在日志里显示成 version=，看着像 bug。
//
// 私有化交付后「你装的是哪一版」是每次支持对话的第一个问题，
// 而无外网机器上没有别的办法回答它。
var Version = "dev"
