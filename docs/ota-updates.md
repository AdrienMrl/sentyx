# Fleet OTA updates

The fleet updater is pull-based. Operators publish a signed release and
activate a campaign; every Pi polls the server with its existing device token.
This remains reliable while units are offline or behind carrier CGNAT.

## Components and trust

- teslcam-ota: operator CLI for keys, releases and campaigns.
- teslcam-updater: root service installed independently from the application.
- internal/ota: manifests and Ed25519 verification.
- Server /v1/ota API: release storage and rollout control.
- /run/teslcam/update-ready.sock: local interlock exposed by the agent.

The signing private key must never be copied to a Pi or the VPS. The golden
image contains only the public key at /etc/teslcam/ota-release.pub.pem.

## Setup

    go run ./cmd/teslcam-ota keygen -out keys/ota-release
    scripts/build-image.sh 64 ~/.ssh/id_ed25519.pub keys/ota-release.public.pem

For an existing prototype:

    scripts/install-agent.sh ota keys/ota-release.public.pem

Back up the private key offline. Losing it requires a separately authenticated
operation to provision a new trust anchor on deployed units.

## Versioning

Releases use CalVer: `YYYY.M.N`, where `N` counts releases within the month
(`2026.8.1` is the first release of August 2026; `N` resets each month). Cut a
release by tagging main with a `v` prefix:

    git tag -a v2026.8.1 -m "..." && git push origin v2026.8.1

Build scripts derive the reported agent version from `git describe --tags`, so
an exact tagged build reports `2026.8.1` and a dev build
`2026.8.1-3-g1f39698[-dirty]` — this is the string shown in heartbeats, the
admin dashboard and BLE onboarding. The human-facing version carries no
ordering semantics: the updater orders strictly by the manifest `sequence`, a
fleet-wide monotonic counter that never resets. Use `app-<version>` /
`sys-<version>` as release IDs.

## Application release

    teslcam-ota bundle-app \
      -id app-2026.8.1 -version 2026.8.1 -sequence 14 \
      -agent build/teslcam-agent \
      -scorer build/teslcam-camera-scorer \
      -key keys/ota-release.private.pem \
      -out build/app-2026.8.1.tar.gz

    teslcam-ota publish \
      -server https://teslcam.example.com \
      -token-file .secrets/operator.token \
      -release build/app-2026.8.1.tar.gz.release.json \
      -artifact build/app-2026.8.1.tar.gz

The updater resumes interrupted downloads, verifies size, SHA-256 and the
signature, waits for the Tesla-write interlock, and installs under
/opt/teslcam/releases. It atomically changes /opt/teslcam/current. A failed
agent healthcheck restores and restarts the previous release.

## Controlled APT release

System releases contain exact package versions and never run a generic
apt full-upgrade. Example unsigned manifest:

    {
      "v": 1,
      "id": "system-2026.08.1",
      "type": "system",
      "version": "2026.08.1",
      "sequence": 1,
      "hardware": ["pi4"],
      "osCodename": "trixie",
      "system": {
        "packages": [
          {"name": "ffmpeg", "version": "7:7.1.1-1+rpt1"}
        ],
        "backupPaths": ["/etc/teslcam"],
        "postInstall": ["systemctl daemon-reload"],
        "reboot": true,
        "maxAttempts": 2
      }
    }

Sign and publish it:

    teslcam-ota sign-system \
      -manifest system-2026.08.1.json \
      -key keys/ota-release.private.pem \
      -out system-2026.08.1.release.json

    teslcam-ota publish -server https://teslcam.example.com \
      -token-file .secrets/operator.token \
      -release system-2026.08.1.release.json

The updater runs apt-get update, a simulation, a complete download, and only
then the pinned installation. Pre/post commands are trusted only because the
whole manifest is signed. APT updates have no full OS rollback; recovery during
beta remains WireGuard/SSH or reflash.

## Progressive rollout

Explicit canary:

    teslcam-ota campaign -server https://teslcam.example.com \
      -token-file .secrets/operator.token -release app-v1.4.0 \
      -percent 100 -devices sentyx-canary-01

Fleet cohort, then expansion:

    teslcam-ota campaign -server https://teslcam.example.com \
      -token-file .secrets/operator.token -release app-v1.4.0 -percent 1

    teslcam-ota campaign-update -server https://teslcam.example.com \
      -token-file .secrets/operator.token -id cmp-... -percent 5

    teslcam-ota status -server https://teslcam.example.com \
      -token-file .secrets/operator.token

Campaigns may be active, paused, or cancelled. Any rollback pauses a campaign
immediately; three failed devices also pause it. Installed devices are not
offered the same release again, and each Pi rejects replay or downgrade by
remembering the highest installed sequence independently for application and
system releases.

## User-requested updates from the app

The phone app can ask for an update on a device its owner has paired, with
`POST /v1/devices/<deviceId>/updates/request` (Supabase user JWT, operator
token, or that device's own token). The server picks the newest **application**
release that has an artifact uploaded and opens a campaign pinned to that one
device at 100%. Everything downstream is the operator path unchanged: the unit
pulls the plan, verifies the Ed25519 manifest against the public key in its
image, waits for `update-ready`, and rolls back on a failed healthcheck.

- The app is a trigger, never a delivery path — no artifact or key touches it.
- Repeating the request reuses the pending offer; it cannot open two campaigns.
- A device already on the newest release gets `409`; nothing installable
  published yet gets `404`.
- System (APT) releases are never offered this way — no rollback means recovery
  is WireGuard or a reflash, so they stay operator-driven.
- These campaigns show up in `teslcam-ota status` alongside operator ones, and a
  user retry after a rollback opens a fresh campaign rather than resuming the
  paused one.

`GET /v1/devices/<deviceId>` carries the matching state back to the app in an
`update` object (`available`, `version`, `notes`, `state`, `progressPct`,
`error`), where `state` is the device's own last report.

## Failure model

- Network loss: retain and resume the partial application download.
- Corrupt or forged artifact: reject before extraction.
- Power loss: persist updater state atomically; dpkg performs normal recovery.
- Tesla writing: remain in waiting-safe.
- Broken application: roll back the current symlink.
- Broken OS or kernel: no automatic rollback in the beta APT design. Adopt an
  A/B image updater before production-scale unattended OS updates.
