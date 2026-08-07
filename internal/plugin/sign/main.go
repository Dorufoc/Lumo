// Command sign 是插件签名工具（开发用）：读取 manifest 文件，用内嵌开发私钥
// 签名，输出十六进制签名供 PluginInstall 的 signature 字段使用。
//
// 用法：
//
//	go run ./internal/plugin/sign -manifest path/to/manifest.json
//	go run ./internal/plugin/sign -manifest path/to/manifest.json -out sig.txt
//
// 私钥仅供测试产物签名使用；本命令是独立工具，绝不参与应用运行路径。
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
)

// devPrivateKeyHex 与 internal/plugin/keys.go 的 DevPublicKeyHex 配对。
const devPrivateKeyHex = "d2f4a2fd81be3babf3af371a39b641c22036bf102839a719d9eeb1910ac0ec43f0fcac376d688aa1dbb9154ea2fc91cf57d7614819dfb9c65be33647e36328a6"

func main() {
	manifestPath := flag.String("manifest", "", "manifest 文件路径（必填）")
	outPath := flag.String("out", "", "签名输出文件路径（可选，缺省打印到 stdout）")
	flag.Parse()

	if *manifestPath == "" {
		fmt.Fprintln(os.Stderr, "错误：-manifest 必填")
		flag.Usage()
		os.Exit(2)
	}
	raw, err := os.ReadFile(*manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "读取 manifest 失败: %v\n", err)
		os.Exit(1)
	}
	key, err := hex.DecodeString(devPrivateKeyHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "私钥解码失败: %v\n", err)
		os.Exit(1)
	}
	sig := hex.EncodeToString(ed25519.Sign(ed25519.PrivateKey(key), raw))
	if *outPath != "" {
		if err := os.WriteFile(*outPath, []byte(sig+"\n"), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "写入签名失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("签名已写入 %s\n", *outPath)
		return
	}
	fmt.Println(sig)
}
