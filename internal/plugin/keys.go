package plugin

// DevPublicKeyHex 是 Lumo 开发用 Ed25519 公钥（编译期常量，十六进制）。
//
// 公钥即本常量：任何已安装插件包的签名都必须能通过该公钥验证，否则拒绝安装。
// 对应的私钥仅供测试产物签名使用——绝不出现在本包生产代码路径：
//   - 单元测试使用 internal/plugin/sign_test.go 中的 SignForTest；
//   - 手工/QA 签名使用 internal/plugin/sign 命令（go run ./internal/plugin/sign）。
const DevPublicKeyHex = "f0fcac376d688aa1dbb9154ea2fc91cf57d7614819dfb9c65be33647e36328a6"
