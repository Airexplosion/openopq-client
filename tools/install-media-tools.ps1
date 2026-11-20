param([Parameter(Mandatory=$true)][string]$Destination)
$ErrorActionPreference = 'Stop'
if (-not [Environment]::Is64BitOperatingSystem -or $env:PROCESSOR_ARCHITECTURE -eq 'ARM64') {
  throw 'This installer supplies Windows x64 tools. Other platforms: install silk v0.3.0 and an FFmpeg build with the documented audio encoders.'
}
$target = [IO.Path]::GetFullPath($Destination)
New-Item -ItemType Directory -Force -Path $target | Out-Null
$cache = Join-Path $target 'downloads'
New-Item -ItemType Directory -Force -Path $cache | Out-Null
$packages = @(
  @{ Name='silk-v0.3.0-x86_64-pc-windows-msvc.zip'; Url='https://github.com/ljh-sh/silk/releases/download/v0.3.0/silk-v0.3.0-x86_64-pc-windows-msvc.zip'; Hash='d25c266d9f34675ea8aa3ec78e640a9f455b4db94d95ab0ca7a6d1ff6e614368'; Exe='silk.exe' },
  @{ Name='ffmpeg-9.0.2-full_build.zip'; Url='https://github.com/GyanD/codexffmpeg/releases/download/9.0.2/ffmpeg-9.0.2-full_build.zip'; Hash='759d0a9831c436a0eb331ad36f236c06cb04aaa0005da46571f0e9d3d9206f6b'; Exe='ffmpeg.exe' }
)
foreach ($package in $packages) {
  $archive = Join-Path $cache $package.Name
  if (-not (Test-Path -LiteralPath $archive)) { Invoke-WebRequest -Uri $package.Url -OutFile $archive }
  if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash.ToLowerInvariant() -ne $package.Hash) {
    throw "SHA256 mismatch: $($package.Name). No executable was installed from this archive."
  }
  $unpacked = Join-Path $cache ($package.Name + '.unpacked')
  Expand-Archive -LiteralPath $archive -DestinationPath $unpacked -Force
  $files = @(Get-ChildItem -LiteralPath $unpacked -Recurse -File -Filter $package.Exe)
  if ($files.Count -ne 1) { throw "Expected exactly one $($package.Exe)" }
  Copy-Item -LiteralPath $files[0].FullName -Destination (Join-Path $target $package.Exe) -Force
}
@'
Media tools are started on demand, with a concurrency limit of two.
silk v0.3.0 (MIT): https://github.com/ljh-sh/silk/tree/v0.3.0
SILK codec upstream license: https://github.com/ljh-sh/silk/tree/v0.3.0
FFmpeg 9.0.2 full build (GPLv3): https://www.gyan.dev/ffmpeg/builds/
Build sources and configuration: https://github.com/GyanD/codexffmpeg
FFmpeg source: https://ffmpeg.org/releases/ffmpeg-9.0.2.tar.xz
Original downloaded archives, including available license files, are retained in downloads/.
Third-party tools retain their own license terms; see the upstream links above.
'@ | Set-Content -LiteralPath (Join-Path $target 'MEDIA-TOOLS.txt') -Encoding utf8
& (Join-Path $target 'silk.exe') --version
& (Join-Path $target 'ffmpeg.exe') -hide_banner -version | Select-Object -First 1
Write-Host "Installed verified media tools: $target"
