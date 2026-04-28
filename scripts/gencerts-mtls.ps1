# 为本机三节点 mTLS 演示生成一套 CA + 服务端证书 + 客户端证书（OpenSSL）。
# 服务端 SAN：127.0.0.1、localhost，三进程可共用同一 server 证书（仅本机 demo）。
# 用法：在仓库根目录执行 .\scripts\gencerts-mtls.ps1

$ErrorActionPreference = "Stop"
$repo = Resolve-Path (Join-Path $PSScriptRoot "..")
$out = Join-Path $repo "certs\mtls-demo"
New-Item -ItemType Directory -Force -Path $out | Out-Null

if (-not (Get-Command openssl -ErrorAction SilentlyContinue)) {
    Write-Error "未找到 openssl，请先安装（例如 Git for Windows 自带，或 choco install openssl）。"
}

Push-Location $out
try {
    if ((Test-Path "ca.pem") -and (Test-Path "server-cert.pem") -and (Test-Path "client-cert.pem")) {
        Write-Host "已存在 ca.pem / server-cert.pem / client-cert.pem，跳过生成。若需重建请先删除 certs\mtls-demo\ 下文件。"
        exit 0
    }

    Write-Host "生成 CA..."
    openssl genrsa -out ca-key.pem 4096
    openssl req -new -x509 -days 825 -key ca-key.pem -out ca.pem -subj "/CN=geecache-mtls-demo-ca/O=geecache"

    @"
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=IP:127.0.0.1,DNS:localhost
"@ | Set-Content -Path server-ext.cnf -Encoding ascii

    Write-Host "生成服务端证书..."
    openssl genrsa -out server-key.pem 2048
    openssl req -new -key server-key.pem -out server.csr -subj "/CN=127.0.0.1/O=geecache"
    openssl x509 -req -days 825 -in server.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial `
        -out server-cert.pem -extfile server-ext.cnf

    @"
basicConstraints=CA:FALSE
keyUsage=digitalSignature,keyEncipherment
extendedKeyUsage=clientAuth
"@ | Set-Content -Path client-ext.cnf -Encoding ascii

    Write-Host "生成客户端证书（节点互相访问时出示）..."
    openssl genrsa -out client-key.pem 2048
    openssl req -new -key client-key.pem -out client.csr -subj "/CN=geecache-peer-client/O=geecache"
    openssl x509 -req -days 825 -in client.csr -CA ca.pem -CAkey ca-key.pem -CAcreateserial `
        -out client-cert.pem -extfile client-ext.cnf

    Remove-Item -ErrorAction SilentlyContinue server.csr, client.csr, *.srl, server-ext.cnf, client-ext.cnf
    Write-Host "完成：$out"
}
finally {
    Pop-Location
}
