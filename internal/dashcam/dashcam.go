// Package dashcam decrypts Tesla 2026.20+ encrypted dashcam clips.
//
// The scheme (reverse-engineered from dashcam.tesla.com):
//   - Each ".mp4" is an eCryptfs-style container: an 0x2000-byte header region
//     followed by AES-128-CBC ciphertext in 4096-byte pages.
//   - The header carries the plaintext size, a file UUID, and per-file ownership
//     metadata (key_id, EC public key, VIN, timestamp, wrapped_key).
//   - The AES key is account-wrapped and can only be unwrapped by Tesla: we POST
//     the ownership metadata to /api/1/decrypt/batch with a bearer token and get
//     back the raw AES-128 key. Video bytes never leave the machine.
//   - Per-page IV = MD5( MD5(file_key) + ascii(page_number) padded to 32 bytes ).
//
// Reference: github.com/XGxF3/tesla-dashcam-decrypt (third_party/).
package dashcam

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
)

const (
	apiBase        = "https://dashcam.tesla.com"
	decryptBatchURL = apiBase + "/api/1/decrypt/batch"

	pageSize          = 4096
	ciphertextOffset  = 0x2000
	extendedHdrOffset = 0x1000

	uuidOffset = 4

	keyIDOffset     = extendedHdrOffset
	publicKeyOffset = keyIDOffset + 4
	publicKeySize   = 65
	vinOffset       = publicKeyOffset + publicKeySize
	vinSize         = 17
	timestampOffset = vinOffset + vinSize
	timestampSize   = 8
	wrappedKeyOffset = timestampOffset + timestampSize
	wrappedKeySize   = 44
)

// Header holds the metadata read from an encrypted clip that is needed to
// request its per-file key and to bound decryption.
type Header struct {
	ID            string // UUID string, used as the API item id
	VIN           string
	KeyID         uint32
	Timestamp     uint64
	WrappedKey    string // base64
	PublicKey     string // base64
	PlaintextSize uint64
}

// ReadHeader parses the encrypted-clip header. It requires a real Tesla
// container with the extended metadata block at 0x1000.
func ReadHeader(path string) (*Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, ciphertextOffset)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, fmt.Errorf("read header (%d bytes): %w", ciphertextOffset, err)
	}

	metaOff := binary.BigEndian.Uint32(buf[0x14:0x18])
	if metaOff != extendedHdrOffset {
		return nil, fmt.Errorf("not a real Tesla encrypted container (metadata offset 0x%x, want 0x%x)", metaOff, extendedHdrOffset)
	}

	plaintextSize := binary.BigEndian.Uint64(buf[0:8])
	if plaintextSize == 0 {
		return nil, fmt.Errorf("invalid plaintext size 0 in header")
	}

	rawUUID := buf[uuidOffset : uuidOffset+16]
	id := fmt.Sprintf("%x-%x-%x-%x-%x", rawUUID[0:4], rawUUID[4:6], rawUUID[6:8], rawUUID[8:10], rawUUID[10:16])

	pub := buf[publicKeyOffset : publicKeyOffset+publicKeySize]
	if pub[0] != 0x04 {
		return nil, fmt.Errorf("invalid EC public key in header (first byte 0x%x, want 0x04)", pub[0])
	}
	vin := string(bytes.TrimRight(buf[vinOffset:vinOffset+vinSize], "\x00"))
	if len(vin) != vinSize {
		return nil, fmt.Errorf("invalid VIN in header: %q", vin)
	}

	return &Header{
		ID:            id,
		VIN:           vin,
		KeyID:         binary.BigEndian.Uint32(buf[keyIDOffset : keyIDOffset+4]),
		Timestamp:     binary.BigEndian.Uint64(buf[timestampOffset : timestampOffset+timestampSize]),
		WrappedKey:    base64.StdEncoding.EncodeToString(buf[wrappedKeyOffset : wrappedKeyOffset+wrappedKeySize]),
		PublicKey:     base64.StdEncoding.EncodeToString(pub),
		PlaintextSize: plaintextSize,
	}, nil
}

type batchItem struct {
	ID         string `json:"id"`
	VIN        string `json:"vin"`
	KeyID      uint32 `json:"key_id"`
	Timestamp  uint64 `json:"timestamp"`
	WrappedKey string `json:"wrapped_key"`
	PublicKey  string `json:"public_key"`
}

// FetchKeys requests per-file AES keys from Tesla's decrypt endpoint. It returns
// a map of item id -> raw AES-128 key bytes.
func FetchKeys(ctx context.Context, httpc *http.Client, token string, headers []*Header) (map[string][]byte, error) {
	items := make([]batchItem, len(headers))
	for i, h := range headers {
		items[i] = batchItem{h.ID, h.VIN, h.KeyID, h.Timestamp, h.WrappedKey, h.PublicKey}
	}
	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, decryptBatchURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", apiBase)
	req.Header.Set("Referer", apiBase+"/")

	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("decrypt/batch request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("decrypt/batch returned 401: token invalid or expired")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("decrypt/batch returned %s: %s", resp.Status, string(raw))
	}

	var out struct {
		Results []struct {
			ID    string `json:"id"`
			Key   string `json:"key"`
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode decrypt/batch response: %w", err)
	}

	keys := make(map[string][]byte)
	for _, r := range out.Results {
		if r.Error != "" {
			return nil, fmt.Errorf("API error for %s: %s", r.ID, r.Error)
		}
		key, err := base64.StdEncoding.DecodeString(r.Key)
		if err != nil {
			return nil, fmt.Errorf("decode key for %s: %w", r.ID, err)
		}
		keys[r.ID] = key
	}
	return keys, nil
}

// pageIV derives the per-page IV: MD5( MD5(file_key) + ascii(page) padded to 32 ).
func pageIV(rootIV []byte, page int) []byte {
	material := make([]byte, 32)
	copy(material, rootIV)
	copy(material[len(rootIV):], strconv.Itoa(page))
	sum := md5.Sum(material)
	return sum[:]
}

// DecryptClip decrypts an encrypted clip to dst using the given raw AES-128 key.
// It returns the number of plaintext bytes written.
func DecryptClip(srcPath, dstPath string, h *Header, key []byte) (int64, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return 0, fmt.Errorf("AES key: %w", err)
	}
	rootSum := md5.Sum(key)
	rootIV := rootSum[:]

	src, err := os.Open(srcPath)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	if _, err := src.Seek(ciphertextOffset, io.SeekStart); err != nil {
		return 0, err
	}

	dst, err := os.Create(dstPath)
	if err != nil {
		return 0, err
	}
	defer dst.Close()

	target := int64(h.PlaintextSize)
	var written int64
	buf := make([]byte, pageSize)
	for page := 0; written < target; page++ {
		n, err := io.ReadFull(src, buf)
		if err == io.EOF {
			break
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return written, err
		}
		if n != pageSize {
			return written, fmt.Errorf("encrypted page %d is %d bytes, want %d", page, n, pageSize)
		}

		mode := cipher.NewCBCDecrypter(block, pageIV(rootIV, page))
		plain := make([]byte, pageSize)
		mode.CryptBlocks(plain, buf)

		if remaining := target - written; remaining < int64(len(plain)) {
			plain = plain[:remaining]
		}
		if _, err := dst.Write(plain); err != nil {
			return written, err
		}
		written += int64(len(plain))
	}
	if written != target {
		return written, fmt.Errorf("decrypted %d bytes, expected %d (truncated input?)", written, target)
	}
	return written, nil
}
