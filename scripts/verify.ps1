# Requires PowerShell 7+. Only creates generated test users/files on this service.
param([string]$BaseUrl = 'http://127.0.0.1:38120')
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
Set-Location (Split-Path -Parent $PSScriptRoot)
if ($BaseUrl -ne 'http://127.0.0.1:38120') { throw 'Acceptance is restricted to the project loopback service.' }
$script:Checks = 0
$script:Clients = [System.Collections.Generic.List[object]]::new()
function New-ApiClient {
    $jar = [System.Net.CookieContainer]::new()
    $handler = [System.Net.Http.HttpClientHandler]::new()
    $handler.CookieContainer = $jar
    $handler.AllowAutoRedirect = $false
    $client = [System.Net.Http.HttpClient]::new($handler)
    $client.Timeout = [TimeSpan]::FromSeconds(20)
    $result = [pscustomobject]@{ Client = $client; Jar = $jar }
    $script:Clients.Add($result)
    return $result
}
function Invoke-Api {
    param($Actor, [string]$Method, [string]$Path, $Body = $null, [string]$ContentType = 'application/json', [bool]$Verified = $true, [string]$Origin = '')
    $req = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::new($Method), $BaseUrl + $Path)
    try {
        if ($Verified) { $req.Headers.Add('X-NetDisk-Request', '1') }
        if ($Origin) { $req.Headers.Add('Origin', $Origin) }
        if ($null -ne $Body) {
            if ($Body -is [byte[]]) { $req.Content = [System.Net.Http.ByteArrayContent]::new($Body) }
            else { $req.Content = [System.Net.Http.StringContent]::new([string]$Body, [System.Text.Encoding]::UTF8) }
            $req.Content.Headers.ContentType = [System.Net.Http.Headers.MediaTypeHeaderValue]::new($ContentType)
        }
        $response = $Actor.Client.SendAsync($req).GetAwaiter().GetResult()
        try {
            $bytes = $response.Content.ReadAsByteArrayAsync().GetAwaiter().GetResult()
            return [pscustomobject]@{
                Status = [int]$response.StatusCode
                Bytes = $bytes
                Text = [System.Text.Encoding]::UTF8.GetString($bytes)
                ContentType = [string]$response.Content.Headers.ContentType
                Disposition = [string]$response.Content.Headers.ContentDisposition
                NoSniff = if ($response.Headers.Contains('X-Content-Type-Options')) { $response.Headers.GetValues('X-Content-Type-Options') -join ',' } else { '' }
            }
        } finally { $response.Dispose() }
    } finally { $req.Dispose() }
}
function Assert-Status($Response, [int]$Expected, [string]$Label) {
    if ($Response.Status -ne $Expected) { throw "$Label failed: expected $Expected; got $($Response.Status). Response body intentionally omitted." }
    $script:Checks++
    Write-Output "PASS $Label ($Expected)"
}
function Assert-True([bool]$Condition, [string]$Label) {
    if (-not $Condition) { throw "$Label failed." }
    $script:Checks++
    Write-Output "PASS $Label"
}
function Hash([byte[]]$Bytes) { return [Convert]::ToHexString([System.Security.Cryptography.SHA256]::HashData($Bytes)).ToLowerInvariant() }
function Wait-Healthy($Actor) {
    for ($attempt = 0; $attempt -lt 40; $attempt++) {
        try { if ((Invoke-Api $Actor GET '/healthz').Status -eq 200) { return } } catch { }
        Start-Sleep -Milliseconds 500
    }
    throw 'Service did not become healthy.'
}

