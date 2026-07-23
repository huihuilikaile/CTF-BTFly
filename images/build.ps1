param(
    [string]$Version = "0.1.0"
)

$ErrorActionPreference = "Stop"
$ProjectRoot = Split-Path -Parent $PSScriptRoot

docker build --file "$PSScriptRoot/base/Dockerfile" --tag "ctf-agent-pi-base:$Version" $ProjectRoot

$Profiles = @("web", "crypto", "pwn", "reverse", "forensics", "misc")
foreach ($Profile in $Profiles) {
    docker build --file "$PSScriptRoot/$Profile/Dockerfile" --tag "ctf-agent-pi-$Profile`:$Version" $ProjectRoot
}

