# Installs the latest tokenstat release on Windows, from PowerShell:
#   irm https://raw.githubusercontent.com/tornikegomareli/tokenstat/main/install.ps1 | iex
# Set $env:TOKENSTAT_INSTALL_DIR to install somewhere other than
# %LOCALAPPDATA%\Programs\tokenstat.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue' # Invoke-WebRequest is much faster without the progress bar

$repo = 'tornikegomareli/tokenstat'
$dir = if ($env:TOKENSTAT_INSTALL_DIR) { $env:TOKENSTAT_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\tokenstat' }
$base = if ($env:TOKENSTAT_BASE_URL) { $env:TOKENSTAT_BASE_URL } else { "https://github.com/$repo/releases/latest/download" }

# throw, not exit: with irm | iex, exit would close the PowerShell window.
function Fail($msg) {
	throw "tokenstat: $msg"
}

$cpu = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
switch ($cpu) {
	'AMD64' { $arch = 'amd64' }
	'ARM64' { $arch = 'arm64' }
	default { Fail "$cpu is not supported. Releases exist for x86-64 and arm64." }
}

$archive = "tokenstat_windows_$arch.zip"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ("tokenstat-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
	Write-Host "Downloading $archive"
	try {
		Invoke-WebRequest "$base/$archive" -OutFile "$tmp\$archive" -UseBasicParsing
		Invoke-WebRequest "$base/checksums.txt" -OutFile "$tmp\checksums.txt" -UseBasicParsing
	} catch {
		Fail "could not download $base/$archive"
	}

	$want = ''
	foreach ($line in Get-Content "$tmp\checksums.txt") {
		$f = $line -split '\s+'
		if ($f.Count -ge 2 -and $f[1] -eq $archive) { $want = $f[0].ToLower() }
	}
	$got = (Get-FileHash "$tmp\$archive" -Algorithm SHA256).Hash.ToLower()
	if (-not $want -or $want -ne $got) { Fail "the checksum of $archive does not match. Nothing was installed." }

	Expand-Archive -Path "$tmp\$archive" -DestinationPath "$tmp\out" -Force
	New-Item -ItemType Directory -Path $dir -Force | Out-Null
	Copy-Item "$tmp\out\tokenstat.exe" "$dir\tokenstat.exe" -Force
	Write-Host "Installed tokenstat to $dir\tokenstat.exe"

	# Now that the new one is in place, remove other copies on the PATH:
	# one that comes first would run instead.
	$target = [IO.Path]::GetFullPath("$dir\tokenstat.exe")
	foreach ($other in Get-Command tokenstat -All -CommandType Application -ErrorAction SilentlyContinue) {
		$path = [IO.Path]::GetFullPath($other.Source)
		if ($path -eq $target) { continue }
		try {
			Remove-Item $path -Force
			Write-Host "Removed another tokenstat: $path"
		} catch {
			Write-Warning "Could not remove another tokenstat: $path. It may run instead of this one. Remove it from an administrator PowerShell."
		}
	}
} finally {
	Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (-not $userPath) { $userPath = '' }
if (($userPath -split ';') -notcontains $dir) {
	[Environment]::SetEnvironmentVariable('Path', (($userPath.TrimEnd(';') + ";$dir").TrimStart(';')), 'User')
	$env:Path = "$env:Path;$dir"
	Write-Host "Added $dir to your PATH. Open a new terminal if tokenstat is not found."
}

Write-Host "Run: tokenstat"