$anonymous = New-ApiClient
$a = New-ApiClient
$b = New-ApiClient
$replay = New-ApiClient
$password = [Guid]::NewGuid().ToString('N') + '!Tmp9'
$suffix = [Guid]::NewGuid().ToString('N').Substring(0, 12)
$aBody = @{ username = "test_a_$suffix"; password = $password } | ConvertTo-Json -Compress
$bBody = @{ username = "test_b_$suffix"; password = $password } | ConvertTo-Json -Compress
$createdFile = $null
try {
    Wait-Healthy $anonymous
    Assert-Status (Invoke-Api $a POST '/api/register' $aBody) 201 'register A'
    Assert-Status (Invoke-Api $b POST '/api/register' $bBody) 201 'register B'
    Assert-Status (Invoke-Api $a POST '/api/login' $aBody) 200 'login A'
    Assert-Status (Invoke-Api $b POST '/api/login' $bBody) 200 'login B'
    $wrong = @{ username = "test_a_$suffix"; password = 'incorrect-temporary-password' } | ConvertTo-Json -Compress
    Assert-Status (Invoke-Api $anonymous POST '/api/login' $wrong) 401 'wrong password rejected'
    Assert-Status (Invoke-Api $a GET '/api/me') 200 'current user'
    $cookie = $a.Jar.GetCookies([Uri]$BaseUrl)['netdisk_session']
    Assert-True ($cookie.HttpOnly -and -not $cookie.Secure) 'local HTTP session cookie usable and HttpOnly'
    Assert-True (-not (Invoke-Api $a GET '/api/me').Text.Contains($cookie.Value)) 'session token absent from JSON'

    $payload = [byte[]]::new(1048593)
    [System.Security.Cryptography.RandomNumberGenerator]::Fill($payload)
    $sourceHash = Hash $payload
    $upload = Invoke-Api $a POST '/api/files?name=acceptance-original.bin' $payload 'application/octet-stream'
    Assert-Status $upload 201 'upload binary file'
    $file = $upload.Text | ConvertFrom-Json
    $createdFile = '/api/files/' + $file.id
    Assert-True ($file.size -eq $payload.Length) 'uploaded size'
    $listResponse = Invoke-Api $a GET '/api/files'
    Assert-Status $listResponse 200 'list A files'
    $listing = $listResponse.Text | ConvertFrom-Json
    Assert-True (@($listing.files).Count -eq 1 -and $listing.files[0].download_url -eq $file.download_url -and $listing.files[0].name -eq 'acceptance-original.bin') 'list metadata and protected link'
    $bList = (Invoke-Api $b GET '/api/files').Text | ConvertFrom-Json
    Assert-True (@($bList.files).Count -eq 0) 'B list excludes A files'
    foreach ($actor in @($b, $anonymous)) {
        $expected = if ($actor -eq $b) { 404 } else { 401 }
        $label = if ($actor -eq $b) { 'B isolation' } else { 'anonymous denied' }
        Assert-Status (Invoke-Api $actor GET $createdFile) $expected "$label metadata"
        Assert-Status (Invoke-Api $actor GET $file.download_url) $expected "$label download"
        Assert-Status (Invoke-Api $actor PATCH $createdFile '{"name":"stolen.bin"}') $expected "$label rename"
        Assert-Status (Invoke-Api $actor DELETE $createdFile) $expected "$label delete"
    }
    Assert-Status (Invoke-Api $anonymous GET '/api/files') 401 'anonymous list denied'
    Assert-Status (Invoke-Api $anonymous GET '/api/me') 401 'anonymous current user denied'
    Assert-Status (Invoke-Api $anonymous POST '/api/files?name=anon.bin' ([byte[]](1,2)) 'application/octet-stream') 401 'anonymous upload denied'
    Assert-Status (Invoke-Api $a PATCH $createdFile '{"name":"csrf.bin"}' 'application/json' $false) 403 'missing CSRF header rejected'
    Assert-Status (Invoke-Api $a PATCH $createdFile '{"name":"csrf.bin"}' 'application/json' $true 'https://attacker.invalid') 403 'foreign Origin rejected'
    foreach ($name in @('../escape.bin', '..', '/etc/passwd', 'a')) {
        Assert-Status (Invoke-Api $a POST ('/api/files?name=' + [Uri]::EscapeDataString($name)) ([byte[]](1,2)) 'application/octet-stream') 400 'malicious upload name rejected'
    }
    Assert-Status (Invoke-Api $a PATCH $createdFile '{"name":"../escape.bin"}') 400 'malicious rename rejected'
    $download = Invoke-Api $a GET $file.download_url
    Assert-Status $download 200 'download original'
    Assert-True ((Hash $download.Bytes) -eq $sourceHash) 'uploaded/downloaded SHA256 equal'
    Assert-True ($download.ContentType -eq 'application/octet-stream' -and $download.Disposition.StartsWith('attachment;') -and $download.NoSniff -eq 'nosniff') 'safe download headers'
    Write-Output "SHA256 $sourceHash"
    Assert-Status (Invoke-Api $a PATCH $createdFile '{"name":"renamed.bin"}') 200 'rename'
    $renamed = (Invoke-Api $a GET $createdFile).Text | ConvertFrom-Json
    Assert-True ($renamed.name -eq 'renamed.bin') 'renamed metadata'
    Assert-True ((Hash (Invoke-Api $a GET $file.download_url).Bytes) -eq $sourceHash) 'rename preserves bytes'

    # Close the TCP write side before Content-Length bytes have been sent.
    $tcp = [System.Net.Sockets.TcpClient]::new()
    try {
        $tcp.Connect('127.0.0.1', 38120)
        $stream = $tcp.GetStream()
        $header = "POST /api/files?name=interrupted.bin HTTP/1.1`r`nHost: 127.0.0.1:38120`r`nContent-Type: application/octet-stream`r`nX-NetDisk-Request: 1`r`nCookie: netdisk_session=$($cookie.Value)`r`nContent-Length: 1000000`r`nConnection: close`r`n`r`npartial"
        $wire = [Text.Encoding]::ASCII.GetBytes($header)
        $stream.Write($wire, 0, $wire.Length)
        $tcp.Client.Shutdown([System.Net.Sockets.SocketShutdown]::Send)
        $stream.ReadTimeout = 5000
        $drain = [byte[]]::new(4096)
        while ($stream.Read($drain, 0, $drain.Length) -gt 0) { }
    } finally { $tcp.Dispose() }
    $afterAbort = (Invoke-Api $a GET '/api/files').Text | ConvertFrom-Json
    Assert-True (@($afterAbort.files).Count -eq 1) 'interrupted upload not published'

    $before = docker inspect --format '{{.State.StartedAt}}' bingyan-netdisk-app
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect project service.' }
    docker compose restart app
    if ($LASTEXITCODE -ne 0) { throw 'Project restart failed.' }
    Wait-Healthy $anonymous
    $after = docker inspect --format '{{.State.StartedAt}}' bingyan-netdisk-app
    if ($LASTEXITCODE -ne 0) { throw 'Cannot inspect restarted service.' }
    Assert-True ($before -ne $after) 'Linux container actually restarted'
    Assert-Status (Invoke-Api $a GET '/api/me') 200 'session persists through restart'
    Assert-Status (Invoke-Api $a POST '/api/login' $aBody) 200 'account persists through restart'
    $persisted = Invoke-Api $a GET $file.download_url
    Assert-Status $persisted 200 'file persists through restart'
    Assert-True ((Hash $persisted.Bytes) -eq $sourceHash) 'restart preserves SHA256'
    Assert-True (((Invoke-Api $a GET $createdFile).Text | ConvertFrom-Json).name -eq 'renamed.bin') 'rename persists through restart'
    Assert-Status (Invoke-Api $a DELETE $createdFile) 204 'delete owned file'
    Assert-Status (Invoke-Api $a GET $file.download_url) 404 'deleted download denied'
    Assert-True (@(((Invoke-Api $a GET '/api/files').Text | ConvertFrom-Json).files).Count -eq 0) 'deleted file absent from list'
    $createdFile = $null
    $savedCookie = $a.Jar.GetCookies([Uri]$BaseUrl)['netdisk_session'].Value
    $replay.Jar.Add([Uri]$BaseUrl, [System.Net.Cookie]::new('netdisk_session', $savedCookie, '/'))
    Assert-Status (Invoke-Api $a POST '/api/logout') 204 'logout'
    Assert-Status (Invoke-Api $a GET '/api/me') 401 'logged-out client rejected'
    Assert-Status (Invoke-Api $replay GET '/api/me') 401 'replayed old session rejected by server'
    Assert-Status (Invoke-Api $b POST '/api/logout') 204 'logout B'
    Write-Output "ACCEPTANCE PASS: $script:Checks checks; only generated test accounts/files used."
} finally {
    if ($null -ne $createdFile) { try { $null = Invoke-Api $a DELETE $createdFile } catch { } }
    foreach ($actor in $script:Clients) { $actor.Client.Dispose() }
}
