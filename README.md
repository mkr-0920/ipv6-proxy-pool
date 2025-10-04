# IPv6 Proxy Pool

一个基于 Go 语言实现的 SOCKS5 代理池工具。它能够自动扫描并利用服务器上的 `/64` IPv6 地址块，生成大量随机 IPv6 地址作为出口 IP，构建一个动态的 IPv6 代理池。

## 🚀 功能特性

- **SOCKS5 代理**：提供标准 SOCKS5 代理服务，兼容各类客户端。
- **动态 IP 池**：自动扫描网卡上的 `/64` IPv6 地址，并以此为基础生成海量随机 IPv6 地址。
- **地址管理**：启动时可选择清理掉非 `/64` 网段的旧地址，保持 IP 池的纯净。
- **轮询出口**：每次代理请求会从 IP 池中轮询选择一个 IPv6 地址作为出口，实现IP的自动切换。
- **跨平台运行**：支持在 Linux 和 Windows 系统上编译和运行。
- **简单配置**：通过简单的 `config.ini` 文件即可完成所有配置。

## 🛠️ 环境准备

你需要安装 Go 语言环境 (版本 >= 1.18)。

- [Go 官方下载地址](https://golang.org/dl/)

## ⚙️ 配置说明

在运行程序前，请先在项目根目录创建并编辑 `config.ini` 文件。

```ini
[default]
# 你服务器上拥有 /64 IPv6 地址块的网卡名称
Networkname = eth0
# SOCKS5 代理监听的端口
port = 1080
```

**如何找到 `Networkname`?**

-   **在 Linux 上**:
    运行 `ip a` 或 `ifconfig` 命令，找到那个分配了公网 IPv6 地址的网卡名称（通常是 `eth0`, `ens...` 等）。
-   **在 Windows 上**:
    运行 `ipconfig` 命令，找到对应的网络适配器名称（例如 "以太网"）。

## 📦 编译与运行

#### 1. 克隆项目
```bash
git clone https://github.com/mkr-0920/ipv6-proxy-pool.git
cd ipv6-proxy-pool
```

#### 2. 编译
在项目根目录下执行以下命令，将会生成一个名为 `ipv6-proxy-pool` (或 `ipv6-proxy-pool.exe`) 的可执行文件。
```bash
go build -o ipv6-proxy-pool
```

#### 3. 运行
> **重要提示**: 由于本程序需要添加和删除网络接口的 IP 地址，因此必须使用管理员或 root 权限运行。

- **在 Linux 上**:
  ```bash
  sudo ./ipv6-proxy-pool
  ```
- **在 Windows 上**:
  以**管理员身份**打开 PowerShell 或 CMD，然后运行：
  ```powershell
  .\ipv6-proxy-pool.exe
  ```

程序启动后，会根据你的选择执行清理和添加 IP 地址的操作，最后启动 SOCKS5 代理服务。

## 💡 如何使用

你可以将 SOCKS5 代理地址 `127.0.0.1:1080` (端口取决于你的配置) 配置在你的应用程序或浏览器中。

以下是使用 `curl` 测试代理的示例：

```bash
# 通过代理请求 ipify，它会返回你当前的出口 IPv6 地址
curl --socks5 127.0.0.1:1080 https://api64.ipify.org

# 多次执行上面的命令，你会发现返回的 IP 地址在不断变化
```
