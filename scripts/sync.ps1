param(
 [Parameter(Mandatory=$true)][string]$Username,
 [Parameter(Mandatory=$true)][string]$Root,
 [string]$Server='http://127.0.0.1:38125',
 [string]$State='',
 [ValidateSet('upload','download','status','archive','restore','restore-all')][string]$Mode='upload',
 [switch]$InitDisk,
 [switch]$AdoptDisk,
 [switch]$RestoreToOtherServer,
 [string]$DiskLabel='',
 [string]$FileId='',
 [string]$Revision='',
 [long]$ReserveBytes=16777216,
 [string]$RemoteName='',
 [string]$RemoteParent='',
 [int]$Concurrency=2,
 [string]$Interval='',
 [switch]$Once,
 [switch]$RetryFailed
)
$ErrorActionPreference='Stop'
$taskProject = Split-Path -Parent $PSScriptRoot
$taskBinary = Join-Path $taskProject 'bin/netdisk-sync.exe'
if (-not (Test-Path -LiteralPath $taskBinary -PathType Leaf)) {
 throw 'Build bin/netdisk-sync.exe first; see docs/automatic-backup.md.'
}
# Resolve caller-relative paths before switching into the project.
$taskSource = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($Root)
if (-not $State) { $State = if ($Mode -eq 'upload') { '.tmp/netdisk-sync/state.json' } else { '.tmp/netdisk-pull/binding.json' } }
if (-not $Interval) { $Interval = if ($Mode -eq 'upload') { '10s' } else { '1m' } }
$taskState = if ([IO.Path]::IsPathRooted($State)) { $State } else { Join-Path $taskProject $State }
$taskArgs = @('-mode',$Mode,'-server',$Server,'-username',$Username,'-root',$taskSource,'-state',$taskState,'-concurrency',"$Concurrency",'-interval',$Interval,'-reserve-bytes',"$ReserveBytes")
if ($InitDisk) { $taskArgs += '-init-disk' }
if ($AdoptDisk) { $taskArgs += '-adopt-disk' }
if ($RestoreToOtherServer) { $taskArgs += '-restore-to-other-server' }
if ($DiskLabel) { $taskArgs += @('-disk-label',$DiskLabel) }
if ($FileId) { $taskArgs += @('-file-id',$FileId) }
if ($Revision) { $taskArgs += @('-revision',$Revision) }
if ($RemoteName) { $taskArgs += @('-remote-name',$RemoteName) }
if ($RemoteParent) { $taskArgs += @('-remote-parent',$RemoteParent) }
if ($Once) { $taskArgs += '-once' }
if ($RetryFailed) { $taskArgs += '-retry-failed' }
$taskSecure = Read-Host 'NetDisk password (memory only)' -AsSecureString
$taskPtr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($taskSecure)
$taskPreviousPassword = $env:NETDISK_SYNC_PASSWORD
try {
 $env:NETDISK_SYNC_PASSWORD = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($taskPtr)
 [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($taskPtr)
 $taskPtr = [IntPtr]::Zero
 & $taskBinary @taskArgs
 if ($LASTEXITCODE -ne 0) { throw "Backup exited with status $LASTEXITCODE." }
} finally {
 if ($taskPtr -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($taskPtr) }
 $env:NETDISK_SYNC_PASSWORD = $taskPreviousPassword
 $taskSecure.Dispose()
}
