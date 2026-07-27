// Command tesla-ble-pair is a proof of concept that enrolls a self-held P-256
// keypair into a Tesla's VCSEC whitelist over BLE from a Mac.
//
// Why: dashcam clips never exist server-side — the Tesla app pulls them
// peer-to-peer from the car over a WebRTC DataChannel, and the car only
// authorizes a session for a key it has whitelisted. Holding our own
// whitelisted key is the prerequisite for any non-app client. See
// docs/tesla-app-traffic-capture.md.
//
// The same keypair works for Tesla's Fleet API "virtual key" flow, which
// enrolls without physical proximity: serve the printed public key at
// https://<domain>/.well-known/appspecific/com.tesla.3p.public-key.pem and
// have the owner approve at https://tesla.com/_ak/<domain>. Both routes end up
// putting this key in the same VCSEC whitelist, so `genkey` output is reusable
// either way — use `pubkey` to emit the file the Fleet API route needs.
//
// Authorization: operates only on a vehicle the operator owns. Pairing still
// requires physical possession of a key card, which is the car's own consent
// gate — this tool cannot bypass it.
package main

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/teslamotors/vehicle-command/pkg/connector/ble"
	"github.com/teslamotors/vehicle-command/pkg/protocol"
	"github.com/teslamotors/vehicle-command/pkg/protocol/protobuf/keys"
	"github.com/teslamotors/vehicle-command/pkg/protocol/protobuf/universalmessage"
	"github.com/teslamotors/vehicle-command/pkg/protocol/protobuf/vcsec"
	"github.com/teslamotors/vehicle-command/pkg/vehicle"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	keyPath := fs.String("key", defaultKeyPath(), "path to the P-256 private key (PEM)")
	vin := fs.String("vin", os.Getenv("TESLA_VIN"), "vehicle VIN (or set TESLA_VIN)")
	timeout := fs.Duration("timeout", 90*time.Second, "overall timeout for BLE operations")
	enrollHex := fs.String("enroll-pubkey-hex", "", "pair: enroll THIS uncompressed P-256 point (04||X||Y hex) instead of -key's public key")

	if err := fs.Parse(os.Args[2:]); err != nil {
		os.Exit(2)
	}

	var err error
	switch cmd {
	case "genkey":
		err = genKey(*keyPath)
	case "pubkey":
		err = printPubKey(*keyPath)
	case "scan":
		err = scan(*vin, *timeout)
	case "pair":
		err = pair(*keyPath, *vin, *timeout, *enrollHex)
	case "verify":
		err = verify(*keyPath, *vin, *timeout)
	default:
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `tesla-ble-pair — enroll a self-held key into a Tesla's BLE whitelist

Usage:
  tesla-ble-pair genkey [-key PATH]              generate a P-256 keypair
  tesla-ble-pair pubkey [-key PATH]              print the public key (Fleet API virtual-key format)
  tesla-ble-pair scan   -vin VIN [-timeout D]    find the car's BLE beacon (no pairing)
  tesla-ble-pair pair   -vin VIN [-key PATH]     enroll the key (requires key-card tap)
  tesla-ble-pair verify -vin VIN [-key PATH]     prove the key is whitelisted (read-only)

Run 'scan' first to confirm the Mac can see the car before attempting to pair.
`)
}

func defaultKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "tesla-ble-key.pem"
	}
	return filepath.Join(home, ".teslcam", "tesla-ble-key.pem")
}

// genKey writes a NIST P-256 private key. P-256 is not a preference — it is the
// only curve Tesla's VCSEC protocol and the Fleet API virtual-key flow accept.
func genKey(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to overwrite existing key at %s\n"+
			"    (a regenerated key would have to be re-enrolled in the car)", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate P-256 key: %w", err)
	}
	der, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("marshal EC private key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	// 0600: this key is the credential the car trusts. Anyone holding it can
	// command the vehicle within the granted role.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create key file: %w", err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}); err != nil {
		return fmt.Errorf("write PEM: %w", err)
	}

	fmt.Printf("wrote private key: %s (mode 0600)\n", path)
	fmt.Println("keep this file secret — it is the credential the car will trust")
	return nil
}

