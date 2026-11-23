# Installs the native OpenNeko release on Windows for one user, with no
# Docker and no admin rights. -Upgrade upgrades an install with the new
# release's own executable and passes the other arguments to `openneko upgrade`.
param(
  [switch]$Upgrade,
  [Parameter(ValueFromRemainingArguments = $true)][string[]]$UpgradeArgs
)

& {
  $ErrorActionPreference = 'Stop'
  $ProgressPreference = 'SilentlyContinue'
  function Fail([string]$message) {
    [Console]::Error.WriteLine("openneko install: $message")
    throw 'openneko install failed'
  }

  if (-not $Upgrade -and $UpgradeArgs) {
    Fail "unknown option $($UpgradeArgs[0]); the options go after -Upgrade"
  }
  $machine = $env:PROCESSOR_ARCHITEW6432
  if (-not $machine) { $machine = $env:PROCESSOR_ARCHITECTURE }
  if (-not $machine) { $machine = "$([System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture)" -replace '^X64$', 'AMD64' }
  switch ($machine) {
    'AMD64' { $arch = 'amd64' }
    'ARM64' { $arch = 'arm64' }
    default { Fail "no native release for $machine" }
  }
  if (-not $env:LOCALAPPDATA) { Fail 'LOCALAPPDATA is not set' }
  $prefix = $env:OPENNEKO_PREFIX
  if (-not $prefix) { $prefix = Join-Path $env:LOCALAPPDATA 'Programs\OpenNeko' }
  $bindir = Join-Path $prefix 'bin'
  $exe = Join-Path $bindir 'openneko.exe'

  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
  $base = $env:OPENNEKO_RELEASE_URL
  if (-not $base) { $base = 'https://github.com/open-neko/openneko/releases/download' }
  $base = $base.TrimEnd('/')
  function Test-ReleaseUrl([string]$url) {
    $url -cmatch '^https://' -or $url -cmatch '^http://(127\.0\.0\.1|localhost|\[::1\])(:[0-9]+)?(/|$)'
  }
  if (-not (Test-ReleaseUrl $base)) { Fail "OPENNEKO_RELEASE_URL must be an https URL: $base" }
  function Save-ReleaseFile([string]$url, [string]$out) {
    $r = Invoke-WebRequest -UseBasicParsing -PassThru -OutFile $out $url
    $final = $r.BaseResponse.ResponseUri
    if (-not $final -and $r.BaseResponse.RequestMessage) { $final = $r.BaseResponse.RequestMessage.RequestUri }
    if (-not $final -or ("$final" -cnotmatch '^https://' -and ([Uri]"$final").Authority -cne ([Uri]$url).Authority)) { Fail "refused $final as the source of ${url}: a release downloads over https only" }
  }
  $tag = $env:OPENNEKO_VERSION
  if (-not $tag) {
    if ($env:OPENNEKO_RELEASE_URL) { Fail 'OPENNEKO_RELEASE_URL is set, so set OPENNEKO_VERSION too' }
    $tag = (Invoke-RestMethod -UseBasicParsing 'https://api.github.com/repos/open-neko/openneko/releases/latest').tag_name
  }
  if (-not $tag.StartsWith('v')) { $tag = "v$tag" }
  if ($tag -cnotmatch '^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$') { Fail "not a release tag: $tag" }

  if ($Upgrade) {
    $cut = (Test-Path -LiteralPath "$exe.new" -PathType Leaf) -or [bool](Get-ChildItem -LiteralPath $bindir -Filter 'openneko.exe.old*' -File -ErrorAction SilentlyContinue)
    if (-not (Test-Path -LiteralPath $exe -PathType Leaf) -and -not $cut) { Fail "no OpenNeko in $prefix; set OPENNEKO_PREFIX to the install folder" }
  } elseif (Test-Path -LiteralPath $exe) {
    Fail "$prefix already holds OpenNeko; run ``openneko upgrade`` or this script with -Upgrade"
  }

  $name = "openneko-native_$($tag.Substring(1))_windows_$arch.zip"
  $tmp = Join-Path ([IO.Path]::GetTempPath()) ("openneko-install-" + [Guid]::NewGuid().ToString('N'))
  New-Item -ItemType Directory -Path $tmp | Out-Null
  try {
    Write-Host "Downloading OpenNeko $tag for windows/$arch..."
    $sums = Join-Path $tmp 'checksums-native.txt'
    $zip = Join-Path $tmp $name
    Save-ReleaseFile "$base/$tag/checksums-native.txt" $sums
    Save-ReleaseFile "$base/$tag/$name" $zip
    $want = @(Get-Content -LiteralPath $sums | ForEach-Object {
        if ($_ -cmatch '^([0-9a-f]{64})  \*?(.+)$' -and $Matches[2] -ceq $name) { $Matches[1] }
      })
    if ($want.Count -eq 0) { Fail "checksums-native.txt does not list $name" }
    $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash.ToLowerInvariant()
    if ($want.Count -ne 1 -or $got -cne $want[0]) { Fail "checksum mismatch for $name" }

    $release = Join-Path $tmp 'release'
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    [IO.Compression.ZipFile]::ExtractToDirectory($zip, $release)
    $newExe = Join-Path $release 'bin\openneko.exe'
    if (-not (Test-Path -LiteralPath $newExe -PathType Leaf) -or
      -not (Test-Path -LiteralPath (Join-Path $release 'share\openneko') -PathType Container)) {
      Fail "$name holds no bin\openneko.exe or share\openneko"
    }

    if ($Upgrade) {
      Write-Host "Upgrading the OpenNeko in $prefix to $tag..."
      & $newExe upgrade --from $release --exe $exe @UpgradeArgs
      if ($LASTEXITCODE -ne 0) { Fail "openneko upgrade failed with exit code $LASTEXITCODE" }
      return
    }

    New-Item -ItemType Directory -Force -Path (Join-Path $prefix 'share') | Out-Null
    Copy-Item -Recurse -LiteralPath (Join-Path $release 'bin') -Destination $prefix
    Copy-Item -Recurse -LiteralPath (Join-Path $release 'share\openneko') -Destination (Join-Path $prefix 'share')
  } finally {
    Remove-Item -Recurse -Force -LiteralPath $tmp -ErrorAction SilentlyContinue
  }

  & $exe version --short | Out-Null
  if ($LASTEXITCODE -ne 0) { Fail 'the installed executable does not run' }
  & $exe release-check --skills --bundled-packs | Out-Null
  if ($LASTEXITCODE -ne 0) {
    Fail "the release in $prefix lacks its bundled skills or packs; remove $prefix and run this script again"
  }

  if ([Environment]::OSVersion.Platform -eq 'Win32NT') {
    $key = [Microsoft.Win32.Registry]::CurrentUser.CreateSubKey('Environment')
    try {
      $raw = "$($key.GetValue('Path', '', 'DoNotExpandEnvironmentNames'))"
      $kind = 'ExpandString'
      if ($raw) { $kind = $key.GetValueKind('Path') }
      $parts = @($raw -split ';' | Where-Object { $_ })
      $expanded = @($parts | ForEach-Object { [Environment]::ExpandEnvironmentVariables($_) })
      if ($parts -notcontains $bindir -and $expanded -notcontains $bindir) {
        $key.SetValue('Path', (($parts + $bindir) -join ';'), $kind)
        try {
          Add-Type -Namespace OpenNeko -Name Notice -MemberDefinition @'
[DllImport("user32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, UIntPtr wParam, string lParam, uint fuFlags, uint uTimeout, out UIntPtr lpdwResult);
'@
          $result = [UIntPtr]::Zero
          [void][OpenNeko.Notice]::SendMessageTimeout([IntPtr]0xffff, 0x1a, [UIntPtr]::Zero, 'Environment', 2, 5000, [ref]$result)
        } catch {}
        Write-Host "Added $bindir to your PATH. A new terminal finds openneko."
      }
    } finally {
      $key.Close()
    }
  }
  if (@($env:Path -split [IO.Path]::PathSeparator) -notcontains $bindir) {
    $env:Path = $bindir + [IO.Path]::PathSeparator + $env:Path
  }
  Write-Host "OpenNeko $tag is in $prefix; $exe runs it."
  Write-Host 'Start it with your Postgres database:'
  Write-Host "  openneko start --native -d --database-url 'postgres://USER:PASSWORD@HOST:5432/DATABASE?sslmode=verify-full'"
}
