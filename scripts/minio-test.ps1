$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
# Official MinIO release source. Binary and server data stay disposable; neither
# command starts the normal app nor mounts its data volume.
docker compose --profile tools run --rm --no-deps -e GOBIN=/workspace/.tmp/full-a/minio-bin tools go install github.com/minio/minio@RELEASE.2025-04-22T22-12-26Z
if ($LASTEXITCODE -ne 0) { throw 'Could not build pinned MinIO test server.' }
docker compose -f compose.minio-test.yaml --profile minio-test up -d minio
if ($LASTEXITCODE -ne 0) { throw 'Could not start isolated MinIO.' }
try {
    docker compose -f compose.minio-test.yaml --profile minio-test run --rm --no-deps tools
    if ($LASTEXITCODE -ne 0) { throw 'MinIO integration failed.' }
} finally {
    docker compose -f compose.minio-test.yaml --profile minio-test stop minio
    docker compose -f compose.minio-test.yaml --profile minio-test rm -f minio
}