// printPubKey emits the SPKI PEM that Tesla's Fleet API virtual-key flow expects
// to be served at /.well-known/appspecific/com.tesla.3p.public-key.pem
func printPubKey(path string) error {
	// LoadPublicKey accepts the private-key PEM and derives the public half.
	pubKey, err := publicKeyFor(path)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKIXPublicKey(pubKey)
	if err != nil {
		return fmt.Errorf("marshal public key: %w", err)
	}
	return pem.Encode(os.Stdout, &pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

func publicKeyFor(path string) (*ecdh.PublicKey, error) {
	pubKey, err := protocol.LoadPublicKey(path)
	if err != nil {
		return nil, fmt.Errorf("derive public key from %s: %w\n"+
			"    (run 'tesla-ble-pair genkey' first)", path, err)
	}
	return pubKey, nil
}

// publicKeyToEnroll returns the external hex point if given, else our own key.
func publicKeyToEnroll(keyPath, enrollHex string) (*ecdh.PublicKey, error) {
	if enrollHex == "" {
		return publicKeyFor(keyPath)
	}
	raw, err := hex.DecodeString(enrollHex)
	if err != nil {
		return nil, fmt.Errorf("decode -enroll-pubkey-hex: %w", err)
	}
	pk, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse P-256 point (need 65-byte uncompressed 04||X||Y): %w", err)
	}
	return pk, nil
}

func loadKey(path string) (protocol.ECDHPrivateKey, error) {
	privKey, err := protocol.LoadPrivateKey(path)
	if err != nil {
		return nil, fmt.Errorf("load private key from %s: %w\n"+
			"    (run 'tesla-ble-pair genkey' first)", path, err)
	}
	return privKey, nil
}

func requireVIN(vin string) error {
	if vin == "" {
		return errors.New("no VIN given: pass -vin or set TESLA_VIN")
	}
	if len(vin) != 17 {
		return fmt.Errorf("VIN %q is %d characters, expected 17", vin, len(vin))
	}
	return nil
}

// scan verifies the Mac's BLE radio can actually see the car before we attempt
// the pairing ceremony. Tesla vehicles advertise a name derived from a hash of
// the VIN, so this confirms both radio range and that we have the right VIN.
func scan(vin string, timeout time.Duration) error {
	if err := requireVIN(vin); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	fmt.Printf("scanning for vehicle %s (BLE range is ~5-10 m — be next to the car)...\n", vin)
	result, err := ble.ScanVehicleBeacon(ctx, vin)
	if err != nil {
		return fmt.Errorf("scan for vehicle beacon: %w\n%s", err, bleTroubleshooting())
	}

	fmt.Printf("\nfound the car\n")
	fmt.Printf("  address:      %s\n", result.Address)
	fmt.Printf("  local name:   %s\n", result.LocalName)
	fmt.Printf("  RSSI:         %d dBm", result.RSSI)
	switch {
	case result.RSSI > -70:
		fmt.Print("  (strong)\n")
	case result.RSSI > -85:
		fmt.Print("  (usable)\n")
	default:
		fmt.Print("  (weak — move closer before pairing)\n")
	}

	// The car advertises Connectable=false once it has hit its BLE client
	// limit. Pairing cannot proceed in that state, so report it here rather
	// than letting `pair` fail with a less obvious error.
	fmt.Printf("  connectable:  %t", result.Connectable)
	if result.Connectable {
		fmt.Print("  (ready to pair)\n")
		fmt.Println("\nnext: tesla-ble-pair pair -vin " + vin)
		return nil
	}

	fmt.Print("  (NOT ready — car is at its BLE connection limit)\n")
	fmt.Print(`
The car accepts only a few simultaneous BLE clients, and they are all taken.
Free one up, then re-run scan until connectable is true:

  - turn Bluetooth OFF on any phone paired as a phone key (the usual cause —
    quitting the Tesla app is not enough, the phone key holds BLE at OS level)
  - do the same for any other household phone that can unlock this car
  - walk other paired phones out of range
  - if you just disconnected something, wait ~30 s for the car to update

`)
	return errors.New("vehicle not accepting new BLE connections")
}

// pair sends AddKeyToWhitelistAndAddPermissions over BLE. The car will not
// accept it until someone taps a key card on the center console — that tap is
// the car's consent gate and cannot be bypassed from software.
func pair(keyPath, vin string, timeout time.Duration, enrollHex string) error {
	if err := requireVIN(vin); err != nil {
		return err
	}
	// The local private key is only the BLE session identity here; enrollment is
	// authorized by the key-card tap (SIGNATURE_TYPE_PRESENT_KEY), not by it.
	privKey, err := loadKey(keyPath)
	if err != nil {
		return err
	}

	// By default enroll our own public key. With -enroll-pubkey-hex we enroll a
	// *different* key (e.g. the Tesla app's own phone key, so the real app on the
	// emulator becomes a valid paired device) — we won't hold its private key,
	// which is fine: the car card-taps the pending request regardless.
	pubKey, err := publicKeyToEnroll(keyPath, enrollHex)
	if err != nil {
		return err
	}
	if enrollHex != "" {
		fmt.Println("enrolling EXTERNAL public key (not our own):", enrollHex[:16]+"...")
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	fmt.Printf("connecting to %s over BLE...\n", vin)
	conn, err := ble.NewConnection(ctx, vin)
	if err != nil {
		return fmt.Errorf("open BLE connection: %w\n%s", err, bleTroubleshooting())
	}
	defer conn.Close()

	car, err := vehicle.NewVehicle(conn, privKey, nil)
	if err != nil {
		return fmt.Errorf("create vehicle client: %w", err)
	}
	if err := car.Connect(ctx); err != nil {
		return fmt.Errorf("connect to vehicle: %w", err)
	}
	defer car.Disconnect()

	// No StartSession before enrollment: the car has no key for us yet — that is
	// precisely what this establishes.
	//
	// SendAddKeyRequestWithRole is fire-and-forget (it writes the BLE
	// characteristic and returns), so it cannot tell us whether the car
	// accepted. The car's consent window after each request is short, so rather
	// than making the operator race a prompt, re-send periodically and poll for
	// enrollment until the deadline.
	fmt.Print(`
>>> TAP YOUR KEY CARD ON THE CENTER CONSOLE NOW <<<

    Do NOT wait for a prompt on the car's screen — there isn't one. The
    protocol uses SIGNATURE_TYPE_PRESENT_KEY: the car passively waits for a
    key-card tap to authorize the pending request, and displays nothing.

    Model 3: lay the card flat on the console just BEHIND THE CUPHOLDERS
    (same spot used to authorize a drive), not the B-pillar.

    The request is re-sent every few seconds, so there is no timing race.
    Tap, pause, and tap again if the attempt counter keeps climbing.

`)

	const resendEvery = 6 * time.Second
	external := enrollHex != ""
	attempt := 0
	for {
		attempt++
		if err := car.SendAddKeyRequestWithRole(ctx, pubKey, keys.Role_ROLE_OWNER,
			vcsec.KeyFormFactor_KEY_FORM_FACTOR_CLOUD_KEY); err != nil {
			return fmt.Errorf("send add-key request: %w", err)
		}

		if external {
			// We hold no private key for an external key, so we cannot verify by
			// session handshake — keep sending across the tap window; the real
			// confirmation is the app flipping to paired.
			fmt.Printf("  attempt %d: sent add-key for the external key...\n", attempt)
			select {
			case <-ctx.Done():
				fmt.Printf("\nSent %d add-key requests for the external key.\n", attempt)
				fmt.Println("Verify on the DEVICE that owns this key (e.g. the emulator's Tesla app):")
				fmt.Println("  its 'Phone Key' should now show paired/connected.")
				fmt.Println("Also check Controls -> Locks on your own phone: an extra key should appear.")
				return nil
			case <-time.After(resendEvery):
				continue
			}
		}

		// Own-key path: a session handshake succeeds only once the car holds our key.
		probeCtx, probeCancel := context.WithTimeout(ctx, resendEvery)
		sessErr := car.StartSession(probeCtx, []universalmessage.Domain{
			universalmessage.Domain_DOMAIN_VEHICLE_SECURITY,
		})
		probeCancel()

		if sessErr == nil {
			fmt.Printf("\nENROLLED AND VERIFIED (after %d attempt(s)): the car authenticated this key.\n", attempt)
			fmt.Printf("private key: %s\n", keyPath)
			fmt.Println("\nCross-check in the Tesla app: Controls -> Locks should list a new key.")
			fmt.Println("Remove it there if this was only a test.")
			return nil
		}

		if ctx.Err() != nil {
			return fmt.Errorf("the car did NOT enroll the key within the timeout (last error: %v)\n"+
				"    most likely causes, in order:\n"+
				"      1. the key card was not tapped on the right spot (behind the cupholders)\n"+
				"      2. the car is asleep — open a door or press the brake, then retry\n"+
				"      3. the card is not a valid key for THIS car\n"+
				"    retry with a longer -timeout if you need more time", sessErr)
		}
		fmt.Printf("  attempt %d: not enrolled yet, re-sending request...\n", attempt)
	}
}

// verify proves the key is actually in the car's whitelist. `pair` reporting
// success only means the request was delivered — the car silently ignores it if
// nobody taps the key card. Establishing an authenticated session is the real
// proof, because the session handshake requires the car to hold our public key.
//
// This is read-only: it starts a session and sends no commands.
func verify(keyPath, vin string, timeout time.Duration) error {
	if err := requireVIN(vin); err != nil {
		return err
	}
	privKey, err := loadKey(keyPath)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	conn, err := ble.NewConnection(ctx, vin)
	if err != nil {
		return fmt.Errorf("open BLE connection: %w\n%s", err, bleTroubleshooting())
	}
	defer conn.Close()

	car, err := vehicle.NewVehicle(conn, privKey, nil)
	if err != nil {
		return fmt.Errorf("create vehicle client: %w", err)
	}
	if err := car.Connect(ctx); err != nil {
		return fmt.Errorf("connect to vehicle: %w", err)
	}
	defer car.Disconnect()

	// A session handshake with the VCSEC domain only completes if the car
	// recognizes our public key. Failure here means enrollment did not take.
	if err := car.StartSession(ctx, []universalmessage.Domain{
		universalmessage.Domain_DOMAIN_VEHICLE_SECURITY,
	}); err != nil {
		return fmt.Errorf("authenticate to vehicle: %w\n"+
			"    the key is NOT whitelisted — re-run 'pair' and tap the key card\n"+
			"    within a couple of seconds of the prompt", err)
	}

	fmt.Println("VERIFIED: the car authenticated this key.")
	fmt.Printf("  vin:         %s\n", vin)
	fmt.Printf("  private key: %s\n", keyPath)
	fmt.Println("\nThe key is in the vehicle's VCSEC whitelist. It can now sign")
	fmt.Println("vehicle commands, and is the credential a dashcam client would use.")
	return nil
}

func bleTroubleshooting() string {
	return `
  troubleshooting:
    - the Mac must be within BLE range (~5-10 m) of the car
    - grant Bluetooth permission: System Settings -> Privacy & Security -> Bluetooth
    - the car must be awake: open the Tesla app or open a door
    - only one BLE client at a time: quit the Tesla app on nearby phones
    - 'scan' before 'pair' to confirm the radio sees the beacon`
}
