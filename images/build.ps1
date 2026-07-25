param(
    [string]$Version = "0.1.0"
)

# 任一镜像构建失败时立即停止，避免发布一套版本不一致的专项镜像。
$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $PSScriptRoot

# 基础镜像包含 Pi、通用工具、公共规则和完整跨方向资料库。
docker build --file "$PSScriptRoot/base/Dockerfile" --tag "ctf-agent-pi-base:$Version" $ProjectRoot

# 六个专项镜像共享同一版本标签，依次安装各方向工具与入口 Skill。
$Profiles = @("web", "crypto", "pwn", "reverse", "forensics", "misc")
foreach ($Profile in $Profiles) {
    docker build --file "$PSScriptRoot/$Profile/Dockerfile" --tag "ctf-agent-pi-$Profile`:$Version" $ProjectRoot
}
