# Remote access over LTE (WireGuard)

How to reach a field unit's shell when it is in a car, on the metered LTE
dongle, with no public address.

## Why a tunnel is required

The Pi is behind **two** layers of NAT on LTE:

1. The dongle NATs the Pi into its own `192.168.8.0/24` RNDIS LAN.
2. The carrier (Hologram IoT SIM) puts the dongle behind CGNAT.

So the unit has no publicly routable address and **nothing can connect in to
it**. This is not a firewall setting — there is no address to dial. The only
way in is to have the Pi *initiate* an outbound tunnel to a host that does
have a public address, then ride back down it. That host is the existing
teslcam VPS, `161.35.232.246`.

## Topology

```
  Pi (CGNAT, LTE or Wi-Fi)                     VPS 161.35.232.246
  wg1  10.8.0.2/24  ── udp/51821 outbound ──▶  wg1  10.8.0.1/24
                                                     │
                          you ──ssh──▶ vps ──────────┘
```

`wg1` is deliberately **separate** from the pre-existing `wg0` on both hosts
(`10.0.0.0/24`, udp/51820). `wg0` on the VPS carries multi-TB production
traffic; nothing here touches it. The interface name, subnet, and port are all
distinct so the two cannot interact.

## Design invariants

These are the properties that keep the tunnel from becoming a data or security
problem. Preserve them when changing anything.

- **`AllowedIPs = 10.8.0.0/24`, never `0.0.0.0/0`.** A default route over the
  tunnel would pull *all* the unit's traffic across the metered SIM. The Pi's
  default route must stay on `wlan0`/`eth0`. Verify after any change:
  `ip route show | grep default` must not mention `wg1`.
- **`PersistentKeepalive = 60`.** Required because the Pi is behind NAT — the
  mapping must be refreshed from the inside or the VPS cannot reply. Costs
  roughly 1.5–2 MB/month idle. Do not lower it; 25s (the common default)
  roughly doubles that for no benefit here.
- **Egress allowlist.** `/etc/teslcam/lte-guard.nft` drops everything on `eth1`
  that is not explicitly permitted, so the tunnel needs its own rule:
  `ip daddr 161.35.232.246 udp dport 51821 accept`. Without it the handshake is
  silently dropped on LTE (it will still work on Wi-Fi, which is a confusing
  failure mode — check this first if a unit is reachable at the bench but not
  in the field).
- **One keypair per unit.** Never bake a key into the golden image: every unit
  would share a private key and collide on the same tunnel IP. Keys are
  generated per-unit at flash time (see below).

## Cloud firewall (one-time, easy to miss)

The droplet sits behind a **DigitalOcean cloud firewall**, which is upstream of
the host and invisible to `nft`/`iptables` on the box. UDP **51821** must be
allowed inbound there, or handshakes never arrive and both ends sit silent with
no error.

Symptom, and how to tell it apart from a local problem:

```sh
# on the VPS, while a unit is trying to connect
sudo tcpdump -ni any udp port 51821
```

Zero packets, while `udp port 51820` shows plenty, means the cloud firewall is
dropping them — not the host. Fix it in the DigitalOcean control panel under
Networking → Firewalls.

## Provisioning a new unit

`scripts/flash-image.sh` handles this at flash time. After writing the image it
offers to provision remote access, and if you accept it:

1. generates a fresh keypair on the Mac (needs `brew install wireguard-tools`),
2. assigns `10.8.0.<unit+1>` from the unit number you give it,
3. writes `teslcam-wg1.conf` onto the FAT boot partition (the Mac cannot write
   the ext4 rootfs, so the config is handed over through `/boot/firmware`),
4. prints the unit's public key and offers to register it on the VPS.

On first boot, `teslcam-wg-provision.service` moves that file into
`/etc/wireguard/wg1.conf` with mode `0600`, **deletes it from the FAT
partition** (so no private key is left on a partition any machine can read),
and enables `wg-quick@wg1`. The service is idempotent and runs every boot, so
dropping a replacement `teslcam-wg1.conf` onto the boot partition re-provisions
a unit without a reflash.

To register a unit on the hub by hand:

```sh
ssh adri@vps "sudo wg set wg1 peer <UNIT_PUBKEY> allowed-ips 10.8.0.<N>/32 && \
              sudo wg-quick save wg1"
```

## Connecting

```sh
ssh -J adri@vps adri@10.8.0.2      # unit 1
```

SSH is key-only on the units (`PasswordAuthentication no`,
`KbdInteractiveAuthentication no`), so a leaked tunnel address alone grants
nothing.

## Operational limits

- **The unit only has power when the car is awake.** Tesla cuts the glovebox
  USB port when the car sleeps, so remote access exists in those windows only —
  it is not on-demand. A unit that does not answer is most likely unpowered
  rather than broken.
- Handshakes can take up to `PersistentKeepalive` seconds after the link comes
  up. Waiting a minute before concluding a unit is down is worth it.

## Troubleshooting order

1. `sudo wg show wg1` on the VPS — is there a recent handshake for that peer?
2. If no handshake from any unit: cloud firewall (see above).
3. If no handshake from one unit on LTE but fine on Wi-Fi: the `lte-guard`
   allowlist rule is missing or the unit's nft table did not load. Check
   `sudo nft list table inet lte_guard` and the `lte-guard drop:` kernel log
   prefix, which counts what it rejected.
4. If handshake is fine but SSH hangs: check the unit is not thermally
   throttled mid-upload; `journalctl -u teslcam-agent | grep health`.
