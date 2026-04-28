# 本机三节点 + 节点 mTLS（不使用 etcd，静态 https peers）。
# 明文端口：9001/9002/9003（/healthz、/metrics；无 /geecache）
# mTLS 端口：9441/9442/9443（/geecache + gRPC）
#
# 用法（仓库根目录）：
#   .\scripts\gencerts-mtls.ps1
#   .\scripts\start-three-mtls.ps1
#
# 验证示例：
#   curl.exe http://localhost:9001/healthz
#   curl.exe -k https://127.0.0.1:9441/geecache/scores/Tom --cert certs/mtls-demo/client-cert.pem --key certs/mtls-demo/client-key.pem --cacert certs/mtls-demo/ca.pem

$ErrorActionPreference = "Stop"
$repo = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $repo

$certDir = Join-Path $repo "certs\mtls-demo"
$ca = Join-Path $certDir "ca.pem"
if (-not (Test-Path $ca)) {
    Write-Host "未找到证书，正在运行 gencerts-mtls.ps1 ..."
    & (Join-Path $PSScriptRoot "gencerts-mtls.ps1")
}

$peers = "https://127.0.0.1:9441,https://127.0.0.1:9442,https://127.0.0.1:9443"
$cert = Join-Path $certDir "server-cert.pem"
$key = Join-Path $certDir "server-key.pem"
$clientCA = $ca
$serverCA = $ca
$clientCert = Join-Path $certDir "client-cert.pem"
$clientKey = Join-Path $certDir "client-key.pem"

foreach ($p in @(9001, 9002, 9003)) {
    $dataPort = Join-Path $repo "data\$p"
    $backing = Join-Path $dataPort "backing"
    New-Item -ItemType Directory -Force -Path $backing | Out-Null
    foreach ($name in "Tom", "Jack", "Sam") {
        $scores = @{ Tom = "630"; Jack = "589"; Sam = "567" }
        [System.IO.File]::WriteAllText((Join-Path $backing $name), $scores[$name])
    }
}

$tlsPorts = @{ 9001 = 9441; 9002 = 9442; 9003 = 9443 }
foreach ($p in 9001, 9002, 9003) {
    $tls = $tlsPorts[$p]
    $base = "https://127.0.0.1:$tls"
    $cmd = @"
Set-Location '$repo'
go run . ``
  -port=$p ``
  -use-etcd=false ``
  -use-grpc=true ``
  -peers=$peers ``
  -peer-mtls-listen=:$tls ``
  -peer-base-url=$base ``
  -peer-mtls-cert='$cert' ``
  -peer-mtls-key='$key' ``
  -peer-mtls-client-ca='$clientCA' ``
  -peer-mtls-server-ca='$serverCA' ``
  -peer-mtls-client-cert='$clientCert' ``
  -peer-mtls-client-key='$clientKey' ``
  -persist-path=data/$p/cache.bolt
"@
    Start-Process -FilePath "powershell.exe" -WorkingDirectory $repo -ArgumentList @("-NoExit", "-Command", $cmd)
}

Write-Host "已启动 3 个窗口：明文 9001-9003，节点 mTLS 9441-9443。"
Write-Host "GET 缓存（须 mTLS 口 + 客户端证书）："
Write-Host "  curl.exe -k https://127.0.0.1:9441/geecache/scores/Tom --cert certs/mtls-demo/client-cert.pem --key certs/mtls-demo/client-key.pem --cacert certs/mtls-demo/ca.pem"
