# 启动三个本机节点：9001 / 9002 / 9003
# 持久化：data/<port>/cache.bolt；回源：data/<port>/backing/<key> 文件
# 用法：在仓库根目录执行 .\scripts\start-three.ps1

$ErrorActionPreference = "Stop"
$repo = Resolve-Path (Join-Path $PSScriptRoot "..")
Set-Location $repo

# 仅当 go run 使用 -use-etcd=false 时生效；默认 true 时节点列表来自 etcd，与 main 一致可省略。
$peers = "http://localhost:9001,http://localhost:9002,http://localhost:9003"

foreach ($p in 9001, 9002, 9003) {
    $dataPort = Join-Path $repo "data\$p"
    $backing = Join-Path $dataPort "backing"
    New-Item -ItemType Directory -Force -Path $backing | Out-Null

    # 示例回源文件（与原先内存 map 一致）；可自行增删改
    [System.IO.File]::WriteAllText((Join-Path $backing "Tom"), "630")
    [System.IO.File]::WriteAllText((Join-Path $backing "Jack"), "589")
    [System.IO.File]::WriteAllText((Join-Path $backing "Sam"), "567")
}

foreach ($p in 9001, 9002, 9003) {
    $args = @(
        "-NoExit",
        "-Command",
        "go run . -port=$p -use-etcd=true -peers=$peers -persist-path=data/$p/cache.bolt"
    )
    Start-Process -FilePath "powershell.exe" -WorkingDirectory $repo -ArgumentList $args
}

Write-Host "Started 3 windows: ports 9001-9003. Example GET: curl http://localhost:9001/geecache/scores/Tom"
